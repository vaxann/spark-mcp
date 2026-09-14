// Package links signs and verifies short-lived URLs that let a browser
// download an attachment or upload files without an MCP session.
package links

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kinds of signed links.
const (
	KindDownload = "get"
	KindUpload   = "put"
)

// Signer holds the HMAC key.
type Signer struct {
	path string
	mu   sync.Mutex
	key  []byte
	now  func() time.Time
}

// NewSigner uses (and creates on first use) the key file at path. An empty
// path keeps a random in-memory key, so links die with the process.
func NewSigner(path string) *Signer {
	return &Signer{path: path, now: time.Now}
}

func (s *Signer) secret() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	if s.path != "" {
		if data, err := os.ReadFile(s.path); err == nil && len(bytes.TrimSpace(data)) >= 32 {
			s.key = bytes.TrimSpace(data)
			return s.key, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := []byte(hex.EncodeToString(raw))
	if s.path != "" {
		if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(s.path, key, 0o600); err != nil {
			return nil, err
		}
	}
	s.key = key
	return key, nil
}

func (s *Signer) mac(kind, subj string, exp int64) (string, error) {
	key, err := s.secret()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(m, "%s\n%s\n%d", kind, subj, exp)
	return hex.EncodeToString(m.Sum(nil)), nil
}

// subject is what a signature covers: the URL path plus any bound query
// parameters (for example the team of a comment upload).
func subject(route string, bound url.Values) string {
	if len(bound) == 0 {
		return route
	}
	return route + "?" + bound.Encode()
}

// Sign returns base + route + "?<bound>&exp=..&sig=..". The route is the URL
// path (for example /attachments/42); it and the bound parameters are signed,
// so a link is valid for exactly one resource, kind and parameter set.
func (s *Signer) Sign(base, kind, route string, bound url.Values, ttl time.Duration) (string, time.Time, error) {
	if base == "" {
		return "", time.Time{}, errors.New("no public URL: signed links need the HTTP transport (set SPARK_MCP_PUBLIC_URL behind a proxy)")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	exp := s.now().Add(ttl).Truncate(time.Second)
	sig, err := s.mac(kind, subject(route, bound), exp.Unix())
	if err != nil {
		return "", time.Time{}, err
	}
	q := url.Values{}
	for k, v := range bound {
		q[k] = v
	}
	q.Set("exp", strconv.FormatInt(exp.Unix(), 10))
	q.Set("sig", sig)
	return strings.TrimRight(base, "/") + route + "?" + q.Encode(), exp, nil
}

// Verify checks kind, route, bound parameters, expiry and signature.
func (s *Signer) Verify(kind, route string, bound url.Values, expStr, sig string) bool {
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || s.now().Unix() > exp {
		return false
	}
	want, err := s.mac(kind, subject(route, bound), exp)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(sig))
}
