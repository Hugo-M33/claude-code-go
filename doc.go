// Package claudecode drives Claude Code from Go: it runs the `claude` command-line program as a
// subprocess and speaks the same stream-json protocol as Anthropic's official Agent SDKs for
// Python and TypeScript. It is not an official Anthropic package.
//
// A Client starts `claude`, sends it prompts and reads back what it does: system messages,
// assistant and user messages, partial message events while it streams, and a result at the end
// of each turn. Tools written in Go are served to Claude through an in-process MCP server
// (MCPServer): the CLI relays each tool call over the same pipes, so no network port is opened.
//
// Authentication is whatever the `claude` program uses: an API key in ANTHROPIC_API_KEY, a cloud
// provider, or the Claude subscription the user signed in with (`claude` then `/login`). Anthropic
// allows a subscription for a person's own, individual use of Claude Code; a product offered to
// other people must use API keys. This package never reads, stores or passes on login
// credentials. See Options.NoAPIKey to make sure a subscription login is the one used.
package claudecode
