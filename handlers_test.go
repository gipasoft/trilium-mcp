package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// --- createNote -------------------------------------------------------------

func TestCreateNote_RequiresTitle(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
	})
	res, _ := h.createNote(context.Background(), toolReq(map[string]any{}))
	if !res.IsError {
		t.Error("createNote without title should error")
	}
}

func TestCreateNote_WithLabels(t *testing.T) {
	var attrPosts int
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/etapi/create-note":
			_, _ = w.Write([]byte(`{"note":{"noteId":"N","title":"T","type":"text"},"branch":{"branchId":"P_N","noteId":"N","parentNoteId":"P"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/etapi/attributes":
			attrPosts++
			_, _ = w.Write([]byte(`{"attributeId":"A","type":"label"}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	res, _ := h.createNote(context.Background(), toolReq(map[string]any{
		"title":  "T",
		"labels": map[string]any{"a": "1", "b": "2"},
	}))
	if res.IsError {
		t.Fatalf("createNote errored: %s", resultText(t, res))
	}
	if attrPosts != 2 {
		t.Errorf("attribute POSTs = %d, want 2", attrPosts)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["note_id"] != "N" || out["branch_id"] != "P_N" {
		t.Errorf("unexpected result: %v", out)
	}
}

// --- updateNote -------------------------------------------------------------

func TestUpdateNote_TitleAndContent(t *testing.T) {
	var patched, putContent bool
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/etapi/notes/N":
			patched = true
			_, _ = w.Write([]byte(`{"noteId":"N","title":"new","type":"text"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/etapi/notes/N/content":
			putContent = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	res, _ := h.updateNote(context.Background(), toolReq(map[string]any{
		"note_id": "N", "title": "new", "content": "body",
	}))
	if res.IsError {
		t.Fatalf("updateNote errored: %s", resultText(t, res))
	}
	if !patched || !putContent {
		t.Errorf("patched=%v putContent=%v, want both true", patched, putContent)
	}
}

func TestUpdateNote_ContentOnly_FallsBackToGet(t *testing.T) {
	var patched, fetched bool
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch:
			patched = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/etapi/notes/N/content":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/etapi/notes/N":
			fetched = true
			_, _ = w.Write([]byte(`{"noteId":"N","title":"t","type":"text"}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
		}
	})
	res, _ := h.updateNote(context.Background(), toolReq(map[string]any{
		"note_id": "N", "content": "body",
	}))
	if res.IsError {
		t.Fatalf("updateNote errored: %s", resultText(t, res))
	}
	if patched {
		t.Error("no PATCH expected when only content changes")
	}
	if !fetched {
		t.Error("expected GetNote fallback when nothing was patched")
	}
}

// --- appendContent ----------------------------------------------------------

func TestAppendContent_SeparatorOnlyWhenNonEmpty(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		sep      string
		add      string
		wantBody string
	}{
		{"empty existing → no separator", "", "", "new", "new"},
		{"non-empty → default separator", "old", "", "new", "old\n\nnew"},
		{"custom separator", "old", " | ", "new", "old | new"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotBody string
			h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_, _ = w.Write([]byte(c.existing))
				case http.MethodPut:
					b, _ := io.ReadAll(r.Body)
					gotBody = string(b)
					w.WriteHeader(http.StatusNoContent)
				}
			})
			args := map[string]any{"note_id": "N", "content": c.add}
			if c.sep != "" {
				args["separator"] = c.sep
			}
			res, _ := h.appendContent(context.Background(), toolReq(args))
			if res.IsError {
				t.Fatalf("errored: %s", resultText(t, res))
			}
			if gotBody != c.wantBody {
				t.Errorf("PUT body = %q, want %q", gotBody, c.wantBody)
			}
		})
	}
}

func TestAppendContent_RequiresContent(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.appendContent(context.Background(), toolReq(map[string]any{"note_id": "N"}))
	if !res.IsError {
		t.Error("appendContent without content should error")
	}
}

// --- searchNotes ------------------------------------------------------------

func TestSearchNotes_RequiresQuery(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.searchNotes(context.Background(), toolReq(map[string]any{}))
	if !res.IsError {
		t.Error("searchNotes without query should error")
	}
}

