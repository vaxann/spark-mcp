// Package mcpserver exposes the spark CLI as MCP tools: the catalog printed by
// `spark tools` becomes the tool list, and the server adds attachment transfer
// (inline content, signed download links, upload pages) and the HTTP transport.
package mcpserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vaxann/spark-mcp/internal/links"
	"github.com/vaxann/spark-mcp/internal/oauth"
	"github.com/vaxann/spark-mcp/internal/sparkcli"
)

// Options configure the server.
type Options struct {
	Runner  *sparkcli.Runner
	Version string
	Log     *slog.Logger
	// LocalFiles allows the attach parameters of draft and comment to name
	// files on this machine. Only safe when the client runs on the same
	// machine (stdio); remote clients send bytes or use upload links.
	LocalFiles bool
	// Remote registers the link tools and HTTP file routes.
	Remote            bool
	AttachmentTimeout time.Duration
	MaxAttachment     int64
	MaxUpload         int64
	LinkTTL           time.Duration
	CatalogRefresh    time.Duration
	// Agent is the AI_AGENT fallback when a client sends no name.
	Agent  string
	Signer *links.Signer
	// PublicURL overrides the base URL derived from request headers.
	PublicURL string
}

// Server wraps an MCP server bound to the spark CLI.
type Server struct {
	opts Options
	run  *sparkcli.Runner
	mcp  *mcp.Server
	log  *slog.Logger

	catMu       sync.Mutex // serialises catalog reloads
	stateMu     sync.RWMutex
	catalog     *sparkcli.Catalog
	catalogRaw  []byte
	lastAttempt time.Time
	registered  []string
}

// Tools added by this server; the names cannot collide with Spark's.
const (
	toolAttachmentLink   = "attachment_link"
	toolUploadLink       = "attachment_upload_link"
	hostHeader           = "X-Spark-Mcp-Host"
	schemeHeader         = "X-Spark-Mcp-Scheme"
	failedCatalogBackoff = 5 * time.Second
)

// New loads the catalog (a failure is logged, not fatal: tools appear once
// Spark Desktop is reachable) and registers the tools.
func New(ctx context.Context, o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Agent == "" {
		o.Agent = "spark-mcp"
	}
	s := &Server{opts: o, run: o.Runner, log: o.Log}
	cat, raw, err := s.fetchCatalog(ctx, o.Agent)
	if err != nil {
		s.log.Warn("spark tool catalog unavailable; tools will be registered once Spark Desktop answers", "err", err)
	}
	instr := fallbackInstructions
	if cat != nil && cat.Instructions != "" {
		instr = cat.Instructions
	}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "spark-mcp", Title: "Spark", Version: o.Version}, &mcp.ServerOptions{
		Instructions: instr + "\n\n" + s.attachmentInstructions(),
		Logger:       o.Log,
	})
	s.lastAttempt = time.Now()
	if cat != nil {
		s.apply(cat, raw)
	}
	s.mcp.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" || method == "tools/call" {
				s.maybeRefresh(ctx, method == "tools/call" && !s.hasTool(req))
			}
			return next(ctx, method, req)
		}
	})
	return s
}

const fallbackInstructions = "You are connected to Spark, the user's email client, via MCP. Use the tools to read email, calendars, meeting transcripts, contacts and team data and, where the user granted write access in Spark Desktop, to draft, comment, triage and manage contacts. Call `accounts` first. If no tools are listed, Spark Desktop is not running or its CLI is not enabled: ask the user to start it."

func (s *Server) attachmentInstructions() string {
	b := strings.Builder{}
	b.WriteString("Attachments: `attachment` returns a file's content (images, text and PDFs inline, other files as a binary resource) up to ")
	b.WriteString(humanSize(s.opts.MaxAttachment))
	b.WriteString(". To attach files to a draft or post them as comments, pass `attachments` as [{name, content_base64}] to `draft` or `comment` for small files")
	if s.opts.LocalFiles {
		b.WriteString(", or `attach` with absolute paths on this computer")
	}
	b.WriteString(". Files that are only on the user's device should go through `" + toolUploadLink + "` (create the draft first).")
	if s.opts.Remote {
		b.WriteString(" `" + toolAttachmentLink + "` returns a short-lived HTTPS link that opens or saves an attachment in a browser; offer it when the user wants the file on their device or the file is too large to return inline. `" + toolUploadLink + "` returns a short-lived page where the user picks files that are attached to a draft or posted as comments on a thread.")
	}
	return b.String()
}

// MCP returns the underlying server.
func (s *Server) MCP() *mcp.Server { return s.mcp }

// Run serves on stdio until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// ---- catalog ----

