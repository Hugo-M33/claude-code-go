// Command chat keeps one Claude Code session open and sends it each line typed, printing the
// replies; Ctrl+C during a reply interrupts it.
//
//	go run ./examples/chat
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"

	claudecode "github.com/Hugo-M33/claude-code-go"
)

func main() {
	ctx := context.Background()
	c, err := claudecode.Connect(ctx, claudecode.Options{
		Tools:                  claudecode.Strings(), // a plain conversation: no tools
		IncludePartialMessages: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	in := bufio.NewScanner(os.Stdin)
	for fmt.Print("> "); in.Scan(); fmt.Print("> ") {
		if in.Text() == "" {
			continue
		}
		if err := c.Send(ctx, in.Text()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		turn, stop := context.WithCancel(ctx)
		go func() {
			select {
			case <-interrupts:
				_ = c.Interrupt(ctx)
			case <-turn.Done():
			}
		}()
		for m, err := range c.Turn(ctx) {
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				stop()
				return
			}
			if ev, ok := m.(*claudecode.StreamEvent); ok && ev.Delta.Type == "text_delta" {
				fmt.Print(ev.Delta.Text)
			}
		}
		stop()
		fmt.Println()
	}
}
