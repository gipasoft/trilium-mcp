# `search_notes` Ordering Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `search_notes` forward validated modification-date ordering to Trilium ETAPI, preserve the returned order, expose exact modification dates, and publish a verified Linux AMD64 artifact from GitHub Actions.

**Architecture:** Keep ETAPI as the sole authority for filtering, ordering, and limiting. Extend only the MCP schema and handler around the existing `SearchOpts`/`Client.SearchNotes` path, add deterministic handler-level validation, and retain the existing slim result projection with two additional optional date fields. Harden endpoint logging separately so validation and transport failures cannot disclose the configured Trilium URL.

**Tech Stack:** Go 1.23, `github.com/mark3labs/mcp-go` v0.32.0, Go `net/http`/`httptest`, GitHub Actions, static Linux AMD64 builds with `CGO_ENABLED=0`.

## Global Constraints

- Work from current upstream `master` commit `a3a3289ca4b6d5b6765f3ee288d5f79663d6872b` on branch `fix/search-notes-ordering`.
- The intended fork is `https://github.com/gipasoft/trilium-mcp`; keep `https://github.com/OVDEN13/trilium-mcp.git` as remote `upstream`.
- Do not modify the QNAP during this plan.
- `query` remains required and non-empty; document `note.noteId != ""` as the match-all query.
- `order_by` accepts only `dateModified` or `utcDateModified`.
- `order_direction` accepts only `asc` or `desc`.
- `limit` defaults to `50` and must be an integer from `1` through `200`.
- Preserve ETAPI result order; never sort locally.
- Return `date_modified` and `utc_date_modified` only when ETAPI supplies them.
- Do not add dependencies or refactor unrelated tools.
- Search remains read-only and may call only `GET /etapi/notes`.
- Tokens, authorization headers, and configured private endpoint URLs must not appear in logs or tool errors.
- Set the server version to `0.1.6`.
- GitHub Actions must pass vet, race-enabled tests, build, and MCP smoke test before uploading the Linux AMD64 binary and SHA-256.
- Before Task 1, provide an official Go 1.23 toolchain outside the repository
  (the current Windows host does not expose `go`). Prefer a temporary official
  toolchain or an official `golang:1.23` container; do not commit toolchain files
  or change the module's declared Go version.

---

## File Map

- `main.go`: MCP schema, argument validation helpers, `searchNotes` projection, server version, and safe startup log.
- `handlers_test.go`: handler-level forwarding, order preservation, dates, validation, compatibility, and read-only HTTP assertions.
- `main_test.go`: focused tests for new reusable argument-validation helpers.
- `wiring_test.go`: schema contract, version contract, and startup-log secrecy.
- `trilium.go`: existing ETAPI query mapping plus sanitized transport errors and failover logging.
- `trilium_test.go`: transport-error and failover-log secrecy regressions.
- `README.md`: public `search_notes` argument contract and match-all ordering example.
- `.github/workflows/ci.yml`: feature-branch CI trigger and downloadable Linux AMD64 artifact.
- `docs/superpowers/specs/2026-07-27-search-notes-ordering-design.md`: approved design; do not alter unless implementation reveals a genuine contradiction.

---

### Task 1: Publish the MCP schema contract

**Files:**
- Modify: `wiring_test.go`
- Modify: `main.go:242-250`

**Interfaces:**
- Consumes: `handlers.register(*server.MCPServer)` and `mcp.Tool.InputSchema`.
- Produces: optional MCP properties `order_by`, `order_direction`, and constrained `limit`; existing handler behavior remains unchanged until Task 2.

- [ ] **Step 1: Add a schema assertion helper and failing contract test**

Append the following helper and test to `wiring_test.go`:

