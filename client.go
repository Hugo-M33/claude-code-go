package claudecode

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// maxLine bounds one line of the CLI's output (a large tool input or file read fits).
const maxLine = 64 << 20

// closeGrace is how long Close waits for the process to end on its own before killing it.
const closeGrace = 5 * time.Second

// Client is a running `claude` process: send it prompts, read what it does. Its methods are
// safe for concurrent use; messages are delivered in order on one channel.
type Client struct {
	opts    Options
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	wmu     sync.Mutex
	servers map[string]*MCPServer
	stderr  *tail

	pmu      sync.Mutex
	pending  map[string]chan controlResult
	inflight map[string]context.CancelFunc
	counter  atomic.Uint64

	queue *queue
	out   chan Message
	done  chan struct{} // the process has ended and its output is read
	err   error         // why it ended, when not asked to

	ctx       context.Context // handlers of the CLI's requests
	cancel    context.CancelFunc
	closing   atomic.Bool
	closeOnce sync.Once

	init json.RawMessage
}

type controlResult struct {
	response json.RawMessage
	err      error
}

// Connect starts `claude` with the options and completes the protocol's handshake.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	path := opts.CLIPath
	if path == "" {
		p, err := FindCLI()
		if err != nil {
			return nil, err
		}
		path = p
	}
	args, err := opts.args()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = opts.env()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	st := &tail{max: 8 << 10}
	if opts.Stderr != nil {
		cmd.Stderr = io.MultiWriter(st, opts.Stderr)
	} else {
		cmd.Stderr = st
	}
	if err := cmd.Start(); err != nil {
		var nf *exec.Error
		if errors.As(err, &nf) || errors.Is(err, fs.ErrNotExist) {
			return nil, &CLINotFoundError{Searched: []string{path}}
		}
		return nil, fmt.Errorf("claudecode: start %s: %w", path, err)
	}
	hctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		opts: opts, cmd: cmd, stdin: stdin, stderr: st,
		servers:  map[string]*MCPServer{},
		pending:  map[string]chan controlResult{},
		inflight: map[string]context.CancelFunc{},
		queue:    newQueue(), out: make(chan Message), done: make(chan struct{}),
		ctx: hctx, cancel: cancel,
	}
	for _, s := range opts.SDKServers {
		c.servers[s.Name] = s
	}
	go c.pump()
	go c.read(stdout)
	resp, err := c.control(ctx, map[string]any{"subtype": "initialize", "hooks": nil})
	if err != nil {
		c.Close()
		return nil, err
	}
	c.init = resp
	return c, nil
}

// Init is the CLI's answer to the handshake (its commands, output styles, account…).
func (c *Client) Init() json.RawMessage { return c.init }

// Send sends a prompt as the user's next message.
func (c *Client) Send(ctx context.Context, prompt string) error {
	return c.SendContent(ctx, prompt)
}

// SendContent sends a user message whose content is a string or a list of content blocks
// (text, image…), as the Messages API takes them.
func (c *Client) SendContent(ctx context.Context, content any) error {
	return c.write(map[string]any{
		"type":               "user",
		"session_id":         "",
		"parent_tool_use_id": nil,
		"message":            map[string]any{"role": "user", "content": content},
	})
}

// Messages delivers what the CLI writes, in order; it is closed when the process has ended
// (Err then says why, if it was not closed on purpose).
func (c *Client) Messages() <-chan Message { return c.out }

// Receive returns the next message, or io.EOF once the process has ended.
func (c *Client) Receive(ctx context.Context) (Message, error) {
	select {
	case m, ok := <-c.out:
		if !ok {
			if err := c.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Turn yields the messages of the current turn, the ResultMessage that ends it included.
func (c *Client) Turn(ctx context.Context) iter.Seq2[Message, error] {
	return func(yield func(Message, error) bool) {
		for {
			m, err := c.Receive(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = ErrClosed
				}
				yield(nil, err)
				return
			}
			if !yield(m, nil) {
				return
			}
			if _, ok := m.(*ResultMessage); ok {
				return
			}
		}
	}
}

// Interrupt stops the turn in progress; its ResultMessage follows.
func (c *Client) Interrupt(ctx context.Context) error {
	_, err := c.control(ctx, map[string]any{"subtype": "interrupt"})
	return err
}

// SetModel switches the model for the next requests.
func (c *Client) SetModel(ctx context.Context, model string) error {
	_, err := c.control(ctx, map[string]any{"subtype": "set_model", "model": model})
	return err
}

// SetPermissionMode switches the permission mode.
func (c *Client) SetPermissionMode(ctx context.Context, mode string) error {
	_, err := c.control(ctx, map[string]any{"subtype": "set_permission_mode", "mode": mode})
	return err
}

// Control sends a control request this package has no method for, and returns the CLI's
// response.
func (c *Client) Control(ctx context.Context, request map[string]any) (json.RawMessage, error) {
	return c.control(ctx, request)
}

// Err says why the process ended when it was not closed on purpose; nil while it runs.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// Close ends the session: standard input is closed so the CLI finishes, and the process is
// killed if it has not ended a few seconds later.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		c.wmu.Lock()
		c.stdin.Close()
		c.wmu.Unlock()
		select {
		case <-c.done:
		case <-time.After(closeGrace):
			_ = c.cmd.Process.Kill()
			<-c.done
		}
		c.cancel()
	})
	return nil
}

// --- Plumbing -----------------------------------------------------------------------------------

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.done:
		return ErrClosed
	default:
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("claudecode: write to claude: %w", err)
	}
	return nil
}

