package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vaxann/spark-mcp/internal/links"
	"github.com/vaxann/spark-mcp/internal/sparkcli"
	"github.com/vaxann/spark-mcp/internal/testutil"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newServer(t *testing.T, fake *testutil.FakeSpark, remote bool) *Server {
	t.Helper()
	return New(context.Background(), Options{
		Runner:            sparkcli.NewRunner(fake.Bin, 4, 5*time.Second, 1<<20, quiet()),
		Version:           "test",
		Log:               quiet(),
		LocalFiles:        !remote,
		Remote:            remote,
		AttachmentTimeout: 5 * time.Second,
		MaxAttachment:     1 << 20,
		MaxUpload:         1 << 20,
		LinkTTL:           time.Minute,
		CatalogRefresh:    time.Hour,
		Signer:            links.NewSigner(filepath.Join(t.TempDir(), "link-secret")),
	})
}

func connectInMemory(t *testing.T, s *Server, onListChanged func()) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.MCP().Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	opts := &mcp.ClientOptions{}
	if onListChanged != nil {
		opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) { onListChanged() }
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test client", Version: "1"}, opts)
	sess, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func toolNames(t *testing.T, sess *mcp.ClientSession) (names []string, byName map[string]*mcp.Tool) {
	t.Helper()
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName = map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
		byName[tl.Name] = tl
	}
	slices.Sort(names)
	return names, byName
}

func props(t *testing.T, tl *mcp.Tool) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(tl.InputSchema)
	var s struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Properties
}

func call(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolListFollowsTransport(t *testing.T) {
	fake := testutil.NewFakeSpark(t)

	local := connectInMemory(t, newServer(t, fake, false), nil)
	names, byName := toolNames(t, local)
	want := []string{"accounts", "action", "attachment", "comment", "draft", "emails", "thread"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("stdio tools %v", names)
	}
	if p := props(t, byName["draft"]); p["attach"] == nil || p["attachments"] == nil || p["attachment_ids"] == nil {
		t.Fatalf("stdio draft schema %v", p)
	}
	if byName["emails"].Annotations == nil || !byName["emails"].Annotations.ReadOnlyHint {
		t.Error("emails must be read-only")
	}
	if a := byName["draft"].Annotations; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Error("draft must be marked destructive")
	}
	init := local.InitializeResult()
	if !strings.Contains(init.Instructions, "Synthetic Spark instructions") || !strings.Contains(init.Instructions, "attachment_upload_link") {
		t.Errorf("instructions %q", init.Instructions)
	}

	remote := connectInMemory(t, newServer(t, fake, true), nil)
	names, byName = toolNames(t, remote)
	if !slices.Contains(names, toolAttachmentLink) || !slices.Contains(names, toolUploadLink) {
		t.Fatalf("remote tools %v", names)
	}
	if p := props(t, byName["comment"]); p["attach"] != nil || p["attachments"] == nil {
		t.Fatalf("remote comment schema must drop local paths: %v", p)
	}
}

func TestCatalogToolCalls(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	sess := connectInMemory(t, newServer(t, fake, false), nil)
	fake.Reset(t)

	res := call(t, sess, "emails", map[string]any{"folder": "Inbox", "filter": "is:unread", "page": 2})
	if res.IsError || !strings.Contains(text(res), "ok: emails") {
		t.Fatalf("emails: %s", text(res))
	}
	last := fake.Last(t)
	if !reflect.DeepEqual(last.Args, []string{"emails", "--filter=is:unread", "--page=2", "--", "Inbox"}) || last.Agent != "test-client" {
		t.Fatalf("recorded %+v", last)
	}

	call(t, sess, "thread", map[string]any{"message_id": "1114"})
	if got := fake.Last(t).Args; !reflect.DeepEqual(got, []string{"thread", "--hide-attachment-paths", "--", "1114"}) {
		t.Fatalf("thread args %q", got)
	}

	res = call(t, sess, "thread", map[string]any{"message_id": "trigger-error"})
	if !res.IsError || !strings.Contains(text(res), "cli_error") || !strings.Contains(text(res), "no thread found") {
		t.Fatalf("cli error: %s", text(res))
	}
	if sc, _ := res.StructuredContent.(map[string]any); sc["code"] != sparkcli.CodeCLI {
		t.Errorf("structured error %v", res.StructuredContent)
	}
	res = call(t, sess, "emails", map[string]any{"bogus": 1})
	if !res.IsError || !strings.Contains(text(res), "invalid_argument") {
		t.Fatalf("unknown argument: %s", text(res))
	}
}

