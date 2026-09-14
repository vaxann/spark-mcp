package mcpserver

import (
	"bufio"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vaxann/spark-mcp/internal/links"
	"github.com/vaxann/spark-mcp/internal/sparkcli"
)

// Downloads may be large and the client slow; the CLI call must outlive them.
const streamTimeout = 30 * time.Minute

// fileRoutes mounts the attachment endpoints. authed reports whether the
// request carries a valid bearer/OAuth token; signed links work without one.
//
//	GET  /attachments/{id}                      download (token or signed link)
//	GET  /upload/{draft|comment}/{id}           upload page (signed link)
//	POST /upload/{draft|comment}/{id}           upload form target (token or signed link)
//	PUT  /drafts/{id}/attachments/{name}        raw upload onto a draft (token)
//	POST /comments/{id}/attachments/{name}      raw upload as a comment (token)
func (s *Server) fileRoutes(mux *http.ServeMux, authed func(*http.Request) bool) {
	signed := func(r *http.Request, kind string, bound url.Values) bool {
		q := r.URL.Query()
		return s.opts.Signer != nil && s.opts.Signer.Verify(kind, r.URL.Path, bound, q.Get("exp"), q.Get("sig"))
	}
	mux.HandleFunc("GET /attachments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) && !signed(r, links.KindDownload, nil) {
			http.Error(w, "forbidden: sign in or use a valid signed link", http.StatusForbidden)
			return
		}
		s.download(w, r, r.PathValue("id"))
	})
	mux.HandleFunc("GET /upload/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		kind, bound, ok := uploadTarget(r)
		if !ok || !signed(r, links.KindUpload, bound) {
			http.Error(w, "this upload link is invalid or expired", http.StatusForbidden)
			return
		}
		s.renderUpload(w, r, kind, "", "")
	})
	mux.HandleFunc("POST /upload/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		kind, bound, ok := uploadTarget(r)
		if !ok || (!signed(r, links.KindUpload, bound) && !authed(r)) {
			http.Error(w, "forbidden: this upload link is invalid or expired", http.StatusForbidden)
			return
		}
		s.uploadForm(w, r, kind)
	})
	mux.HandleFunc("PUT /drafts/{id}/attachments/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.rawUpload(w, r, "draft", nil)
	})
	mux.HandleFunc("POST /comments/{id}/attachments/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var extra []string
		if team := r.URL.Query().Get("team"); team != "" {
			extra = []string{"--team=" + team}
		}
		s.rawUpload(w, r, "comment", extra)
	})
}