```go
func schemaProperty(t *testing.T, tool mcp.Tool, name string) map[string]any {
	t.Helper()
	value, ok := tool.InputSchema.Properties[name]
	if !ok {
		t.Fatalf("%s schema missing property %q", tool.Name, name)
	}
	property, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s property %q has type %T", tool.Name, name, value)
	}
	return property
}

func TestRegister_SearchNotesOrderingSchema(t *testing.T) {
	h := &handlers{c: NewClient("http://localhost", "tok", 0), lvl: logOff}
	s := server.NewMCPServer(serverName, serverVersion, server.WithToolCapabilities(false))
	h.register(s)

	resp := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	jr := resp.(mcp.JSONRPCResponse)
	tools := jr.Result.(mcp.ListToolsResult).Tools

	var search mcp.Tool
	for _, tool := range tools {
		if tool.Name == "search_notes" {
			search = tool
			break
		}
	}
	if search.Name == "" {
		t.Fatal("search_notes not registered")
	}

	orderBy := schemaProperty(t, search, "order_by")
	if got := orderBy["enum"]; !reflect.DeepEqual(got, []string{"dateModified", "utcDateModified"}) {
		t.Errorf("order_by enum = %#v", got)
	}
	direction := schemaProperty(t, search, "order_direction")
	if got := direction["enum"]; !reflect.DeepEqual(got, []string{"asc", "desc"}) {
		t.Errorf("order_direction enum = %#v", got)
	}
	limit := schemaProperty(t, search, "limit")
	if limit["minimum"] != float64(1) || limit["maximum"] != float64(200) ||
		limit["multipleOf"] != float64(1) || limit["default"] != float64(50) {
		t.Errorf("limit schema = %#v", limit)
	}
	if !strings.Contains(search.Description, `note.noteId != ""`) {
		t.Errorf("search_notes description lacks match-all query: %q", search.Description)
	}
}
```

Add `reflect` to the `wiring_test.go` imports.

- [ ] **Step 2: Verify the execution toolchain**

Run:

```powershell
go version
```

Expected: an official `go1.23.x` toolchain is available. If it is not, provision
one outside the repository before continuing.

- [ ] **Step 3: Run the schema test and confirm the red state**

Run:

```powershell
go test ./... -run TestRegister_SearchNotesOrderingSchema -count=1
```

Expected: FAIL because `order_by` and `order_direction` do not exist and the description lacks the match-all query.

- [ ] **Step 4: Extend the registered tool schema minimally**

Replace the current `search_notes` registration in `main.go` with:

```go
s.AddTool(mcp.NewTool("search_notes",
	mcp.WithDescription("Search notes using Trilium search syntax (e.g. '#tag', '#status=active', '\"foo bar\"', 'note.title %= \"^Re\"'). Use 'note.noteId != \"\"' to match all notes. ETAPI applies ordering and returns up to 'limit' results."),
	mcp.WithToolAnnotation(readOnly),
	mcp.WithString("query", mcp.Required(), mcp.Description("Non-empty Trilium search expression; use 'note.noteId != \"\"' to match all notes")),
	mcp.WithString("ancestor_note_id", mcp.Description("Scope the search to one subtree — only notes that are descendants of this note are returned. This is the correct way to limit search to a 'folder' like '🔧 Runbooks'.")),
	mcp.WithBoolean("fast_search", mcp.Description("Skip full-text body scan, search metadata only (default false)")),
	mcp.WithBoolean("include_archived", mcp.Description("Include archived notes (default false)")),
	mcp.WithString("order_by",
		mcp.Description("Order results by modification date"),
		mcp.Enum("dateModified", "utcDateModified")),
	mcp.WithString("order_direction",
		mcp.Description("Order direction"),
		mcp.Enum("asc", "desc")),
	mcp.WithNumber("limit",
		mcp.Description("Max results (default 50; integer from 1 to 200)"),
		mcp.Min(1), mcp.Max(200), mcp.MultipleOf(1), mcp.DefaultNumber(50)),
), h.withLogging("search_notes", h.searchNotes))
```

`mcp-go` v0.32.0 has no `WithInteger`; `type: number` plus `multipleOf: 1`,
`minimum`, and `maximum` expresses the integer contract in its DSL. Task 2
adds mandatory runtime validation as defense in depth.

- [ ] **Step 5: Format and rerun the focused test**

Run:

```powershell
gofmt -w main.go wiring_test.go
go test ./... -run TestRegister_SearchNotesOrderingSchema -count=1
```

Expected: PASS.

- [ ] **Step 6: Run existing registration regressions**

Run:

```powershell
go test ./... -run 'TestRegister_(AddsAllTools|SearchNotesOrderingSchema)' -count=1
```

Expected: both tests PASS and the tool count remains 16.

- [ ] **Step 7: Commit the schema contract**

```powershell
git add main.go wiring_test.go
git commit -m "feat: expose Trilium search ordering schema"
```

---

### Task 2: Validate, forward, and return ordered dated results

**Files:**
- Modify: `main_test.go`
- Modify: `handlers_test.go`
- Modify: `main.go:409-446`
- Modify: `main.go:548-575`