func TestDraftAttachments(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	sess := connectInMemory(t, newServer(t, fake, false), nil)
	fake.Reset(t)

	local := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(local, []byte("local file"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := call(t, sess, "draft", map[string]any{
		"to":             []string{"alice@example.com"},
		"subject":        "Report",
		"body":           "See attached",
		"attachments":    []map[string]any{{"name": "report.pdf", "content_base64": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 test"))}},
		"attach":         []string{local},
		"attachment_ids": []string{"42"},
	})
	if res.IsError {
		t.Fatalf("draft: %s", text(res))
	}
	calls := fake.Calls(t)
	if len(calls) != 3 {
		t.Fatalf("want base call + 2 attach calls, got %d: %+v", len(calls), calls)
	}
	if want := []string{"draft", "--attach-id=42", "--to=alice@example.com", "--subject=Report", "--body=See attached"}; !reflect.DeepEqual(calls[0].Args, want) {
		t.Fatalf("base call %q", calls[0].Args)
	}
	if !reflect.DeepEqual(calls[1].Args, []string{"draft", "--edit=777", "--attach-stream=report.pdf"}) || string(calls[1].Stdin) != "%PDF-1.4 test" {
		t.Fatalf("first attach %q %q", calls[1].Args, calls[1].Stdin)
	}
	if !reflect.DeepEqual(calls[2].Args, []string{"draft", "--edit=777", "--attach-stream=notes.txt"}) || string(calls[2].Stdin) != "local file" {
		t.Fatalf("second attach %q", calls[2].Args)
	}

	// Attaching to an existing draft skips the base call.
	fake.Reset(t)
	call(t, sess, "draft", map[string]any{"edit": "777", "attachments": []map[string]any{{"name": "a.txt", "content_base64": "YQ=="}}})
	if calls := fake.Calls(t); len(calls) != 1 || calls[0].Args[1] != "--edit=777" {
		t.Fatalf("edit attach %+v", calls)
	}

	for name, args := range map[string]map[string]any{
		"bad base64": {"body": "x", "attachments": []map[string]any{{"name": "a", "content_base64": "@@@"}}},
		"no name":    {"body": "x", "attachments": []map[string]any{{"content_base64": "YQ=="}}},
		"unshare":    {"edit": "777", "unshare": true, "attachments": []map[string]any{{"name": "a", "content_base64": "YQ=="}}},
		"too large":  {"body": "x", "attachments": []map[string]any{{"name": "a", "content_base64": base64.StdEncoding.EncodeToString(make([]byte, 2<<20))}}},
	} {
		if res := call(t, sess, "draft", args); !res.IsError {
			t.Errorf("%s must fail", name)
		}
	}
}

func TestRemoteRejectsLocalPaths(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	sess := connectInMemory(t, newServer(t, fake, true), nil)
	fake.Reset(t)
	res := call(t, sess, "draft", map[string]any{"body": "x", "attach": []string{"/etc/hosts"}})
	if !res.IsError || !strings.Contains(text(res), "not available over a remote connection") {
		t.Fatalf("remote attach: %s", text(res))
	}
	if len(fake.Calls(t)) != 0 {
		t.Fatal("spark must not run")
	}
}

func TestCommentAttachments(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	sess := connectInMemory(t, newServer(t, fake, true), nil)
	fake.Reset(t)
	res := call(t, sess, "comment", map[string]any{
		"message_id":  "9",
		"body":        "Files below",
		"team":        "Alpha",
		"attachments": []map[string]any{{"name": "a.png", "content_base64": "YQ=="}, {"name": "b.txt", "content_base64": "Yg=="}},
	})
	if res.IsError {
		t.Fatal(text(res))
	}
	calls := fake.Calls(t)
	if len(calls) != 3 ||
		!reflect.DeepEqual(calls[0].Args, []string{"comment", "--body=Files below", "--team=Alpha", "--", "9"}) ||
		!reflect.DeepEqual(calls[1].Args, []string{"comment", "--attach-stream=a.png", "--team=Alpha", "--", "9"}) ||
		string(calls[2].Stdin) != "b" {
		t.Fatalf("calls %+v", calls)
	}
	if res := call(t, sess, "comment", map[string]any{"edit": "5", "attachments": []map[string]any{{"name": "a", "content_base64": "YQ=="}}}); !res.IsError {
		t.Error("edit with attachments must fail")
	}
}

func TestAttachmentTool(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	s := newServer(t, fake, false)
	sess := connectInMemory(t, s, nil)
	res := call(t, sess, "attachment", map[string]any{"id": 42})
	if res.IsError || len(res.Content) != 2 {
		t.Fatalf("attachment: %+v", res)
	}
	img, ok := res.Content[1].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || !strings.HasPrefix(string(img.Data), "\x89PNG") {
		t.Fatalf("image block %#v", res.Content[1])
	}
	if !strings.Contains(text(res), "photo.png") {
		t.Errorf("summary %q", text(res))
	}
	s.opts.MaxAttachment = 10
	if res := call(t, sess, "attachment", map[string]any{"id": 42}); !res.IsError || !strings.Contains(text(res), "too_large") {
		t.Fatalf("limit: %s", text(res))
	}
	if res := call(t, sess, "attachment", map[string]any{"id": "abc"}); !res.IsError {
		t.Fatal("non-numeric id must fail")
	}
}

func TestCatalogRefresh(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	s := newServer(t, fake, true)
	changed := make(chan struct{}, 4)
	sess := connectInMemory(t, s, func() { changed <- struct{}{} })
	toolNames(t, sess)

	// Spark drops write tools (access level lowered to read-only).
	var cat map[string]any
	data, _ := os.ReadFile(fake.Catalog)
	_ = json.Unmarshal(data, &cat)
	var kept []any
	for _, tl := range cat["tools"].([]any) {
		if n := tl.(map[string]any)["name"]; n != "draft" && n != "comment" && n != "action" {
			kept = append(kept, tl)
		}
	}
	cat["tools"] = kept
	data, _ = json.Marshal(cat)
	if err := os.WriteFile(fake.Catalog, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s.stateMu.Lock()
	s.lastAttempt = time.Now().Add(-2 * time.Hour)
	s.stateMu.Unlock()

	names, _ := toolNames(t, sess)
	if slices.Contains(names, "draft") || slices.Contains(names, toolUploadLink) || !slices.Contains(names, toolAttachmentLink) {
		t.Fatalf("after refresh: %v", names)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("clients were not notified of the tool list change")
	}
}

func TestCatalogUnavailableAtStart(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	good, _ := os.ReadFile(fake.Catalog)
	if err := os.WriteFile(fake.Catalog, []byte("Error: Spark Desktop is not running."), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newServer(t, fake, false)
	sess := connectInMemory(t, s, nil)
	if names, _ := toolNames(t, sess); len(names) != 0 {
		t.Fatalf("no tools expected, got %v", names)
	}
	if !strings.Contains(sess.InitializeResult().Instructions, "Spark Desktop is not running") {
		t.Error("fallback instructions expected")
	}
	if err := os.WriteFile(fake.Catalog, good, 0o600); err != nil {
		t.Fatal(err)
	}
	s.stateMu.Lock()
	s.lastAttempt = time.Now().Add(-time.Minute)
	s.stateMu.Unlock()
	if names, _ := toolNames(t, sess); !slices.Contains(names, "emails") {
		t.Fatalf("tools must appear once Spark answers: %v", names)
	}
}