func uploadTarget(r *http.Request) (kind string, bound url.Values, ok bool) {
	kind = r.PathValue("kind")
	if (kind != "draft" && kind != "comment") || !sparkcli.ValidAttachmentID(r.PathValue("id")) {
		return "", nil, false
	}
	if team := r.URL.Query().Get("team"); kind == "comment" && team != "" {
		bound = url.Values{"team": {team}}
	}
	return kind, bound, true
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, id string) {
	call := sparkcli.Call{Agent: s.opts.Agent, Timeout: s.opts.AttachmentTimeout}
	info, err := s.run.StatAttachment(r.Context(), id, call)
	if err != nil {
		httpErr(w, err)
		return
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		call.Timeout = streamTimeout
		err := s.run.StreamAttachment(r.Context(), id, pw, call)
		_ = pw.CloseWithError(err)
		done <- err
	}()
	br := bufio.NewReaderSize(pr, 64<<10)
	head, _ := br.Peek(512)
	if len(head) == 0 {
		// Nothing arrived: report the CLI error instead of an empty file.
		if err := <-done; err != nil {
			httpErr(w, err)
			return
		}
	}
	mime := sparkcli.SniffMIME(head)
	if mime == "" {
		mime = info.MediaType
	}
	disp := "inline"
	if r.URL.Query().Get("download") == "1" {
		disp = "attachment"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename*=UTF-8''%s`, disp, url.PathEscape(sparkcli.SafeFileName(info.Name))))
	w.Header().Set("Cache-Control", "private, no-store")
	// Attachments are untrusted content: never let a browser execute them.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if _, err := io.Copy(w, br); err != nil {
		s.log.Warn("attachment download interrupted", "err", err)
	}
	_ = pr.Close() // unblocks the CLI writer if the client went away
	<-done
}

// rawUpload streams one request body onto a draft or as a comment.
func (s *Server) rawUpload(w http.ResponseWriter, r *http.Request, kind string, extra []string) {
	id := r.PathValue("id")
	if !sparkcli.ValidAttachmentID(id) {
		httpErr(w, sparkcli.E(sparkcli.CodeInvalidArgument, "id must be numeric"))
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.opts.MaxUpload+1))
	if err != nil || int64(len(data)) > s.opts.MaxUpload {
		httpErr(w, sparkcli.E(sparkcli.CodeTooLarge, "upload exceeds the %s limit", humanSize(s.opts.MaxUpload)))
		return
	}
	res, err := s.run.AttachStream(r.Context(), kind, id, r.PathValue("name"), data, extra, sparkcli.Call{Agent: s.opts.Agent})
	if err != nil {
		httpErr(w, err)
		return
	}
	writeJSONResp(w, http.StatusCreated, map[string]string{"target": kind + " " + id, "name": sparkcli.SafeFileName(r.PathValue("name")), "link": sparkcli.ParseLink(res.Text())})
}

func (s *Server) uploadForm(w http.ResponseWriter, r *http.Request, kind string) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, 10*s.opts.MaxUpload+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil { //nolint:gosec // body is capped by MaxBytesReader above
		s.renderUpload(w, r, kind, "Upload too large or malformed.", "err")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	var extra []string
	if team := r.URL.Query().Get("team"); team != "" {
		extra = []string{"--team=" + team}
	}
	var done []string
	deepLink := ""
	for _, fh := range r.MultipartForm.File["file"] {
		name := sparkcli.SafeFileName(fh.Filename)
		if fh.Size > s.opts.MaxUpload {
			s.renderUpload(w, r, kind, fmt.Sprintf("%s is larger than %s.%s", name, humanSize(s.opts.MaxUpload), doneSuffix(done)), "err")
			return
		}
		f, err := fh.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, s.opts.MaxUpload+1))
		_ = f.Close()
		if err != nil {
			s.renderUpload(w, r, kind, "Could not read "+name+"."+doneSuffix(done), "err")
			return
		}
		res, err := s.run.AttachStream(r.Context(), kind, id, name, data, extra, sparkcli.Call{Agent: s.opts.Agent})
		if err != nil {
			s.renderUpload(w, r, kind, "Failed to add "+name+": "+messageOf(err)+doneSuffix(done), "err")
			return
		}
		if l := sparkcli.ParseLink(res.Text()); l != "" {
			deepLink = l
		}
		done = append(done, name)
	}
	if len(done) == 0 {
		s.renderUpload(w, r, kind, "No file received.", "err")
		return
	}
	msg := "Attached to draft " + id + ": " + strings.Join(done, ", ")
	if kind == "comment" {
		msg = "Posted to the thread: " + strings.Join(done, ", ")
	}
	s.renderUploadLink(w, r, kind, msg, "ok", deepLink)
}

func doneSuffix(done []string) string {
	if len(done) == 0 {
		return ""
	}
	return " Already added: " + strings.Join(done, ", ") + "."
}

var uploadTmpl = template.Must(template.New("upload").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Upload · spark-mcp</title>
<style>
body{font-family:system-ui,sans-serif;background:#111;color:#eee;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0}
form{background:#1c1c1c;padding:2rem;border-radius:12px;width:min(420px,92vw);box-shadow:0 8px 30px #0008}
h1{font-size:1.1rem;margin:0 0 .5rem}p{color:#aaa;font-size:.9rem;margin:.25rem 0 1rem;word-break:break-word}
input[type=file]{width:100%;color:#ccc}button{margin-top:1rem;width:100%;padding:.7rem;border:0;border-radius:8px;background:#4f7cff;color:#fff;font-size:1rem;cursor:pointer}
a{color:#8fb0ff}.ok{color:#7bd88f}.err{color:#ff6b6b}
</style></head><body>
<form method="post" action="{{.Action}}" enctype="multipart/form-data">
<h1>{{.Heading}}</h1>
<p>{{.Detail}}<br>Up to {{.Limit}} per file. Link valid until {{.Expires}}.</p>
<input type="file" name="file" required multiple>
<button type="submit">Upload</button>
{{if .Message}}<p class="{{.Class}}">{{.Message}}</p>{{end}}
{{if .DeepLink}}<p><a href="{{.DeepLink}}">Open in Spark</a></p>{{end}}
</form></body></html>`))

type uploadView struct {
	Action, Heading, Detail, Limit, Expires, Message, Class string
	DeepLink                                                template.URL
}

func (s *Server) renderUpload(w http.ResponseWriter, r *http.Request, kind, msg, class string) {
	s.renderUploadLink(w, r, kind, msg, class, "")
}

func (s *Server) renderUploadLink(w http.ResponseWriter, r *http.Request, kind, msg, class, deepLink string) {
	id := r.PathValue("id")
	v := uploadView{Action: r.URL.RequestURI(), Limit: humanSize(s.opts.MaxUpload), Message: msg, Class: class}
	if kind == "draft" {
		v.Heading, v.Detail = "Attach files to a Spark draft", "Files are added to draft "+id+"."
	} else {
		v.Heading, v.Detail = "Send files to a Spark thread", "Each file is posted as a team comment on the thread of message "+id+"."
	}
	if n, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64); err == nil {
		v.Expires = time.Unix(n, 0).UTC().Format(time.RFC1123)
	}
	// Only Spark's own deep links are rendered as links.
	if strings.HasPrefix(deepLink, "https://sparkmailapp.com/") {
		v.DeepLink = template.URL(deepLink) //nolint:gosec // prefix-checked Spark deep link
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	if class == "err" {
		w.WriteHeader(http.StatusBadRequest)
	}
	_ = uploadTmpl.Execute(w, v) //nolint:gosec // html/template escapes every field
}

func statusOf(err error) int {
	var se *sparkcli.Error
	if errors.As(err, &se) {
		switch se.Code {
		case sparkcli.CodeInvalidArgument:
			return http.StatusBadRequest
		case sparkcli.CodeTooLarge:
			return http.StatusRequestEntityTooLarge
		case sparkcli.CodeUnavailable:
			return http.StatusServiceUnavailable
		case sparkcli.CodeTimeout:
			return http.StatusGatewayTimeout
		case sparkcli.CodeCLI:
			return http.StatusUnprocessableEntity
		}
	}
	return http.StatusInternalServerError
}

// httpErr writes an error as JSON (never HTML, so no markup is reflected).
func httpErr(w http.ResponseWriter, err error) {
	var se *sparkcli.Error
	if !errors.As(err, &se) {
		se = sparkcli.E(sparkcli.CodeInternal, "%s", err.Error())
	}
	writeJSONResp(w, statusOf(err), map[string]string{"code": se.Code, "message": se.Message})
}

func writeJSONResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = jsonEncode(w, v)
}