func TestSearchNotes_OptionMappingDatesAndOrder(t *testing.T) {
	var method, path string
	var q url.Values
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, q = r.Method, r.URL.Path, r.URL.Query()
		_, _ = w.Write([]byte(`{"results":[
			{"noteId":"B","title":"newer","type":"text","dateModified":"2026-07-27 12:00:00.000+0200","utcDateModified":"2026-07-27 10:00:00.000Z"},
			{"noteId":"A","title":"older","type":"text","dateModified":"2026-07-26 12:00:00.000+0200","utcDateModified":"2026-07-26 10:00:00.000Z"}
		]}`))
	})
	res, _ := h.searchNotes(context.Background(), toolReq(map[string]any{
		"query":            `note.noteId != ""`,
		"ancestor_note_id": "ROOT",
		"fast_search":      true,
		"include_archived": true,
		"order_by":         "dateModified",
		"order_direction":  "desc",
		"limit":            float64(5),
	}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if method != http.MethodGet || path != "/etapi/notes" {
		t.Fatalf("request = %s %s, want GET /etapi/notes", method, path)
	}
	checks := map[string]string{
		"search": `note.noteId != ""`, "ancestorNoteId": "ROOT",
		"fastSearch": "true", "includeArchivedNotes": "true",
		"orderBy": "dateModified", "orderDirection": "desc", "limit": "5",
	}
	for key, want := range checks {
		if got := q.Get(key); got != want {
			t.Errorf("query[%s] = %q, want %q", key, got, want)
		}
	}
	var out struct {
		Count   int `json:"count"`
		Results []struct {
			NoteID          string `json:"note_id"`
			DateModified    string `json:"date_modified"`
			UtcDateModified string `json:"utc_date_modified"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Count != 2 || out.Results[0].NoteID != "B" || out.Results[1].NoteID != "A" {
		t.Fatalf("order changed: %+v", out.Results)
	}
	if out.Results[0].DateModified == "" || out.Results[0].UtcDateModified == "" {
		t.Fatalf("dates missing: %+v", out.Results[0])
	}
}

func TestSearchNotes_QueryOnlyCompatibilityAndMissingDates(t *testing.T) {
	var q url.Values
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = w.Write([]byte(`{"results":[{"noteId":"A","title":"a","type":"text"}]}`))
	})
	res, _ := h.searchNotes(context.Background(), toolReq(map[string]any{"query": "#x"}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if q.Get("limit") != "50" || q.Has("orderBy") || q.Has("orderDirection") {
		t.Errorf("compatibility query = %v", q)
	}
	text := resultText(t, res)
	if strings.Contains(text, "date_modified") || strings.Contains(text, "utc_date_modified") {
		t.Errorf("invented missing dates: %s", text)
	}
}

func TestSearchNotes_RejectsInvalidOrderingBeforeHTTP(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"order_by", map[string]any{"order_by": "title"}, "order_by"},
		{"order_direction", map[string]any{"order_direction": "sideways"}, "order_direction"},
		{"limit zero", map[string]any{"limit": float64(0)}, "limit"},
		{"limit high", map[string]any{"limit": float64(201)}, "limit"},
		{"limit fraction", map[string]any{"limit": 1.5}, "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) { called = true })
			args := map[string]any{"query": "#x"}
			for key, value := range tc.args {
				args[key] = value
			}
			res, _ := h.searchNotes(context.Background(), toolReq(args))
			if !res.IsError || !strings.Contains(resultText(t, res), tc.want) {
				t.Fatalf("unexpected result: %s", resultText(t, res))
			}
			if called {
				t.Fatal("invalid arguments reached ETAPI")
			}
		})
	}
}

// --- attributes -------------------------------------------------------------

func TestAddLabel_RequiredArgs(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.addLabel(context.Background(), toolReq(map[string]any{"note_id": "N"}))
	if !res.IsError {
		t.Error("addLabel without name should error")
	}
}

func TestAddLabel_PostsLabelAttribute(t *testing.T) {
	var sent Attribute
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"attributeId":"A","type":"label","name":"k","value":"v"}`))
	})
	res, _ := h.addLabel(context.Background(), toolReq(map[string]any{
		"note_id": "N", "name": "k", "value": "v", "inheritable": true,
	}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if sent.Type != "label" || sent.Name != "k" || sent.Value != "v" || !sent.IsInheritable {
		t.Errorf("posted attribute = %+v", sent)
	}
}

func TestAddRelation_PostsRelationAttribute(t *testing.T) {
	var sent Attribute
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"attributeId":"A","type":"relation"}`))
	})
	res, _ := h.addRelation(context.Background(), toolReq(map[string]any{
		"note_id": "N", "name": "author", "target_note_id": "T",
	}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if sent.Type != "relation" || sent.Value != "T" {
		t.Errorf("posted attribute = %+v, want relation→T", sent)
	}
}

func TestAddRelation_RequiresTarget(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.addRelation(context.Background(), toolReq(map[string]any{"note_id": "N", "name": "x"}))
	if !res.IsError {
		t.Error("addRelation without target should error")
	}
}

func TestRemoveAttribute(t *testing.T) {
	var deletedPath string
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		deletedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	res, _ := h.removeAttribute(context.Background(), toolReq(map[string]any{"attribute_id": "ABC"}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if deletedPath != "/etapi/attributes/ABC" {
		t.Errorf("deleted path = %q", deletedPath)
	}
}

func TestListAttributes(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"noteId":"N","attributes":[{"attributeId":"A","type":"label","name":"k","value":"v"}]}`))
	})
	res, _ := h.listAttributes(context.Background(), toolReq(map[string]any{"note_id": "N"}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), `"attributes"`) {
		t.Errorf("missing attributes in result: %s", resultText(t, res))
	}
}