func (s *Server) fetchCatalog(ctx context.Context, agent string) (*sparkcli.Catalog, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := s.run.Run(ctx, sparkcli.Call{Args: []string{"tools"}, Timeout: 15 * time.Second, Agent: agent})
	if err != nil {
		return nil, nil, err
	}
	cat, err := sparkcli.ParseCatalog(res.Stdout)
	if err != nil {
		return nil, nil, err
	}
	return cat, res.Stdout, nil
}

func (s *Server) hasTool(req mcp.Request) bool {
	p, ok := req.GetParams().(*mcp.CallToolParamsRaw)
	if !ok {
		return true
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return slices.Contains(s.registered, p.Name)
}

// maybeRefresh re-reads the catalog when it is stale (or missing, or a call
// names an unknown tool), so tools follow Spark Desktop's access settings.
func (s *Server) maybeRefresh(ctx context.Context, force bool) {
	s.stateMu.RLock()
	have, last := s.catalog != nil, s.lastAttempt
	s.stateMu.RUnlock()
	interval := s.opts.CatalogRefresh
	if !have || force {
		interval = failedCatalogBackoff
	}
	if interval <= 0 || time.Since(last) < interval {
		return
	}
	if !s.catMu.TryLock() {
		return
	}
	defer s.catMu.Unlock()
	s.stateMu.Lock()
	s.lastAttempt = time.Now()
	s.stateMu.Unlock()
	cat, raw, err := s.fetchCatalog(ctx, s.opts.Agent)
	if err != nil {
		s.log.Warn("refresh spark tool catalog", "err", err)
		return
	}
	s.apply(cat, raw)
}

// apply registers the catalog's tools, replacing changed ones and removing
// tools Spark no longer lists. The SDK notifies connected clients.
func (s *Server) apply(cat *sparkcli.Catalog, raw []byte) {
	s.stateMu.Lock()
	unchanged := bytes.Equal(raw, s.catalogRaw)
	s.catalog, s.catalogRaw = cat, raw
	old := s.registered
	s.stateMu.Unlock()
	if unchanged {
		return
	}
	var names []string
	byName := map[string]bool{}
	for _, t := range cat.Tools {
		t := t
		tool := &mcp.Tool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: s.inputSchema(&t),
			Annotations: annotationsFor(&t),
		}
		s.mcp.AddTool(tool, s.catalogHandler(t))
		names = append(names, t.Name)
		byName[t.Name] = true
	}
	if s.opts.Remote && byName["attachment"] {
		s.addAttachmentLinkTool()
		names = append(names, toolAttachmentLink)
	}
	if byName["draft"] || byName["comment"] {
		if s.opts.Remote {
			s.addUploadLinkTool()
			names = append(names, toolUploadLink)
		}
	}
	var gone []string
	for _, n := range old {
		if !slices.Contains(names, n) {
			gone = append(gone, n)
		}
	}
	if len(gone) > 0 {
		s.mcp.RemoveTools(gone...)
	}
	s.stateMu.Lock()
	s.registered = names
	s.stateMu.Unlock()
	s.log.Info("spark tools registered", "count", len(names), "removed", len(gone))
}

// Catalog returns the current catalog (nil until Spark answered once).
func (s *Server) Catalog() *sparkcli.Catalog {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.catalog
}

func (s *Server) tool(name string) (sparkcli.Tool, bool) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.catalog != nil {
		for _, t := range s.catalog.Tools {
			if t.Name == name {
				return t, true
			}
		}
	}
	return sparkcli.Tool{}, false
}

// readOnlyTools mirrors the official extension's fallback: every tool must
// carry readOnlyHint or destructiveHint, and unknown tools are assumed to
// change data.
var readOnlyTools = map[string]bool{
	"accounts": true, "folders": true, "emails": true, "search": true, "thread": true, "attachment": true,
	"events": true, "availability": true, "contacts": true, "team": true, "meetings": true, "meeting": true,
	"templates": true, "template": true,
}

func annotationsFor(t *sparkcli.Tool) *mcp.ToolAnnotations {
	f, tr := false, true
	a := &mcp.ToolAnnotations{Title: t.Title, OpenWorldHint: &f}
	if readOnlyTools[t.Name] {
		a.ReadOnlyHint = true
	} else {
		a.DestructiveHint = &tr
	}
	if len(t.Annotations) > 0 {
		_ = json.Unmarshal(t.Annotations, a)
	}
	return a
}

// ---- results ----

func textResult(text string) *mcp.CallToolResult {
	if text == "" {
		text = "(no output)"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(err error) *mcp.CallToolResult {
	var se *sparkcli.Error
	if !errors.As(err, &se) {
		se = sparkcli.E(sparkcli.CodeInternal, "%s", err.Error())
	}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: "Error (" + se.Code + "): " + se.Message}},
		StructuredContent: map[string]any{"code": se.Code, "message": se.Message},
	}
}

