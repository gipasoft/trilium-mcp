package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// captureLog redirects the standard logger to a buffer for the duration of fn.
func captureLog(fn func()) string {
	var buf bytes.Buffer
	old := log.Writer()
	flags := log.Flags()
	prefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(old)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	}()
	fn()
	return buf.String()
}

func TestRegister_AddsAllTools(t *testing.T) {
	h := &handlers{c: NewClient("http://localhost", "tok", 0), lvl: logOff}
	s := server.NewMCPServer(serverName, serverVersion, server.WithToolCapabilities(false))
	h.register(s)

	resp := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	jr, ok := resp.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("response type = %T, want JSONRPCResponse", resp)
	}
	tools := jr.Result.(mcp.ListToolsResult).Tools

	want := []string{
		"create_note", "get_note", "update_note", "append_content", "delete_note",
		"search_notes", "add_label", "add_relation", "remove_attribute", "list_attributes",
		"move_note", "clone_note", "delete_branch", "batch_create_notes",
		"batch_delete_notes", "get_note_subtree",
	}
	got := make(map[string]mcp.Tool, len(tools))
	for _, tl := range tools {
		got[tl.Name] = tl
	}
	if len(tools) != len(want) {
		t.Errorf("registered %d tools, want %d", len(tools), len(want))
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
	// Spot-check that annotations (and thus boolPtr) were applied.
	if a := got["get_note"].Annotations; a.ReadOnlyHint == nil || !*a.ReadOnlyHint {
		t.Error("get_note should carry a read-only annotation")
	}
	if a := got["delete_note"].Annotations; a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Error("delete_note should carry a destructive annotation")
	}
}

func TestWithLogging_Off_NoWrap(t *testing.T) {
	h := &handlers{lvl: logOff}
	calls := 0
	inner := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls++
		return mcp.NewToolResultText("ok"), nil
	}
	wrapped := h.withLogging("t", inner)
	out := captureLog(func() {
		_, _ = wrapped(context.Background(), toolReq(nil))
	})
	if calls != 1 {
		t.Errorf("inner called %d times, want 1", calls)
	}
	if out != "" {
		t.Errorf("logOff should not log, got %q", out)
	}
}

func TestWithLogging_Info_LogsOkAndPassesThrough(t *testing.T) {
	h := &handlers{lvl: logInfo}
	want := mcp.NewToolResultText("payload")
	wrapped := h.withLogging("mytool", func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return want, nil
	})
	var got *mcp.CallToolResult
	out := captureLog(func() {
		got, _ = wrapped(context.Background(), toolReq(nil))
	})
	if got != want {
		t.Error("withLogging did not pass the result through unchanged")
	}
	if !strings.Contains(out, "→ mytool") || !strings.Contains(out, "← mytool ok") {
		t.Errorf("info log missing call markers: %q", out)
	}
}

func TestWithLogging_Debug_LogsArgsAndResult(t *testing.T) {
	h := &handlers{lvl: logDebug}
	wrapped := h.withLogging("dbg", func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("body"), nil
	})
	out := captureLog(func() {
		_, _ = wrapped(context.Background(), toolReq(map[string]any{"k": "v"}))
	})
	if !strings.Contains(out, `args=`) || !strings.Contains(out, `"k":"v"`) {
		t.Errorf("debug log missing args: %q", out)
	}
	if !strings.Contains(out, "body") {
		t.Errorf("debug log missing result preview: %q", out)
	}
}

func TestWithLogging_ToolErrorBranch(t *testing.T) {
	h := &handlers{lvl: logInfo}
	wrapped := h.withLogging("te", func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultError("boom"), nil
	})
	out := captureLog(func() {
		_, _ = wrapped(context.Background(), toolReq(nil))
	})
	if !strings.Contains(out, "tool-error") || !strings.Contains(out, "boom") {
		t.Errorf("expected tool-error log, got %q", out)
	}
}

func TestWithLogging_ExecErrorBranch(t *testing.T) {
	h := &handlers{lvl: logInfo}
	wrapped := h.withLogging("ee", func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("kaboom")
	})
	out := captureLog(func() {
		_, _ = wrapped(context.Background(), toolReq(nil))
	})
	if !strings.Contains(out, "exec-error") || !strings.Contains(out, "kaboom") {
		t.Errorf("expected exec-error log, got %q", out)
	}
}

func TestHTMLUnescapingWriter(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()

	w := &htmlUnescapingWriter{w: f}
	in := []byte(`{"html":"<p>a & b</p>"}`)
	n, err := w.Write(in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Write must report the input length so callers see a complete write,
	// even though fewer bytes hit disk after unescaping.
	if n != len(in) {
		t.Errorf("Write returned %d, want %d", n, len(in))
	}
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != `{"html":"<p>a & b</p>"}` {
		t.Errorf("written bytes = %q, want unescaped HTML", got)
	}
}