// --- getNote ----------------------------------------------------------------

func TestGetNote_WithContent(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			_, _ = w.Write([]byte("BODY"))
			return
		}
		_, _ = w.Write([]byte(`{"noteId":"N","title":"t","type":"text"}`))
	})
	res, _ := h.getNote(context.Background(), toolReq(map[string]any{"note_id": "N", "include_content": true}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), `"content": "BODY"`) {
		t.Errorf("content not included: %s", resultText(t, res))
	}
}

// --- cloneNote / deleteBranch ----------------------------------------------

func TestCloneNote(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/etapi/branches" {
			_, _ = w.Write([]byte(`{"branchId":"P_N","noteId":"N","parentNoteId":"P"}`))
			return
		}
		t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
	})
	res, _ := h.cloneNote(context.Background(), toolReq(map[string]any{"note_id": "N", "new_parent_id": "P"}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "P_N") {
		t.Errorf("branch id missing: %s", resultText(t, res))
	}
}

func TestDeleteBranch_RequiresID(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.deleteBranch(context.Background(), toolReq(map[string]any{}))
	if !res.IsError {
		t.Error("deleteBranch without branch_id should error")
	}
}

// --- batchCreateNotes -------------------------------------------------------

func TestBatchCreateNotes_PartialFailures(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/etapi/create-note":
			_, _ = w.Write([]byte(`{"note":{"noteId":"N","title":"A","type":"text"},"branch":{"branchId":"P_N","noteId":"N","parentNoteId":"P"}}`))
		case "/etapi/attributes":
			_, _ = w.Write([]byte(`{"attributeId":"A","type":"label"}`))
		default:
			t.Errorf("unexpected call: %s", r.URL.Path)
		}
	})
	res, _ := h.batchCreateNotes(context.Background(), toolReq(map[string]any{
		"notes": []any{
			map[string]any{"title": "A", "labels": map[string]any{"k": "v"}},
			map[string]any{"content": "no title"}, // missing title
			"not-an-object",                       // wrong type
		},
	}))
	if res.IsError {
		t.Fatalf("batch should report per-item errors, not fail wholesale: %s", resultText(t, res))
	}
	var out struct {
		Created int `json:"created"`
		Failed  int `json:"failed"`
		Results []struct {
			Index int    `json:"index"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Created != 1 || out.Failed != 2 {
		t.Errorf("created=%d failed=%d, want 1/2", out.Created, out.Failed)
	}
}

func TestBatchCreateNotes_RequiresArray(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.batchCreateNotes(context.Background(), toolReq(map[string]any{"notes": "nope"}))
	if !res.IsError {
		t.Error("batchCreateNotes with non-array should error")
	}
}

// --- error paths ------------------------------------------------------------

// TestHandlers_PropagateClientErrors drives each handler against a server that
// always returns HTTP 500, exercising the `if err != nil` branches that the
// happy-path tests don't reach.
func TestHandlers_PropagateClientErrors(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	ctx := context.Background()
	cases := []struct {
		name string
		call func() (*mcp.CallToolResult, error)
	}{
		{"createNote", func() (*mcp.CallToolResult, error) {
			return h.createNote(ctx, toolReq(map[string]any{"title": "T"}))
		}},
		{"getNote", func() (*mcp.CallToolResult, error) {
			return h.getNote(ctx, toolReq(map[string]any{"note_id": "N"}))
		}},
		{"getNote_content", func() (*mcp.CallToolResult, error) {
			return h.getNote(ctx, toolReq(map[string]any{"note_id": "N", "include_content": true}))
		}},
		{"updateNote", func() (*mcp.CallToolResult, error) {
			return h.updateNote(ctx, toolReq(map[string]any{"note_id": "N", "title": "x"}))
		}},
		{"appendContent", func() (*mcp.CallToolResult, error) {
			return h.appendContent(ctx, toolReq(map[string]any{"note_id": "N", "content": "x"}))
		}},
		{"deleteNote", func() (*mcp.CallToolResult, error) {
			return h.deleteNote(ctx, toolReq(map[string]any{"note_id": "N"}))
		}},
		{"searchNotes", func() (*mcp.CallToolResult, error) {
			return h.searchNotes(ctx, toolReq(map[string]any{"query": "#x"}))
		}},
		{"addLabel", func() (*mcp.CallToolResult, error) {
			return h.addLabel(ctx, toolReq(map[string]any{"note_id": "N", "name": "k"}))
		}},
		{"addRelation", func() (*mcp.CallToolResult, error) {
			return h.addRelation(ctx, toolReq(map[string]any{"note_id": "N", "name": "k", "target_note_id": "T"}))
		}},
		{"removeAttribute", func() (*mcp.CallToolResult, error) {
			return h.removeAttribute(ctx, toolReq(map[string]any{"attribute_id": "A"}))
		}},
		{"listAttributes", func() (*mcp.CallToolResult, error) {
			return h.listAttributes(ctx, toolReq(map[string]any{"note_id": "N"}))
		}},
		{"moveNote", func() (*mcp.CallToolResult, error) {
			return h.moveNote(ctx, toolReq(map[string]any{"note_id": "N", "new_parent_id": "P", "from_parent_id": "O"}))
		}},
		{"cloneNote", func() (*mcp.CallToolResult, error) {
			return h.cloneNote(ctx, toolReq(map[string]any{"note_id": "N", "new_parent_id": "P"}))
		}},
		{"deleteBranch", func() (*mcp.CallToolResult, error) {
			return h.deleteBranch(ctx, toolReq(map[string]any{"branch_id": "P_N"}))
		}},
		{"getNoteSubtree", func() (*mcp.CallToolResult, error) {
			return h.getNoteSubtree(ctx, toolReq(map[string]any{"note_id": "N"}))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := c.call()
			if err != nil {
				t.Fatalf("handler returned exec error, want tool error result: %v", err)
			}
			if !res.IsError {
				t.Errorf("%s should surface client error as IsError result", c.name)
			}
		})
	}
}

// --- batchDeleteNotes -------------------------------------------------------

func TestBatchDeleteNotes_MixedInput(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	res, _ := h.batchDeleteNotes(context.Background(), toolReq(map[string]any{
		"note_ids": []any{"A", "", float64(3), "B"},
	}))
	if res.IsError {
		t.Fatalf("errored: %s", resultText(t, res))
	}
	var out struct {
		Deleted []string            `json:"deleted"`
		Failed  []map[string]string `json:"failed"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Deleted) != 2 || len(out.Failed) != 2 {
		t.Errorf("deleted=%v failed=%v, want 2 deleted / 2 failed", out.Deleted, out.Failed)
	}
}

func TestBatchDeleteNotes_RequiresArray(t *testing.T) {
	h := newHandlers(t, func(w http.ResponseWriter, r *http.Request) {})
	res, _ := h.batchDeleteNotes(context.Background(), toolReq(map[string]any{}))
	if !res.IsError {
		t.Error("batchDeleteNotes without note_ids should error")
	}
}
