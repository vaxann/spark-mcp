package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vaxann/spark-mcp/internal/testutil"
)

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(r)
}

func newHTTP(t *testing.T, token string) (*httptest.Server, *testutil.FakeSpark) {
	t.Helper()
	fake := testutil.NewFakeSpark(t)
	s := newServer(t, fake, true)
	ts := httptest.NewServer(s.Handler(HTTPOptions{Token: token}))
	t.Cleanup(ts.Close)
	return ts, fake
}

func connectHTTP(t *testing.T, ts *httptest.Server, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "http-client", Version: "1"}, nil)
	hc := &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}}
	sess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: hc}, nil)
	if err == nil {
		t.Cleanup(func() { _ = sess.Close() })
	}
	return sess, err
}

func TestHTTPBearerAuth(t *testing.T) {
	ts, fake := newHTTP(t, "s3cret")
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("expected 401 with WWW-Authenticate, got %d", resp.StatusCode)
	}
	if _, err := connectHTTP(t, ts, "wrong"); err == nil {
		t.Fatal("wrong token must fail")
	}
	resp, err = http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok"`) {
		t.Errorf("healthz: %d %s", resp.StatusCode, body)
	}
	sess, err := connectHTTP(t, ts, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	fake.Reset(t)
	res := call(t, sess, "accounts", nil)
	if res.IsError || fake.Last(t).Agent != "http-client" {
		t.Fatalf("accounts over HTTP: %s %+v", text(res), fake.Last(t))
	}
	// Attachment routes demand a token or a signature.
	for _, u := range []string{"/attachments/42", "/upload/draft/777"} {
		resp, _ := http.Get(ts.URL + u)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s without auth: %d", u, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/drafts/777/attachments/a.txt", strings.NewReader("x"))
	resp, _ = http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("raw upload without token: %d", resp.StatusCode)
	}
}

func linkFrom(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", text(res))
	}
	var out linkOut
	if err := json.Unmarshal([]byte(text(res)), &out); err != nil {
		t.Fatalf("link output %q: %v", text(res), err)
	}
	return out.URL
}

func TestSignedDownloadLink(t *testing.T) {
	ts, _ := newHTTP(t, "s3cret")
	sess, err := connectHTTP(t, ts, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	link := linkFrom(t, call(t, sess, toolAttachmentLink, map[string]any{"id": 42, "download": true}))
	if !strings.HasPrefix(link, ts.URL+"/attachments/42?") {
		t.Fatalf("link %s must use the request host", link)
	}
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "\x89PNG") {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	h := resp.Header
	if h.Get("Content-Type") != "image/png" || !strings.HasPrefix(h.Get("Content-Disposition"), "attachment; filename*=UTF-8''photo.png") ||
		h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("headers %v", h)
	}
	// Tampering with the path or signature is refused.
	tampered := strings.Replace(link, "/attachments/42?", "/attachments/43?", 1)
	if resp, _ := http.Get(tampered); resp.StatusCode != http.StatusForbidden {
		t.Errorf("tampered link: %d", resp.StatusCode)
	}
	// With a token, no signature is needed; CLI errors surface as JSON.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/attachments/404", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, _ = http.DefaultClient.Do(req)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "cli_error") {
		t.Errorf("missing attachment: %d %s", resp.StatusCode, body)
	}
	// Forwarded headers from a tunnel shape the public URL.
	hc := &http.Client{Transport: headerRT{h: http.Header{"X-Forwarded-Proto": {"https"}}, next: bearer{token: "s3cret", next: http.DefaultTransport}}}
	client := mcp.NewClient(&mcp.Implementation{Name: "proxied", Version: "1"}, nil)
	psess, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = psess.Close() }()
	if l := linkFrom(t, call(t, psess, toolAttachmentLink, map[string]any{"id": 42})); !strings.HasPrefix(l, "https://"+strings.TrimPrefix(ts.URL, "http://")) {
		t.Errorf("forwarded link %s", l)
	}
}

type headerRT struct {
	h    http.Header
	next http.RoundTripper
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.h {
		r.Header[k] = v
	}
	return h.next.RoundTrip(r)
}

func multipartBody(t *testing.T, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write([]byte(content))
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestUploadLink(t *testing.T) {
	ts, fake := newHTTP(t, "s3cret")
	sess, err := connectHTTP(t, ts, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if res := call(t, sess, toolUploadLink, map[string]any{"draft_id": "777", "message_id": "9"}); !res.IsError {
		t.Error("both targets must be refused")
	}
	link := linkFrom(t, call(t, sess, toolUploadLink, map[string]any{"draft_id": "777"}))
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Attach files to a Spark draft") {
		t.Fatalf("upload page: %d %s", resp.StatusCode, page)
	}
	fake.Reset(t)
	body, ctype := multipartBody(t, map[string]string{"scan.pdf": "%PDF-1.7"})
	resp, err = http.Post(link, ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Attached to draft 777: scan.pdf") || !strings.Contains(string(page), "Open in Spark") {
		t.Fatalf("upload result: %d %s", resp.StatusCode, page)
	}
	last := fake.Last(t)
	if !reflect.DeepEqual(last.Args, []string{"draft", "--edit=777", "--attach-stream=scan.pdf"}) || string(last.Stdin) != "%PDF-1.7" {
		t.Fatalf("recorded %+v", last)
	}

	// Comment uploads bind the team into the signature.
	clink := linkFrom(t, call(t, sess, toolUploadLink, map[string]any{"message_id": "9", "team": "Alpha"}))
	u, _ := url.Parse(clink)
	q := u.Query()
	q.Set("team", "Beta")
	u.RawQuery = q.Encode()
	body, ctype = multipartBody(t, map[string]string{"a.txt": "a"})
	if resp, _ := http.Post(u.String(), ctype, body); resp.StatusCode != http.StatusForbidden {
		t.Errorf("team swap accepted: %d", resp.StatusCode)
	}
	body, ctype = multipartBody(t, map[string]string{"a.txt": "a"})
	resp, _ = http.Post(clink, ctype, body)
	_ = resp.Body.Close()
	if got := fake.Last(t).Args; resp.StatusCode != 200 || !reflect.DeepEqual(got, []string{"comment", "--attach-stream=a.txt", "--team=Alpha", "--", "9"}) {
		t.Fatalf("comment upload %d %q", resp.StatusCode, got)
	}

	// Raw upload with the token.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/drafts/777/attachments/raw.bin", strings.NewReader("raw"))
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, _ = http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if last := fake.Last(t); resp.StatusCode != http.StatusCreated || last.Args[2] != "--attach-stream=raw.bin" || string(last.Stdin) != "raw" {
		t.Fatalf("raw upload %d %+v", resp.StatusCode, last)
	}
	big := strings.NewReader(strings.Repeat("x", 2<<20))
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/drafts/777/attachments/big.bin", big)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, _ = http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize raw upload: %d", resp.StatusCode)
	}
}

func TestLoopbackWithoutToken(t *testing.T) {
	ts, _ := newHTTP(t, "")
	if _, err := connectHTTP(t, ts, ""); err != nil {
		t.Fatalf("loopback without token must work: %v", err)
	}
	if !IsLoopback("127.0.0.1:1") || !IsLoopback("localhost:1") || IsLoopback("0.0.0.0:1") || IsLoopback("[::]:1") || IsLoopback("bad") {
		t.Error("IsLoopback wrong")
	}
	s := &Server{}
	if err := s.RunHTTP(context.Background(), HTTPOptions{Listen: "0.0.0.0:0"}); err == nil || !strings.Contains(err.Error(), "without a token") {
		t.Errorf("public listen without token: %v", err)
	}
}