**Interfaces:**
- Consumes: MCP arguments `order_by`, `order_direction`, `limit`; existing `SearchOpts` fields `OrderBy`, `OrderDirection`, `Limit`.
- Produces:
  - `argOptionalEnum(req mcp.CallToolRequest, name string, allowed ...string) (string, error)`
  - `argBoundedInt(req mcp.CallToolRequest, name string, defaultValue, min, max int) (int, error)`
  - result fields `date_modified` and `utc_date_modified`.

- [ ] **Step 1: Add failing unit tests for validation helpers**

Add to `main_test.go`:

```go
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
```

- [ ] **Step 2: Add failing handler tests for forwarding, order, and dates**

Replace `TestSearchNotes_OptionMappingAndSlimResult` in `handlers_test.go` with:

```go
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
```

- [ ] **Step 3: Add failing compatibility, omission, and rejection tests**

Add to `handlers_test.go`:

```go
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
```

- [ ] **Step 4: Run focused tests and confirm the red state**

Run:

```powershell
go test ./... -run 'TestArg(OptionalEnum|BoundedInt)|TestSearchNotes_' -count=1
```

Expected: FAIL at compile time because the helpers do not exist. After adding
only the helper signatures, the handler assertions must remain red until the
forwarding and projection implementation is added.

- [ ] **Step 5: Implement the two validation helpers**

Add `math` to the `main.go` imports and place these helpers after `argInt`:

```go
func argOptionalEnum(req mcp.CallToolRequest, name string, allowed ...string) (string, error) {
	value, present := req.GetArguments()[name]
	if !present {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("'%s' must be one of: %s", name, strings.Join(allowed, ", "))
	}
	for _, candidate := range allowed {
		if text == candidate {
			return text, nil
		}
	}
	return "", fmt.Errorf("'%s' must be one of: %s", name, strings.Join(allowed, ", "))
}

func argBoundedInt(
	req mcp.CallToolRequest,
	name string,
	defaultValue, min, max int,
) (int, error) {
	value, present := req.GetArguments()[name]
	if !present {
		return defaultValue, nil
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case int:
		number = float64(typed)
	default:
		return 0, fmt.Errorf("'%s' must be an integer from %d to %d", name, min, max)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number ||
		number < float64(min) || number > float64(max) {
		return 0, fmt.Errorf("'%s' must be an integer from %d to %d", name, min, max)
	}
	return int(number), nil
}
```

The errors list only constant field names and allowed values; they never echo
the rejected value.

- [ ] **Step 6: Implement handler forwarding and dated projection**

Replace `searchNotes` with:

```go
func (h *handlers) searchNotes(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	q := argString(req, "query")
	if q == "" {
		return errResult("'query' is required")
	}
	orderBy, err := argOptionalEnum(req, "order_by", "dateModified", "utcDateModified")
	if err != nil {
		return errResult("%v", err)
	}
	orderDirection, err := argOptionalEnum(req, "order_direction", "asc", "desc")
	if err != nil {
		return errResult("%v", err)
	}
	limit, err := argBoundedInt(req, "limit", 50, 1, 200)
	if err != nil {
		return errResult("%v", err)
	}
	opts := SearchOpts{
		Query:           q,
		AncestorNoteID:  argString(req, "ancestor_note_id"),
		FastSearch:      argBool(req, "fast_search"),
		IncludeArchived: argBool(req, "include_archived"),
		OrderBy:         orderBy,
		OrderDirection:  orderDirection,
		Limit:           limit,
	}
	notes, err := h.c.SearchNotes(ctx, opts)
	if err != nil {
		return errResult("search_notes failed: %v", err)
	}
	type slim struct {
		NoteID          string      `json:"note_id"`
		Title           string      `json:"title"`
		Type            string      `json:"type"`
		Attributes      []Attribute `json:"attributes,omitempty"`
		DateModified    string      `json:"date_modified,omitempty"`
		UtcDateModified string      `json:"utc_date_modified,omitempty"`
	}
	out := make([]slim, 0, len(notes))
	for _, note := range notes {
		out = append(out, slim{
			NoteID: note.NoteID, Title: note.Title, Type: note.Type,
			Attributes: note.Attributes, DateModified: note.DateModified,
			UtcDateModified: note.UtcModified,
		})
	}
	return okJSON(map[string]any{"count": len(out), "results": out})
}
```

Do not change `Client.SearchNotes`; it already maps all three ETAPI parameters
and returns `out.Results` without reordering.

