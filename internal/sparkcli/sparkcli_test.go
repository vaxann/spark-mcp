package sparkcli_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vaxann/spark-mcp/internal/sparkcli"
	"github.com/vaxann/spark-mcp/internal/testutil"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func catalog(t *testing.T, fake *testutil.FakeSpark) *sparkcli.Catalog {
	t.Helper()
	data, err := os.ReadFile(fake.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	c, err := sparkcli.ParseCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tool(t *testing.T, c *sparkcli.Catalog, name string) *sparkcli.Tool {
	t.Helper()
	for i := range c.Tools {
		if c.Tools[i].Name == name {
			return &c.Tools[i]
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

func args(t *testing.T, js string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(js))
	dec.UseNumber()
	m := map[string]any{}
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBuildArgs(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	c := catalog(t, fake)
	tests := []struct {
		tool, in string
		want     []string
		errCode  string
	}{
		{"accounts", `{}`, []string{"accounts"}, ""},
		{"emails", `{"folder":"Inbox","filter":"is:unread from:a@example.com","page":2,"new_senders":true}`,
			[]string{"emails", "--filter=is:unread from:a@example.com", "--page=2", "--new-senders", "--", "Inbox"}, ""},
		{"emails", `{"new_senders":false,"filter":""}`, []string{"emails"}, ""},
		// Values that look like options stay values.
		{"emails", `{"folder":"--help","filter":"-x"}`, []string{"emails", "--filter=-x", "--", "--help"}, ""},
		{"thread", `{"message_id":1114}`, []string{"thread", "--", "1114"}, ""},
		{"thread", `{}`, nil, sparkcli.CodeInvalidArgument},
		{"action", `{"action_name":"archive","message_ids":["1","2"],"user":["a@example.com","b@example.com"]}`,
			[]string{"action", "--user=a@example.com", "--user=b@example.com", "--", "archive", "1", "2"}, ""},
		{"action", `{"action_name":"pin","message_ids":"7"}`, []string{"action", "--", "pin", "7"}, ""},
		{"action", `{"action_name":"pin","message_ids":[]}`, nil, sparkcli.CodeInvalidArgument},
		{"attachment", `{"id":"42"}`, []string{"attachment", "--", "42"}, ""},
		{"attachment", `{"id":4.5}`, nil, sparkcli.CodeInvalidArgument},
		{"emails", `{"page":"two"}`, nil, sparkcli.CodeInvalidArgument},
		{"emails", `{"new_senders":"yes"}`, nil, sparkcli.CodeInvalidArgument},
		{"emails", `{"folders":["Inbox"]}`, nil, sparkcli.CodeInvalidArgument},
		{"draft", `{"mode":"signatures"}`, []string{"draft", "--", "signatures"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.tool+" "+tc.in, func(t *testing.T) {
			got, err := tool(t, c, tc.tool).BuildArgs(args(t, tc.in))
			if tc.errCode != "" {
				var se *sparkcli.Error
				if !errors.As(err, &se) || se.Code != tc.errCode {
					t.Fatalf("want %s, got %v", tc.errCode, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestInputSchema(t *testing.T) {
	c := catalog(t, testutil.NewFakeSpark(t))
	s := tool(t, c, "action").InputSchema()
	if s["type"] != "object" || !reflect.DeepEqual(s["required"], []string{"action_name", "message_ids"}) {
		t.Fatalf("schema: %v", s)
	}
	ids := s["properties"].(map[string]any)["message_ids"].(map[string]any)
	if ids["type"] != "array" || ids["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("array schema: %v", ids)
	}
	if _, err := sparkcli.ParseCatalog([]byte(`{"tools":[{"name":"x","command":"x","parameters":[{"name":"p","type":"object"}]}]}`)); err == nil {
		t.Error("unsupported parameter types must be rejected")
	}
	if _, err := sparkcli.ParseCatalog([]byte(`not json`)); err == nil {
		t.Error("garbage must be rejected")
	}
}

func codeOf(err error) string {
	var se *sparkcli.Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestRunner(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	r := sparkcli.NewRunner(fake.Bin, 2, 3*time.Second, 1<<10, quiet())
	ctx := context.Background()

	res, err := r.Run(ctx, sparkcli.Call{Args: []string{"emails", "--", "Inbox"}, Agent: "claude-ai"})
	if err != nil || !strings.Contains(res.Text(), "ok: emails -- Inbox") {
		t.Fatalf("run: %v %v", res, err)
	}
	last := fake.Last(t)
	if last.Agent != "claude-ai" || !reflect.DeepEqual(last.Args, []string{"emails", "--", "Inbox"}) {
		t.Fatalf("recorded %+v", last)
	}

	// Stdin reaches the CLI byte for byte.
	payload := []byte("\x00\x01binary\xff")
	if _, err := r.Run(ctx, sparkcli.Call{Args: []string{"comment", "--attach-stream=a.bin", "--", "9"}, Stdin: payload}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Last(t).Stdin; string(got) != string(payload) {
		t.Fatalf("stdin %q", got)
	}

	// Text beyond the limit is truncated, not fatal.
	res, err = r.Run(ctx, sparkcli.Call{Args: []string{"emails", "trigger-big"}})
	if err != nil || !res.Truncated || len(res.Stdout) != 1<<10 || !strings.Contains(res.Text(), "truncated") {
		t.Fatalf("truncation: %v %v", err, res != nil && res.Truncated)
	}
	// Binary beyond the limit fails.
	if _, err := r.Run(ctx, sparkcli.Call{Args: []string{"emails", "trigger-big"}, MaxBytes: 100}); codeOf(err) != sparkcli.CodeTooLarge {
		t.Fatalf("strict limit: %v", err)
	}
	if _, err := r.Run(ctx, sparkcli.Call{Args: []string{"thread", "trigger-error"}}); codeOf(err) != sparkcli.CodeCLI || !strings.Contains(err.Error(), "no thread found") {
		t.Fatalf("cli error: %v", err)
	}
	if _, err := r.Run(ctx, sparkcli.Call{Args: []string{"trigger-offline"}}); codeOf(err) != sparkcli.CodeUnavailable {
		t.Fatalf("offline: %v", err)
	}
	if _, err := r.Run(ctx, sparkcli.Call{Args: []string{"trigger-sleep"}, Timeout: 200 * time.Millisecond}); codeOf(err) != sparkcli.CodeTimeout {
		t.Fatalf("timeout: %v", err)
	}
	missing := sparkcli.NewRunner("/nonexistent/spark", 1, time.Second, 1<<10, quiet())
	if _, err := missing.Run(ctx, sparkcli.Call{Args: []string{"tools"}}); codeOf(err) != sparkcli.CodeUnavailable {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestAttachments(t *testing.T) {
	fake := testutil.NewFakeSpark(t)
	r := sparkcli.NewRunner(fake.Bin, 1, 3*time.Second, 1<<20, quiet())
	ctx := context.Background()
	info, err := r.StatAttachment(ctx, "42", sparkcli.Call{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "photo.png" || info.Size != 24 || info.MediaType != "image/png" || info.MessageID != "555" {
		t.Fatalf("info %+v", info)
	}
	if _, err := r.StatAttachment(ctx, "404", sparkcli.Call{}); codeOf(err) != sparkcli.CodeCLI {
		t.Fatalf("missing attachment: %v", err)
	}
	if _, err := r.StatAttachment(ctx, "-1", sparkcli.Call{}); codeOf(err) != sparkcli.CodeInvalidArgument {
		t.Fatalf("bad id: %v", err)
	}
	data, err := r.ReadAttachment(ctx, "42", 1<<20, sparkcli.Call{})
	if err != nil || sparkcli.SniffMIME(data) != "image/png" {
		t.Fatalf("read: %q %v", data, err)
	}
	if _, err := r.ReadAttachment(ctx, "42", 4, sparkcli.Call{}); codeOf(err) != sparkcli.CodeTooLarge {
		t.Fatalf("read limit: %v", err)
	}
	if _, err := r.AttachStream(ctx, "draft", "777", "../evil\n.pdf", []byte("pdf"), nil, sparkcli.Call{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Last(t).Args; !reflect.DeepEqual(got, []string{"draft", "--edit=777", "--attach-stream=_evil_.pdf"}) {
		t.Fatalf("attach args %q", got)
	}
	if _, err := r.AttachStream(ctx, "comment", "9", "a.txt", []byte("x"), []string{"--team=Alpha"}, sparkcli.Call{}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Last(t).Args; !reflect.DeepEqual(got, []string{"comment", "--attach-stream=a.txt", "--team=Alpha", "--", "9"}) {
		t.Fatalf("comment args %q", got)
	}
}

func TestOutputHelpers(t *testing.T) {
	out := "Draft saved.\nID: 777\nLink: https://sparkmailapp.com/dpl/bl?token=x\n"
	if sparkcli.ParseDraftID(out) != "777" || sparkcli.ParseLink(out) != "https://sparkmailapp.com/dpl/bl?token=x" {
		t.Error("draft output parsing")
	}
	for in, want := range map[string]string{"IMAGE/PNG; x=y": "image/png", "IMAGE/IMAGE/PNG": "application/octet-stream", "": "application/octet-stream"} {
		if got := sparkcli.NormalizeMIME(in); got != want {
			t.Errorf("NormalizeMIME(%q) = %q", in, got)
		}
	}
	if sparkcli.SanitizeAgent(" Claude Code (desktop) ") != "Claude-Code-desktop" {
		t.Errorf("agent: %q", sparkcli.SanitizeAgent(" Claude Code (desktop) "))
	}
	if sparkcli.SafeFileName("...") != "attachment" || sparkcli.SafeFileName("a/b\\c.txt") != "a_b_c.txt" {
		t.Error("file names")
	}
}
