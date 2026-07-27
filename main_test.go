package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// toolReq builds a CallToolRequest carrying the given arguments, mirroring how
// the MCP server hands arguments to a handler.
func toolReq(args map[string]any) mcp.CallToolRequest {
	var r mcp.CallToolRequest
	r.Params.Arguments = args
	return r
}

// resultText extracts the first text block from a tool result.
func resultText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if r == nil {
		t.Fatal("nil result")
	}
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("result has no text content: %+v", r)
	return ""
}

// newHandlers wires a handlers value to a test HTTP server standing in for Trilium.
func newHandlers(t *testing.T, handler http.HandlerFunc) *handlers {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return &handlers{c: NewClient(s.URL, "tok", 2*time.Second), lvl: logOff}
}

func TestArgString(t *testing.T) {
	req := toolReq(map[string]any{"a": "hello", "n": float64(3)})
	if got := argString(req, "a"); got != "hello" {
		t.Errorf("argString(a) = %q, want hello", got)
	}
	if got := argString(req, "missing"); got != "" {
		t.Errorf("argString(missing) = %q, want empty", got)
	}
	if got := argString(req, "n"); got != "" {
		t.Errorf("argString(non-string) = %q, want empty", got)
	}
}

func TestArgStringPresent(t *testing.T) {
	req := toolReq(map[string]any{"a": "", "n": 5})
	if s, ok := argStringPresent(req, "a"); !ok || s != "" {
		t.Errorf("argStringPresent(a) = (%q,%v), want (\"\",true)", s, ok)
	}
	if _, ok := argStringPresent(req, "missing"); ok {
		t.Error("argStringPresent(missing) ok=true, want false")
	}
	if _, ok := argStringPresent(req, "n"); ok {
		t.Error("argStringPresent(non-string) ok=true, want false")
	}
}

func TestArgBool(t *testing.T) {
	req := toolReq(map[string]any{"yes": true, "no": false, "s": "true"})
	if !argBool(req, "yes") {
		t.Error("argBool(yes) = false, want true")
	}
	if argBool(req, "no") {
		t.Error("argBool(no) = true, want false")
	}
	if argBool(req, "missing") {
		t.Error("argBool(missing) = true, want false")
	}
	if argBool(req, "s") {
		t.Error("argBool(non-bool) = true, want false")
	}
}

func TestArgInt(t *testing.T) {
	// JSON numbers decode to float64; native ints should also work.
	req := toolReq(map[string]any{"f": float64(7), "i": 9, "s": "10"})
	if got := argInt(req, "f", -1); got != 7 {
		t.Errorf("argInt(float64) = %d, want 7", got)
	}
	if got := argInt(req, "i", -1); got != 9 {
		t.Errorf("argInt(int) = %d, want 9", got)
	}
	if got := argInt(req, "missing", 42); got != 42 {
		t.Errorf("argInt(missing) = %d, want default 42", got)
	}
	if got := argInt(req, "s", 42); got != 42 {
		t.Errorf("argInt(non-number) = %d, want default 42", got)
	}
}

func TestArgOptionalEnum(t *testing.T) {
	allowed := []string{"asc", "desc"}
	if got, err := argOptionalEnum(toolReq(nil), "direction", allowed...); err != nil || got != "" {
		t.Fatalf("missing arg = %q, %v", got, err)
	}
	if got, err := argOptionalEnum(toolReq(map[string]any{"direction": "desc"}), "direction", allowed...); err != nil || got != "desc" {
		t.Fatalf("valid arg = %q, %v", got, err)
	}
	for _, value := range []any{"sideways", float64(1), ""} {
		if _, err := argOptionalEnum(toolReq(map[string]any{"direction": value}), "direction", allowed...); err == nil {
			t.Errorf("argOptionalEnum(%#v) accepted invalid value", value)
		}
	}
}