- [ ] **Step 7: Format and run focused tests**

```powershell
gofmt -w main.go main_test.go handlers_test.go
go test ./... -run 'TestArg(OptionalEnum|BoundedInt)|TestSearchNotes_' -count=1
```

Expected: all focused tests PASS.

- [ ] **Step 8: Run the complete Go test suite**

```powershell
go test ./... -count=1
```

Expected: PASS with zero failures.

- [ ] **Step 9: Commit handler behavior**

```powershell
git add main.go main_test.go handlers_test.go
git commit -m "fix: forward Trilium note ordering and dates"
```

---

### Task 3: Remove private endpoint details from logs and tool errors

**Files:**
- Modify: `wiring_test.go`
- Modify: `trilium_test.go`
- Modify: `main.go:79-82`
- Modify: `trilium.go:106-108`
- Modify: `trilium.go:179-195`

**Interfaces:**
- Consumes: `Client.urls`, `transportErr`, standard `log` package.
- Produces: endpoint-count startup log, constant transport error text, and index-only failover logs.

- [ ] **Step 1: Change the existing transport-error test to require sanitization**

In `trilium_test.go`, replace `TestTransportErr_ErrorAndUnwrap` with:

```go
func TestTransportErr_ErrorIsSanitizedAndUnwraps(t *testing.T) {
	inner := errors.New("Get \"http://private.example/etapi/notes?token=secret\": refused")
	te := transportErr{err: inner}
	if te.Error() != "trilium transport error" {
		t.Errorf("Error() = %q", te.Error())
	}
	if !errors.Is(te, inner) {
		t.Error("errors.Is should unwrap transportErr to its inner error")
	}
}
```

- [ ] **Step 2: Add failing startup and failover log tests**

Add to `wiring_test.go`:

```go
func TestRun_StartupLogOmitsEndpointAndToken(t *testing.T) {
	trilium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"appVersion":"test"}`))
	}))
	t.Cleanup(trilium.Close)
	env := func(key string) string {
		switch key {
		case "TRILIUM_URL":
			return trilium.URL
		case "TRILIUM_TOKEN":
			return "private-token"
		default:
			return ""
		}
	}
	output := captureLog(func() {
		if err := run(context.Background(), env, strings.NewReader(""), &syncBuffer{}); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	if strings.Contains(output, trilium.URL) || strings.Contains(output, "private-token") {
		t.Fatalf("startup log leaked private configuration: %q", output)
	}
	if !strings.Contains(output, "endpoints=1") {
		t.Fatalf("startup log lacks safe endpoint count: %q", output)
	}
}
```

Add to `trilium_test.go`:

```go
func TestClient_FallbackLogOmitsEndpointAndTransportDetail(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"appVersion":"x"}`))
	}))
	t.Cleanup(good.Close)
	privateURL := "http://127.0.0.1:1/private"
	c := NewClient(privateURL+","+good.URL, "tok", 2*time.Second)
	output := captureLog(func() {
		if _, err := c.AppInfo(context.Background()); err != nil {
			t.Fatalf("AppInfo: %v", err)
		}
	})
	if strings.Contains(output, privateURL) || strings.Contains(output, good.URL) ||
		strings.Contains(strings.ToLower(output), "refused") {
		t.Fatalf("failover log leaked endpoint detail: %q", output)
	}
	if !strings.Contains(output, "endpoint 1 unreachable") {
		t.Fatalf("missing safe failover message: %q", output)
	}
}
```

- [ ] **Step 3: Run secrecy tests and confirm the red state**

```powershell
go test ./... -run 'TestTransportErr_ErrorIsSanitizedAndUnwraps|TestRun_StartupLogOmitsEndpointAndToken|TestClient_FallbackLogOmitsEndpointAndTransportDetail' -count=1
```

Expected: FAIL because current errors and logs include URLs and transport details.

- [ ] **Step 4: Sanitize startup and transport reporting**

In `main.go`, replace the startup log with:

```go
log.Printf("starting %s v%s — endpoints=%d timeout=%s log=%s",
	serverName, serverVersion, len(h.c.URLs()), timeout, logLevelName(lvl))
```

In `trilium.go`, change:

```go
func (e transportErr) Error() string { return "trilium transport error" }
```

In `Client.tryURLs`, replace the failover log with:

```go
if i+1 < len(c.urls) {
	log.Printf("trilium: endpoint %d unreachable; trying next endpoint", i+1)
}
```

Keep `Unwrap()` unchanged so internal classification can still inspect the
underlying transport failure without exposing it through `%v`.

- [ ] **Step 5: Format and run secrecy plus full tests**

```powershell
gofmt -w main.go wiring_test.go trilium.go trilium_test.go
go test ./... -run 'TestTransportErr_ErrorIsSanitizedAndUnwraps|TestRun_StartupLogOmitsEndpointAndToken|TestClient_FallbackLogOmitsEndpointAndTransportDetail' -count=1
go test ./... -count=1
```

Expected: both commands PASS.

- [ ] **Step 6: Commit endpoint secrecy**

```powershell
git add main.go wiring_test.go trilium.go trilium_test.go
git commit -m "fix: redact Trilium endpoint details"
```

---

### Task 4: Version, document, and publish the CI artifact

**Files:**
- Modify: `wiring_test.go`
- Modify: `main.go:23`
- Modify: `README.md`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: completed `search_notes` contract and current CI build job.
- Produces: server version `0.1.6`, documented call example, and artifact `trilium-mcp-linux-amd64-<commit SHA>` containing the binary and checksum.

- [ ] **Step 1: Add a failing version contract**

Add to `wiring_test.go`:

```go
func TestServerVersion(t *testing.T) {
	if serverVersion != "0.1.6" {
		t.Fatalf("serverVersion = %q, want 0.1.6", serverVersion)
	}
}
```

- [ ] **Step 2: Run the version test and confirm the red state**

```powershell
go test ./... -run TestServerVersion -count=1
```

Expected: FAIL with current version `0.1.5`.

- [ ] **Step 3: Bump the server version**

Change the constant in `main.go`:

```go
serverVersion = "0.1.6"
```

- [ ] **Step 4: Document ordered searches in README**

After the existing `search_notes` usage example, add:

````markdown
To retrieve the most recently modified notes, use a match-all query and let
ETAPI order before applying the limit:

```jsonc
search_notes({
  query: "note.noteId != \"\"",
  order_by: "dateModified",
  order_direction: "desc",
  limit: 5
})
```

`order_by` accepts `dateModified` or `utcDateModified`;
`order_direction` accepts `asc` or `desc`; `limit` is an integer from 1 to 200.
Results include `date_modified` and `utc_date_modified` when supplied by
Trilium ETAPI. The server preserves ETAPI order and never invents missing dates.
````

- [ ] **Step 5: Make CI run on fix branches and upload Linux AMD64 output**

Replace `.github/workflows/ci.yml` with:

```yaml
name: CI

on:
  push:
    branches: [master, main, "fix/**"]
  pull_request:

permissions:
  contents: read

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.23"
          cache: true
      - run: go vet ./...
      - run: go test -race ./...
      - name: Build Linux AMD64
        env:
          GOOS: linux
          GOARCH: amd64
          CGO_ENABLED: "0"
        run: |
          go build -ldflags="-s -w" -o trilium-mcp-linux-amd64 .
          sha256sum trilium-mcp-linux-amd64 > trilium-mcp-linux-amd64.sha256
      - name: Smoke test (MCP initialize handshake)
        env:
          TRILIUM_URL: http://127.0.0.1:9999
          TRILIUM_TOKEN: dummy
        run: |
          printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ci","version":"0"}}}\n' \
            | timeout 5 ./trilium-mcp-linux-amd64 \
            | grep -q '"name":"trilium-mcp"'
      - name: Upload Linux AMD64 artifact
        uses: actions/upload-artifact@v4
        with:
          name: trilium-mcp-linux-amd64-${{ github.sha }}
          path: |
            trilium-mcp-linux-amd64
            trilium-mcp-linux-amd64.sha256
          if-no-files-found: error
          retention-days: 14
```

- [ ] **Step 6: Format and verify version, tests, vet, and build locally**

```powershell
gofmt -w main.go wiring_test.go
go test ./... -run TestServerVersion -count=1
go test -race ./...
go vet ./...
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -ldflags="-s -w" -o trilium-mcp-linux-amd64 .
Get-FileHash -Algorithm SHA256 trilium-mcp-linux-amd64
Remove-Item Env:GOOS,Env:GOARCH,Env:CGO_ENABLED
```

Expected: every Go command exits 0; `Get-FileHash` returns a SHA-256. The built
file is ignored by the existing `trilium-mcp-*` rule and must not be committed.

- [ ] **Step 7: Check workflow and documentation diff**

```powershell
git diff --check
git diff -- README.md .github/workflows/ci.yml
git status --short
```

Expected: no whitespace errors; only intended source, test, README, workflow,
spec, and plan files are present.

- [ ] **Step 8: Commit release-readiness changes**

```powershell
git add main.go wiring_test.go README.md .github/workflows/ci.yml
git commit -m "ci: publish verified Trilium MCP binary"
```

---

### Task 5: Verify locally, create the remote fork, and require green Actions

**Files:**
- No planned source modifications.
- Inspect: all files changed since `upstream/master`.

**Interfaces:**
- Consumes: commits from Tasks 1-4.
- Produces: pushed branch in `gipasoft/trilium-mcp`, green GitHub Actions run, downloadable binary plus matching SHA-256, and a release-readiness report. It does not deploy.

- [ ] **Step 1: Run the full local verification from a clean state**

```powershell
gofmt -w main.go main_test.go handlers_test.go wiring_test.go trilium.go trilium_test.go
go vet ./...
go test -race ./...
go test ./... -count=1
git diff --check upstream/master...HEAD
git status --short
```

Expected: all commands exit 0 and the working tree is clean. If `gofmt` changes
tracked files, inspect them, rerun the full verification, and commit only the
formatting required by the implementation.

- [ ] **Step 2: Audit the exact change set and read-only guarantees**

```powershell
git diff --stat upstream/master...HEAD
git diff upstream/master...HEAD -- main.go handlers_test.go main_test.go wiring_test.go trilium.go trilium_test.go README.md .github/workflows/ci.yml
git grep -n -E 'TRILIUM_TOKEN=|Authorization:|private-token' HEAD -- ':!docs/superpowers/**'
```

Expected:

- `search_notes` calls only `Client.SearchNotes`;
- tests assert `GET /etapi/notes`;
- no mutative call was added;
- no real token, authorization header value, or private endpoint was added;
- the literal `private-token` exists only inside the deliberate log-redaction test, if retained.

- [ ] **Step 3: Create the authorized GitHub fork when a supported GitHub surface is available**

Create `gipasoft/trilium-mcp` as a fork of `OVDEN13/trilium-mcp`. Do not create
an unrelated empty repository with the same name. If no authenticated GitHub
connector/browser is available, stop here and ask the user to create the fork;
do not substitute credentials scraped from local stores.

- [ ] **Step 4: Configure remotes without changing upstream**

After the fork exists:

```powershell
git remote get-url upstream
git remote add origin https://github.com/gipasoft/trilium-mcp.git
git remote -v
```

Expected:

- `upstream` fetches from `OVDEN13/trilium-mcp`;
- `origin` fetches and pushes to `gipasoft/trilium-mcp`.

If `origin` already exists, verify its exact URL instead of adding it again.

- [ ] **Step 5: Push the feature branch**

```powershell
git push -u origin fix/search-notes-ordering
```

Expected: push succeeds and starts the `CI` workflow because `fix/**` is an
explicit push trigger.

- [ ] **Step 6: Inspect GitHub Actions and require every step to pass**

Using the authenticated GitHub connector/browser, inspect the run for the exact
feature-branch commit. Require green status for:

- checkout and Go 1.23 setup;
- `go vet ./...`;
- `go test -race ./...`;
- Linux AMD64 static build;
- MCP initialize smoke test;
- artifact upload.

Do not accept a run from another commit or branch.

- [ ] **Step 7: Download and verify the Actions artifact**

Download `trilium-mcp-linux-amd64-<commit SHA>` into a temporary local
directory outside the repository. In that directory run:

```powershell
$expected = (Get-Content -Raw .\trilium-mcp-linux-amd64.sha256).Split()[0].ToLowerInvariant()
$actual = (Get-FileHash -Algorithm SHA256 .\trilium-mcp-linux-amd64).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw "SHA-256 mismatch: expected $expected, got $actual" }
```

Expected: no exception and identical hashes.

- [ ] **Step 8: Record the release-readiness evidence without deploying**

Report:

- fork URL and feature branch;
- exact commit SHA;
- GitHub Actions run URL and green status;
- artifact name;
- verified SHA-256;
- local vet/test/build results;
- explicit statement that the QNAP was not modified;
- remaining deployment guard: backup the current binary before replacing only
  `bin/trilium-mcp`, then recreate only the proxy service and retain rollback.

Do not create a tag, GitHub release, or QNAP change in this plan.