// agentOf names the calling client for Spark's audit log.
func (s *Server) agentOf(req *mcp.CallToolRequest) string {
	if req != nil && req.Session != nil {
		if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil {
			if a := sparkcli.SanitizeAgent(p.ClientInfo.Name); a != "" {
				return a
			}
		}
	}
	return s.opts.Agent
}

// publicBase is the externally visible base URL for signed links.
func (s *Server) publicBase(req *mcp.CallToolRequest) string {
	if s.opts.PublicURL != "" {
		return strings.TrimRight(s.opts.PublicURL, "/")
	}
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ""
	}
	return baseFromHeaders(req.Extra.Header, req.Extra.Header.Get(hostHeader), req.Extra.Header.Get(schemeHeader))
}

func baseFromHeaders(h http.Header, host, scheme string) string {
	if scheme == "" {
		scheme = "http"
	}
	if p := h.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.ToLower(strings.TrimSpace(strings.Split(p, ",")[0]))
	}
	if fh := h.Get("X-Forwarded-Host"); fh != "" {
		host = strings.TrimSpace(strings.Split(fh, ",")[0])
	}
	if host == "" || (scheme != "http" && scheme != "https") {
		return ""
	}
	return scheme + "://" + host
}

// ---- HTTP transport ----

// HTTPOptions configure the streamable HTTP transport.
type HTTPOptions struct {
	Listen  string // host:port
	Token   string // bearer token; required unless Listen is loopback
	TLSCert string
	TLSKey  string
	// OAuth, when non-nil, adds the embedded authorization server so apps
	// that only support OAuth (the Claude apps) can sign in with a password.
	OAuth *oauth.Server
}

// IsLoopback reports whether a listen address binds only to localhost.
func IsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Handler builds the HTTP handler: /mcp (bearer or OAuth protected), /healthz
// (unauthenticated), the OAuth endpoints and the attachment routes.
func (s *Server) Handler(o HTTPOptions) http.Handler {
	// Inline base64 attachments travel inside one JSON-RPC request.
	bodyLimit := s.opts.MaxUpload/3*4*2 + (1 << 20)
	if bodyLimit < mcp.DefaultMaxRequestBodyBytes {
		bodyLimit = mcp.DefaultMaxRequestBodyBytes
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp },
		&mcp.StreamableHTTPOptions{Logger: s.log, DisableLocalhostProtection: o.Token != "" || o.OAuth != nil, MaxRequestBodyBytes: bodyLimit})
	// Tool handlers only see request headers: pass the Host and scheme on so
	// signed links can be built without configuration behind a tunnel.
	withHost := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(hostHeader, r.Host)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		r.Header.Set(schemeHeader, scheme)
		mcpHandler.ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	var protected http.Handler
	var authed func(*http.Request) bool
	if o.OAuth != nil {
		o.OAuth.Mount(mux)
		protected = o.OAuth.Middleware(withHost)
		authed = func(r *http.Request) bool {
			tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			return ok && o.OAuth.Valid(strings.TrimSpace(tok))
		}
	} else {
		protected = bearerAuth(o.Token, withHost)
		authed = func(r *http.Request) bool {
			if o.Token == "" {
				return true // loopback without a token, enforced by RunHTTP
			}
			tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			return ok && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(tok)), []byte(o.Token)) == 1
		}
	}
	mux.Handle("/mcp", protected)
	mux.Handle("/mcp/", protected)
	s.fileRoutes(mux, authed)
	return mux
}

func bearerAuth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="spark-mcp"`)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RunHTTP serves the streamable HTTP transport until ctx is done. A bearer
// token is mandatory unless the listen address is loopback.
func (s *Server) RunHTTP(ctx context.Context, o HTTPOptions) error {
	if o.Token == "" && !IsLoopback(o.Listen) {
		return fmt.Errorf("refusing to listen on %s without a token: set SPARK_MCP_HTTP_TOKEN or bind to 127.0.0.1", o.Listen)
	}
	if o.Token == "" {
		s.log.Warn("HTTP transport without a token: only safe because it is bound to loopback", "listen", o.Listen)
	}
	srv := &http.Server{Addr: o.Listen, Handler: s.Handler(o), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if o.TLSCert != "" || o.TLSKey != "" {
			s.log.Info("serving MCP over HTTPS", "listen", o.Listen, "endpoint", "/mcp")
			errCh <- srv.ListenAndServeTLS(o.TLSCert, o.TLSKey)
			return
		}
		s.log.Info("serving MCP over HTTP", "listen", o.Listen, "endpoint", "/mcp")
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func jsonEncode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