func TestArgBoundedInt(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		want    int
		wantErr bool
	}{
		{name: "missing uses default", want: 50},
		{name: "float JSON integer", value: float64(5), want: 5},
		{name: "native integer", value: 200, want: 200},
		{name: "fraction", value: 1.5, wantErr: true},
		{name: "zero", value: float64(0), wantErr: true},
		{name: "over max", value: float64(201), wantErr: true},
		{name: "wrong type", value: "5", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{}
			if tc.value != nil {
				args["limit"] = tc.value
			}
			got, err := argBoundedInt(toolReq(args), "limit", 50, 1, 200)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
				t.Fatalf("got (%d, %v), want (%d, err=%v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestArgStringMap(t *testing.T) {
	req := toolReq(map[string]any{
		"labels": map[string]any{
			"str":    "x",
			"null":   nil,
			"bool":   true,
			"num":    float64(3.5),
			"nested": map[string]any{"k": "v"},
		},
	})
	got := argStringMap(req, "labels")
	want := map[string]string{
		"str":    "x",
		"null":   "",
		"bool":   "true",
		"num":    "3.5",
		"nested": `{"k":"v"}`,
	}
	if len(got) != len(want) {
		t.Fatalf("argStringMap len = %d (%v), want %d", len(got), got, len(want))
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("argStringMap[%s] = %q, want %q", k, got[k], w)
		}
	}
	if argStringMap(req, "missing") != nil {
		t.Error("argStringMap(missing) != nil")
	}
}

func TestCoerceAndStrFrom(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"x", "x"},
		{nil, ""},
		{true, "true"},
		{float64(3), "3"},
		{float64(3.5), "3.5"},
		{map[string]any{"k": "v"}, `{"k":"v"}`},
	}
	for _, c := range cases {
		if got := coerceString(c.in); got != c.want {
			t.Errorf("coerceString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := strFrom("hi"); got != "hi" {
		t.Errorf("strFrom(string) = %q, want hi", got)
	}
	if got := strFrom(42); got != "" {
		t.Errorf("strFrom(non-string) = %q, want empty", got)
	}
}

func TestOkJSON_NoHTMLEscape(t *testing.T) {
	res, err := okJSON(map[string]any{"html": "<p>a & b</p>"})
	if err != nil {
		t.Fatalf("okJSON: %v", err)
	}
	if res.IsError {
		t.Error("okJSON result IsError = true, want false")
	}
	text := resultText(t, res)
	if !strings.Contains(text, "<p>a & b</p>") {
		t.Errorf("okJSON escaped HTML: %q", text)
	}
}

func TestErrResult(t *testing.T) {
	res, err := errResult("boom %d", 7)
	if err != nil {
		t.Fatalf("errResult returned err: %v", err)
	}
	if !res.IsError {
		t.Error("errResult IsError = false, want true")
	}
	if got := resultText(t, res); got != "boom 7" {
		t.Errorf("errResult text = %q, want 'boom 7'", got)
	}
}

func TestDeleteNote(t *testing.T) {
	var deletedPath string
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		deletedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	res, _ := h.deleteNote(context.Background(), toolReq(map[string]any{"note_id": "N"}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if deletedPath != "/etapi/notes/N" {
		t.Errorf("deleted path = %q, want /etapi/notes/N", deletedPath)
	}
	res2, _ := h.deleteNote(context.Background(), toolReq(map[string]any{}))
	if !res2.IsError {
		t.Error("deleteNote without note_id should error")
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := map[string]logLevel{
		"off": logOff, "0": logOff, "FALSE": logOff, "no": logOff,
		"debug": logDebug, "verbose": logDebug, "2": logDebug,
		"info": logInfo, "": logInfo, "garbage": logInfo,
	}
	for in, want := range cases {
		if got := parseLogLevel(in); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLogLevelName(t *testing.T) {
	for lvl, want := range map[logLevel]string{logOff: "off", logInfo: "info", logDebug: "debug"} {
		if got := logLevelName(lvl); got != want {
			t.Errorf("logLevelName(%v) = %q, want %q", lvl, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate(short) = %q, want hello", got)
	}
	if got := truncate("hello world", 5); got != "hello…" {
		t.Errorf("truncate(long) = %q, want 'hello…'", got)
	}
}

func TestSummarizeResult(t *testing.T) {
	if got := summarizeResult(nil, 10); got != "" {
		t.Errorf("summarizeResult(nil) = %q, want empty", got)
	}
	res := mcp.NewToolResultText("line one\nline two")
	if got := summarizeResult(res, 100); got != "line one line two" {
		t.Errorf("summarizeResult = %q, want newlines flattened", got)
	}
}

func TestBranchID(t *testing.T) {
	if got := BranchID("parent", "note"); got != "parent_note" {
		t.Errorf("BranchID = %q, want parent_note", got)
	}
}

// --- moveNote ---------------------------------------------------------------

func TestMoveNote_MissingArgs(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP call: %s %s", r.Method, r.URL.Path)
	})
	res, _ := h.moveNote(context.Background(), toolReq(map[string]any{"note_id": "N"}))
	if !res.IsError {
		t.Error("moveNote without new_parent_id should error")
	}
}

func TestMoveNote_SameParent(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP call: %s %s", r.Method, r.URL.Path)
	})
	res, _ := h.moveNote(context.Background(), toolReq(map[string]any{
		"note_id": "N", "new_parent_id": "P", "from_parent_id": "P",
	}))
	if !res.IsError {
		t.Error("moveNote with identical parents should error")
	}
	if !strings.Contains(resultText(t, res), "same") {
		t.Errorf("unexpected message: %q", resultText(t, res))
	}
}

func TestMoveNote_ExplicitParent_CreatesThenDeletes(t *testing.T) {
	var created, deleted bool
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/etapi/branches":
			created = true
			_, _ = w.Write([]byte(`{"branchId":"NEW_N","noteId":"N","parentNoteId":"NEW"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/etapi/branches/OLD_N":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	res, err := h.moveNote(context.Background(), toolReq(map[string]any{
		"note_id": "N", "new_parent_id": "NEW", "from_parent_id": "OLD",
	}))
	if err != nil {
		t.Fatalf("moveNote: %v", err)
	}
	if res.IsError {
		t.Fatalf("moveNote errored: %s", resultText(t, res))
	}
	if !created || !deleted {
		t.Errorf("created=%v deleted=%v, want both true", created, deleted)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	for k, want := range map[string]string{
		"note_id": "N", "new_parent_id": "NEW", "old_parent_id": "OLD",
		"new_branch_id": "NEW_N", "removed_branch": "OLD_N",
	} {
		if out[k] != want {
			t.Errorf("result[%s] = %v, want %s", k, out[k], want)
		}
	}
}

func TestMoveNote_AutoParent_Single(t *testing.T) {
	var created, deleted bool
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/etapi/notes/N":
			_, _ = w.Write([]byte(`{"noteId":"N","title":"t","type":"text","parentNoteIds":["OLD"]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/etapi/branches":
			created = true
			_, _ = w.Write([]byte(`{"branchId":"NEW_N","noteId":"N","parentNoteId":"NEW"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/etapi/branches/OLD_N":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	res, _ := h.moveNote(context.Background(), toolReq(map[string]any{
		"note_id": "N", "new_parent_id": "NEW",
	}))
	if res.IsError {
		t.Fatalf("moveNote errored: %s", resultText(t, res))
	}
	if !created || !deleted {
		t.Errorf("created=%v deleted=%v, want both true", created, deleted)
	}
}

func TestMoveNote_AutoParent_Ambiguous(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/etapi/notes/N" {
			_, _ = w.Write([]byte(`{"noteId":"N","parentNoteIds":["A","B"]}`))
			return
		}
		t.Errorf("unexpected call after ambiguity: %s %s", r.Method, r.URL.Path)
	})
	res, _ := h.moveNote(context.Background(), toolReq(map[string]any{
		"note_id": "N", "new_parent_id": "NEW",
	}))
	if !res.IsError {
		t.Error("moveNote with 2 parents and no from_parent_id should error")
	}
	if !strings.Contains(resultText(t, res), "2 parents") {
		t.Errorf("unexpected message: %q", resultText(t, res))
	}
}

// --- getNoteSubtree ---------------------------------------------------------

// subtreeServer serves a fixed 4-note tree:
//
//	root → c1 → g1
//	root → c2
func subtreeServer(t *testing.T, contentByID map[string]string) http.HandlerFunc {
	notes := map[string]string{
		"root": `{"noteId":"root","title":"root","type":"text","childNoteIds":["c1","c2"]}`,
		"c1":   `{"noteId":"c1","title":"c1","type":"text","childNoteIds":["g1"]}`,
		"c2":   `{"noteId":"c2","title":"c2","type":"text"}`,
		"g1":   `{"noteId":"g1","title":"g1","type":"text"}`,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/etapi/notes/")
		if strings.HasSuffix(path, "/content") {
			id := strings.TrimSuffix(path, "/content")
			_, _ = w.Write([]byte(contentByID[id]))
			return
		}
		body, ok := notes[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

type subtreeNode struct {
	NoteID    string         `json:"note_id"`
	Content   string         `json:"content"`
	Children  []*subtreeNode `json:"children"`
	Truncated bool           `json:"truncated_at_depth"`
}

type subtreeResult struct {
	Root         *subtreeNode `json:"root"`
	NotesVisited int          `json:"notes_visited"`
	MaxDepth     int          `json:"max_depth"`
	Limit        int          `json:"limit"`
}

func TestGetNoteSubtree_MissingID(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.getNoteSubtree(context.Background(), toolReq(map[string]any{}))
	if !res.IsError {
		t.Error("getNoteSubtree without note_id should error")
	}
}

func TestGetNoteSubtree_FullDepth(t *testing.T) {
	h := newHandlers(t, subtreeServer(t, nil))
	res, _ := h.getNoteSubtree(context.Background(), toolReq(map[string]any{
		"note_id": "root", "max_depth": float64(2),
	}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	var out subtreeResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.NotesVisited != 4 {
		t.Errorf("notes_visited = %d, want 4", out.NotesVisited)
	}
	if out.Root.Truncated {
		t.Error("root marked truncated at full depth")
	}
}

func TestGetNoteSubtree_DepthTruncation(t *testing.T) {
	h := newHandlers(t, subtreeServer(t, nil))
	res, _ := h.getNoteSubtree(context.Background(), toolReq(map[string]any{
		"note_id": "root", "max_depth": float64(1),
	}))
	var out subtreeResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.NotesVisited != 3 {
		t.Errorf("notes_visited = %d, want 3 (root + 2 children)", out.NotesVisited)
	}
	// c1 has a child (g1) but we stopped at depth 1, so it must be flagged.
	var c1 *subtreeNode
	for _, c := range out.Root.Children {
		if c.NoteID == "c1" {
			c1 = c
		}
	}
	if c1 == nil || !c1.Truncated {
		t.Errorf("c1 should be truncated at depth limit, got %+v", c1)
	}
}

func TestGetNoteSubtree_LimitCap(t *testing.T) {
	h := newHandlers(t, subtreeServer(t, nil))
	res, _ := h.getNoteSubtree(context.Background(), toolReq(map[string]any{
		"note_id": "root", "max_depth": float64(5), "limit": float64(2),
	}))
	var out subtreeResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.NotesVisited != 2 {
		t.Errorf("notes_visited = %d, want 2 (capped by limit)", out.NotesVisited)
	}
}

func TestGetNoteSubtree_IncludeContent(t *testing.T) {
	h := newHandlers(t, subtreeServer(t, map[string]string{
		"root": "ROOT-BODY", "c1": "C1-BODY", "c2": "C2-BODY", "g1": "G1-BODY",
	}))
	res, _ := h.getNoteSubtree(context.Background(), toolReq(map[string]any{
		"note_id": "root", "include_content": true,
	}))
	var out subtreeResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Root.Content != "ROOT-BODY" {
		t.Errorf("root content = %q, want ROOT-BODY", out.Root.Content)
	}
}