func (c *Client) control(ctx context.Context, request map[string]any) (json.RawMessage, error) {
	id := fmt.Sprintf("req_%d_%s", c.counter.Add(1), randomHex(4))
	ch := make(chan controlResult, 1)
	c.pmu.Lock()
	c.pending[id] = ch
	c.pmu.Unlock()
	defer func() {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
	}()
	if err := c.write(map[string]any{"type": "control_request", "request_id": id, "request": request}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.response, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		if c.err != nil {
			return nil, c.err
		}
		return nil, ErrClosed
	}
}

// read routes the CLI's output: answers to our control requests, its own requests (MCP,
// permissions), and messages for the caller.
func (c *Client) read(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var head struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Request   json.RawMessage `json:"request"`
			Response  struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
				Error     string          `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			// Not a protocol line (a stray print): keep it with the diagnostics.
			c.stderr.Write(append(append([]byte("stdout: "), line...), '\n'))
			continue
		}
		switch head.Type {
		case "control_response":
			c.pmu.Lock()
			ch := c.pending[head.Response.RequestID]
			c.pmu.Unlock()
			if ch != nil {
				r := controlResult{response: head.Response.Response}
				if head.Response.Subtype == "error" {
					r.err = &ControlError{Subtype: subtypeOf(head.Request), Message: head.Response.Error}
				}
				ch <- r
			}
		case "control_request":
			id := head.RequestID
			req := append(json.RawMessage(nil), head.Request...)
			ctx, cancel := context.WithCancel(c.ctx)
			c.pmu.Lock()
			c.inflight[id] = cancel
			c.pmu.Unlock()
			go c.answer(ctx, id, req)
		case "control_cancel_request":
			c.pmu.Lock()
			if cancel := c.inflight[head.RequestID]; cancel != nil {
				cancel()
			}
			c.pmu.Unlock()
		case "keep_alive":
		default:
			m, err := ParseMessage(line)
			if err != nil {
				c.stderr.Write([]byte(err.Error() + "\n"))
				continue
			}
			c.queue.push(m)
		}
	}
	readErr := sc.Err()
	waitErr := c.cmd.Wait()
	if !c.closing.Load() {
		switch {
		case waitErr != nil:
			code := -1
			var ee *exec.ExitError
			if errors.As(waitErr, &ee) {
				code = ee.ExitCode()
			}
			c.err = &ProcessError{ExitCode: code, Stderr: c.stderr.String()}
		case readErr != nil:
			c.err = fmt.Errorf("claudecode: read from claude: %w", readErr)
		}
	}
	close(c.done)
	c.queue.close()
}

func subtypeOf(req json.RawMessage) string {
	var r struct {
		Subtype string `json:"subtype"`
	}
	_ = json.Unmarshal(req, &r)
	if r.Subtype == "" {
		return "a control request"
	}
	return r.Subtype
}

// answer handles one request of the CLI and writes its response.
func (c *Client) answer(ctx context.Context, id string, raw json.RawMessage) {
	defer func() {
		c.pmu.Lock()
		if cancel := c.inflight[id]; cancel != nil {
			cancel()
			delete(c.inflight, id)
		}
		c.pmu.Unlock()
	}()
	var req struct {
		Subtype    string          `json:"subtype"`
		ServerName string          `json:"server_name"`
		Message    json.RawMessage `json:"message"`
	}
	resp, err := func() (any, error) {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		switch req.Subtype {
		case "mcp_message":
			s := c.servers[req.ServerName]
			if s == nil {
				return nil, fmt.Errorf("no in-process MCP server named %q", req.ServerName)
			}
			return map[string]any{"mcp_response": s.Handle(ctx, req.Message)}, nil
		case "can_use_tool":
			if c.opts.CanUseTool == nil {
				return nil, errors.New("no CanUseTool callback is set")
			}
			var pr PermissionRequest
			if err := json.Unmarshal(raw, &pr); err != nil {
				return nil, err
			}
			pr.Raw = raw
			res, err := c.opts.CanUseTool(ctx, pr)
			if err != nil {
				return nil, err
			}
			return res.wire(pr.Input), nil
		}
		return nil, fmt.Errorf("claude-code-go does not handle %q requests", req.Subtype)
	}()
	if ctx.Err() != nil {
		return // the CLI gave the request up: it expects no answer
	}
	response := map[string]any{"subtype": "success", "request_id": id, "response": resp}
	if err != nil {
		response = map[string]any{"subtype": "error", "request_id": id, "error": err.Error()}
	}
	_ = c.write(map[string]any{"type": "control_response", "response": response})
}

// pump hands queued messages to the channel, so a slow reader never blocks the CLI's
// requests (an MCP call waits for its answer while messages pile up).
func (c *Client) pump() {
	defer close(c.out)
	for {
		m, ok := c.queue.pop()
		if !ok {
			return
		}
		c.out <- m
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// queue is an unbounded FIFO of messages.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []Message
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *queue) push(m Message) {
	q.mu.Lock()
	q.items = append(q.items, m)
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *queue) pop() (Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return nil, false
	}
	m := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	return m, true
}

// tail keeps the end of a stream.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// Query runs one prompt in a new process and yields its messages until the turn's result,
// then ends the process.
func Query(ctx context.Context, prompt string, opts Options) iter.Seq2[Message, error] {
	return func(yield func(Message, error) bool) {
		c, err := Connect(ctx, opts)
		if err != nil {
			yield(nil, err)
			return
		}
		defer c.Close()
		if err := c.Send(ctx, prompt); err != nil {
			yield(nil, err)
			return
		}
		for m, err := range c.Turn(ctx) {
			if !yield(m, err) || err != nil {
				return
			}
		}
	}
}
