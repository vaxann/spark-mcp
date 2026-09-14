package links

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parts(t *testing.T, link string) (string, string, string) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path, u.Query().Get("exp"), u.Query().Get("sig")
}

func TestSignVerify(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "state", "link-secret")
	s := NewSigner(keyFile)
	link, exp, err := s.Sign("https://mcp.example.com/", KindDownload, "/attachments/42", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "https://mcp.example.com/attachments/42?") || time.Until(exp) > time.Minute {
		t.Fatalf("link %s exp %v", link, exp)
	}
	route, e, sig := parts(t, link)
	if !s.Verify(KindDownload, route, nil, e, sig) {
		t.Fatal("valid link rejected")
	}
	for name, ok := range map[string]bool{
		"other kind":  s.Verify(KindUpload, route, nil, e, sig),
		"other route": s.Verify(KindDownload, "/attachments/43", nil, e, sig),
		"other exp":   s.Verify(KindDownload, route, nil, e+"0", sig),
		"bad sig":     s.Verify(KindDownload, route, nil, e, strings.Repeat("0", len(sig))),
	} {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
	// The key persists: a new signer over the same file verifies old links.
	if info, err := os.Stat(keyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	if !NewSigner(keyFile).Verify(KindDownload, route, nil, e, sig) {
		t.Error("link must survive a restart")
	}
	// Bound parameters are part of the signature.
	up, _, err := s.Sign("https://mcp.example.com", KindUpload, "/upload/comment/7", url.Values{"team": {"Alpha"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	uroute, ue, usig := parts(t, up)
	if !s.Verify(KindUpload, uroute, url.Values{"team": {"Alpha"}}, ue, usig) || s.Verify(KindUpload, uroute, url.Values{"team": {"Beta"}}, ue, usig) || s.Verify(KindUpload, uroute, nil, ue, usig) {
		t.Error("bound parameters not enforced")
	}
	// Expiry.
	s.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if s.Verify(KindDownload, route, nil, e, sig) {
		t.Error("expired link accepted")
	}
	if _, _, err := s.Sign("", KindDownload, "/x", nil, 0); err == nil {
		t.Error("signing without a base URL must fail")
	}
}
