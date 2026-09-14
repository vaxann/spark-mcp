package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTS(t *testing.T, statePath string) (*httptest.Server, *Server) {
	t.Helper()
	s, err := New(Config{Password: "hunter2", StaticToken: "static", StatePath: statePath, AccessTTL: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Mount(mux)
	mux.Handle("/mcp", s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("mcp ok")) })))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, s
}

func getJSON(t *testing.T, c *http.Client, u string) map[string]any {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %v", u, resp.StatusCode, m)
	}
	return m
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestFullFlow(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "oauth.json")
	ts, _ := newTS(t, statePath)
	c := noRedirect()

	// 1. Discovery from a 401.
	resp, _ := c.Get(ts.URL + "/mcp")
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=\""+ts.URL+"/.well-known/oauth-protected-resource\"") {
		t.Fatalf("401 discovery header: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	pr := getJSON(t, c, ts.URL+"/.well-known/oauth-protected-resource")
	if pr["resource"] != ts.URL+"/mcp" {
		t.Fatalf("resource metadata: %v", pr)
	}
	as := getJSON(t, c, ts.URL+"/.well-known/oauth-authorization-server")
	if as["issuer"] != ts.URL || as["registration_endpoint"] != ts.URL+"/oauth/register" {
		t.Fatalf("as metadata: %v", as)
	}

	// 2. Dynamic client registration (public client, like the Claude apps).
	body := `{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude","token_endpoint_auth_method":"none"}`
	resp, err := c.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	if resp.StatusCode != 201 || reg["client_id"] == nil || reg["client_secret"] != nil {
		t.Fatalf("register: %d %v", resp.StatusCode, reg)
	}
	clientID := reg["client_id"].(string)
	resp, _ = c.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(`{"redirect_uris":["http://evil.example/cb"]}`))
	if resp.StatusCode != 400 {
		t.Error("plain http redirect must be rejected")
	}

	// 3. Authorization request with PKCE -> sign-in page.
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authURL := ts.URL + "/oauth/authorize?" + url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"state": {"xyz"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "scope": {"spark"}}.Encode()
	resp, _ = c.Get(authURL)
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(page), `name="req_id" value="`) || !strings.Contains(string(page), "Claude") {
		t.Fatalf("sign-in page: %d\n%s", resp.StatusCode, page)
	}
	reqID := strings.SplitN(strings.SplitN(string(page), `name="req_id" value="`, 2)[1], `"`, 2)[0]
	// Missing PKCE is refused with a redirect error.
	resp, _ = c.Get(ts.URL + "/oauth/authorize?" + url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}}.Encode())
	if resp.StatusCode != 302 || !strings.Contains(resp.Header.Get("Location"), "error=invalid_request") {
		t.Errorf("missing PKCE: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Unregistered redirect must not redirect at all.
	resp, _ = c.Get(ts.URL + "/oauth/authorize?" + url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://attacker.example/cb"}}.Encode())
	if resp.StatusCode != 400 {
		t.Errorf("unregistered redirect: %d", resp.StatusCode)
	}

	// 4. Wrong password re-renders; right password redirects with a code.
	resp, _ = c.PostForm(ts.URL+"/oauth/authorize", url.Values{"req_id": {reqID}, "password": {"nope"}})
	if resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	resp, _ = c.PostForm(ts.URL+"/oauth/authorize", url.Values{"req_id": {reqID}, "password": {"hunter2"}})
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || loc.Host != "claude.ai" || loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("grant redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	code := loc.Query().Get("code")
	// The sign-in request is single use.
	resp, _ = c.PostForm(ts.URL+"/oauth/authorize", url.Values{"req_id": {reqID}, "password": {"hunter2"}})
	if resp.StatusCode != 400 {
		t.Errorf("req_id reuse: %d", resp.StatusCode)
	}

	// 5. Token exchange: bad verifier fails, good one succeeds, code is single use.
	tokenReq := func(v url.Values) (int, map[string]any) {
		resp, err := c.PostForm(ts.URL+"/oauth/token", v)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	st, m := tokenReq(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "code_verifier": {strings.Repeat("x", 64)}})
	if st != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("bad verifier: %d %v", st, m)
	}
	// The code was consumed by the failed attempt: re-run the sign-in to get a fresh one.
	resp, _ = c.Get(authURL)
	page, _ = io.ReadAll(resp.Body)
	reqID = strings.SplitN(strings.SplitN(string(page), `name="req_id" value="`, 2)[1], `"`, 2)[0]
	resp, _ = c.PostForm(ts.URL+"/oauth/authorize", url.Values{"req_id": {reqID}, "password": {"hunter2"}})
	loc, _ = url.Parse(resp.Header.Get("Location"))
	code = loc.Query().Get("code")
	st, tok := tokenReq(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "code_verifier": {verifier}})
	if st != 200 || tok["access_token"] == nil || tok["refresh_token"] == nil || tok["token_type"] != "Bearer" {
		t.Fatalf("token: %d %v", st, tok)
	}
	if st, _ := tokenReq(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "code_verifier": {verifier}}); st != 400 {
		t.Error("code must be single use")
	}

	// 6. Access token works on the resource; static token still works; junk does not.
	access := tok["access_token"].(string)
	check := func(token string) int {
		req, _ := http.NewRequest("GET", ts.URL+"/mcp", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, _ := c.Do(req)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if check(access) != 200 || check("static") != 200 || check("sp_at_junk") != 401 || check("") != 401 {
		t.Fatalf("resource access: %d %d %d %d", check(access), check("static"), check("sp_at_junk"), check(""))
	}

	// 7. Refresh rotates: old refresh and old access die.
	refresh := tok["refresh_token"].(string)
	st, tok2 := tokenReq(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	if st != 200 || tok2["access_token"] == access {
		t.Fatalf("refresh: %d %v", st, tok2)
	}
	if st, _ := tokenReq(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}); st != 400 {
		t.Error("old refresh token must be invalid after rotation")
	}
	if check(access) != 401 || check(tok2["access_token"].(string)) != 200 {
		t.Error("old access token must die with its refresh token")
	}

	// 8. State survives a restart.
	ts2, _ := newTS(t, statePath)
	req, _ := http.NewRequest("GET", ts2.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok2["access_token"].(string))
	resp, _ = c.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("token must survive restart: %d", resp.StatusCode)
	}
	if st, _ := tokenReq(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok2["refresh_token"].(string)}, "client_id": {clientID}}); st != 200 {
		t.Error("refresh must survive restart")
	}
}

func TestConfidentialClientAndBaseURL(t *testing.T) {
	ts, s := newTS(t, "")
	resp, _ := http.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(`{"redirect_uris":["https://app.example/cb"],"client_name":"App"}`))
	var reg map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	if reg["client_secret"] == nil || reg["token_endpoint_auth_method"] != "client_secret_basic" {
		t.Fatalf("confidential registration: %v", reg)
	}
	// Token endpoint refuses without the secret.
	resp, _ = http.PostForm(ts.URL+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {reg["client_id"].(string)}})
	if resp.StatusCode != 401 {
		t.Errorf("missing secret: %d", resp.StatusCode)
	}
	// Base URL honours proxy headers and PublicURL.
	r := httptest.NewRequest("GET", "http://internal:8765/x", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "mcp.example.com")
	if got := s.baseURL(r); got != "https://mcp.example.com" {
		t.Errorf("derived base: %s", got)
	}
	s.cfg.PublicURL = "https://fixed.example/"
	if got := s.baseURL(r); got != "https://fixed.example" {
		t.Errorf("public url: %s", got)
	}
	if _, err := New(Config{}); err == nil {
		t.Error("password required")
	}
}
