package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vaxann/spark-mcp/internal/links"
	"github.com/vaxann/spark-mcp/internal/sparkcli"
)

// Arguments this server adds to Spark's draft and comment tools.
const (
	argAttachments   = "attachments"
	argAttachmentIDs = "attachment_ids"
	argAttach        = "attach"
)

// inputSchema is the catalog schema, adjusted for attachment transfer.
func (s *Server) inputSchema(t *sparkcli.Tool) map[string]any {
	schema := t.InputSchema()
	if t.Name != "draft" && t.Name != "comment" {
		return schema
	}
	props := schema["properties"].(map[string]any)
	if !s.opts.LocalFiles {
		// A remote client's paths mean nothing here, and honouring them would
		// let a caller mail out any file on this computer.
		delete(props, argAttach)
	} else if p, ok := props[argAttach].(map[string]any); ok {
		p["description"] = "Absolute paths of files on this computer to attach. The server reads them and streams the bytes to Spark."
	}
	props[argAttachments] = map[string]any{
		"type":        "array",
		"description": fmt.Sprintf("Files to attach, given inline (each at most %s). For files that exist only on the user's device use %s instead.", humanSize(s.opts.MaxUpload), toolUploadLink),
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":           map[string]any{"type": "string", "description": "File name shown to recipients, e.g. report.pdf"},
				"content_base64": map[string]any{"type": "string", "description": "File bytes, standard base64"},
			},
			"required": []string{"name", "content_base64"},
		},
	}
	if t.Name == "draft" {
		if _, ok := t.Param(argAttachmentIDs); !ok {
			props[argAttachmentIDs] = map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "IDs of attachments on existing emails (the ID column of the thread Attachments table) to copy onto this draft. Replies do not inherit attachments; forwards do.",
			}
		}
	}
	return schema
}

func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	in := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return in, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "arguments must be a JSON object: %s", err)
	}
	return in, nil
}

// catalogHandler runs one catalog tool.
func (s *Server) catalogHandler(t sparkcli.Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		res, err := s.dispatch(ctx, req, t)
		if err != nil {
			s.log.Info("tool failed", "tool", t.Name, "duration", time.Since(start).Round(time.Millisecond), "err_code", codeOf(err))
			return errorResult(err), nil
		}
		s.log.Info("tool", "tool", t.Name, "duration", time.Since(start).Round(time.Millisecond))
		return res, nil
	}
}

func codeOf(err error) string {
	if se, ok := err.(*sparkcli.Error); ok {
		return se.Code
	}
	return sparkcli.CodeInternal
}

func (s *Server) dispatch(ctx context.Context, req *mcp.CallToolRequest, t sparkcli.Tool) (*mcp.CallToolResult, error) {
	in, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, err
	}
	switch t.Name {
	case "attachment":
		return s.attachmentTool(ctx, req, in)
	case "draft":
		return s.draftTool(ctx, req, t, in)
	case "comment":
		return s.commentTool(ctx, req, t, in)
	}
	args, err := t.BuildArgs(in)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(0)
	if t.Name == "thread" {
		// Local attachment paths are useless to clients; the ID column and the
		// attachment tool cover everything.
		args = insertFlags(args, "--hide-attachment-paths")
		if b, _ := in["download_attachments"].(bool); b {
			timeout = s.opts.AttachmentTimeout
		}
	}
	res, err := s.run.Run(ctx, sparkcli.Call{Args: args, Agent: s.agentOf(req), Timeout: timeout})
	if err != nil {
		return nil, err
	}
	return textResult(res.Text()), nil
}

// insertFlags adds flags after the command and before the "--" terminator.
func insertFlags(args []string, flags ...string) []string {
	if len(flags) == 0 {
		return args
	}
	out := append([]string{}, args[:1]...)
	out = append(out, flags...)
	return append(out, args[1:]...)
}

// file is an attachment to upload.
type file struct {
	name string
	data []byte
}

// takeFiles removes the attachment arguments from in and returns the files to
// stream, the attach paths to forward unchanged, and attachment IDs.
func (s *Server) takeFiles(in map[string]any) (files []file, forward []string, ids []string, err error) {
	if raw, ok := in[argAttachments]; ok {
		delete(in, argAttachments)
		items, ok := raw.([]any)
		if !ok && raw != nil {
			return nil, nil, nil, sparkcli.E(sparkcli.CodeInvalidArgument, "attachments must be an array of {name, content_base64}")
		}
		for i, it := range items {
			m, ok := it.(map[string]any)
			name, _ := m["name"].(string)
			content, _ := m["content_base64"].(string)
			if !ok || strings.TrimSpace(name) == "" || content == "" {
				return nil, nil, nil, sparkcli.E(sparkcli.CodeInvalidArgument, "attachments[%d] needs name and content_base64", i)
			}
			data, derr := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(content), ""))
			if derr != nil {
				return nil, nil, nil, sparkcli.E(sparkcli.CodeInvalidArgument, "attachments[%d].content_base64 is not valid base64: %s", i, derr)
			}
			files = append(files, file{name: name, data: data})
		}
	}
	if raw, ok := in[argAttachmentIDs]; ok {
		delete(in, argAttachmentIDs)
		arr, _ := raw.([]any)
		for _, v := range arr {
			id := strings.TrimSpace(fmt.Sprint(v))
			if !sparkcli.ValidAttachmentID(id) {
				return nil, nil, nil, sparkcli.E(sparkcli.CodeInvalidArgument, "attachment_ids must be positive integers, got %q", id)
			}
			ids = append(ids, id)
		}
	}
	if raw, ok := in[argAttach]; ok {
		delete(in, argAttach)
		if !s.opts.LocalFiles {
			return nil, nil, nil, sparkcli.E(sparkcli.CodeInvalidArgument, "attach (local paths) is not available over a remote connection: use attachments or %s", toolUploadLink)
		}
		arr, _ := raw.([]any)
		for _, v := range arr {
			p, _ := v.(string)
			if p == "" {
				continue
			}
			data, rerr := readLocal(p, s.opts.MaxUpload)
			if rerr != nil {
				if se, ok := rerr.(*sparkcli.Error); ok && se.Code == sparkcli.CodeTooLarge {
					return nil, nil, nil, se
				}
				// Let Spark itself report paths the server cannot read.
				forward = append(forward, p)
				continue
			}
			files = append(files, file{name: filepath.Base(p), data: data})
		}
	}
	for _, f := range files {
		if int64(len(f.data)) > s.opts.MaxUpload {
			return nil, nil, nil, sparkcli.E(sparkcli.CodeTooLarge, "%s is %s, above the %s upload limit", sparkcli.SafeFileName(f.name), humanSize(int64(len(f.data))), humanSize(s.opts.MaxUpload))
		}
	}
	return files, forward, ids, nil
}

func readLocal(p string, max int64) ([]byte, error) {
	if !filepath.IsAbs(p) {
		return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "attach paths must be absolute")
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "cannot read %s", filepath.Base(p))
	}
	if info.Size() > max {
		return nil, sparkcli.E(sparkcli.CodeTooLarge, "%s is %s, above the %s upload limit", filepath.Base(p), humanSize(info.Size()), humanSize(max))
	}
	return os.ReadFile(p)
}

func pathArgs(flag string, values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, flag+"="+v)
	}
	return out
}

// draftTool creates or edits the draft, then streams each file onto it.
func (s *Server) draftTool(ctx context.Context, req *mcp.CallToolRequest, t sparkcli.Tool, in map[string]any) (*mcp.CallToolResult, error) {
	files, forward, ids, err := s.takeFiles(in)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		if b, _ := in["unshare"].(bool); b {
			return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "unshare cannot be combined with attachments: unshare first, then attach")
		}
		if d, _ := in["delete"].(string); d != "" {
			return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "delete cannot be combined with attachments")
		}
	}
	agent := s.agentOf(req)
	pk := ""
	if v, ok := in["edit"]; ok && v != nil {
		pk = strings.TrimSpace(fmt.Sprint(v))
	}
	contentKeys := 0
	for k := range in {
		if k != "edit" {
			contentKeys++
		}
	}
	needBase := len(files) == 0 || pk == "" || contentKeys > 0 || len(forward) > 0 || len(ids) > 0
	last := ""
	if needBase {
		args, err := t.BuildArgs(in)
		if err != nil {
			return nil, err
		}
		args = insertFlags(args, append(pathArgs("--attach", forward), pathArgs("--attach-id", ids)...)...)
		timeout := time.Duration(0)
		if len(ids) > 0 {
			timeout = s.opts.AttachmentTimeout
		}
		res, err := s.run.Run(ctx, sparkcli.Call{Args: args, Agent: agent, Timeout: timeout})
		if err != nil {
			return nil, err
		}
		last = res.Text()
		if len(files) == 0 {
			return textResult(last), nil
		}
		if pk == "" {
			if pk = sparkcli.ParseDraftID(last); pk == "" {
				return nil, sparkcli.E(sparkcli.CodeCLI, "the draft was saved but its ID could not be read, so the files were not attached:\n%s", last)
			}
		}
	}
	for i, f := range files {
		res, err := s.run.AttachStream(ctx, "draft", pk, f.name, f.data, nil, sparkcli.Call{Agent: agent})
		if err != nil {
			se, _ := err.(*sparkcli.Error)
			if se == nil {
				se = sparkcli.E(sparkcli.CodeInternal, "%s", err)
			}
			done := ""
			if i > 0 {
				done = fmt.Sprintf(" (%d of %d files were attached before the failure)", i, len(files))
			}
			return nil, &sparkcli.Error{Code: se.Code, Message: fmt.Sprintf("attaching %s to draft %s failed%s: %s", sparkcli.SafeFileName(f.name), pk, done, se.Message)}
		}
		last = res.Text()
	}
	return textResult(last), nil
}

// commentTool posts the text (if any), then each file as its own comment
// message, matching the CLI's one-file-per-message rule.
func (s *Server) commentTool(ctx context.Context, req *mcp.CallToolRequest, t sparkcli.Tool, in map[string]any) (*mcp.CallToolResult, error) {
	files, forward, ids, err := s.takeFiles(in)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "unknown argument %q for tool comment", argAttachmentIDs)
	}
	agent := s.agentOf(req)
	msgID := ""
	if v, ok := in["message_id"]; ok && v != nil {
		msgID = strings.TrimSpace(fmt.Sprint(v))
	}
	if len(files) > 0 && (in["edit"] != nil || msgID == "") {
		return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "attachments need message_id and cannot be combined with edit")
	}
	if len(files) == 0 {
		args, err := t.BuildArgs(in)
		if err != nil {
			return nil, err
		}
		res, err := s.run.Run(ctx, sparkcli.Call{Args: insertFlags(args, pathArgs("--attach", forward)...), Agent: agent})
		if err != nil {
			return nil, err
		}
		return textResult(res.Text()), nil
	}
	var share []string
	if team, _ := in["team"].(string); team != "" {
		share = append(share, "--team="+team)
	}
	if users, ok := in["user"].([]any); ok {
		for _, u := range users {
			if us, _ := u.(string); us != "" {
				share = append(share, "--user="+us)
			}
		}
	}
	var outputs []string
	failed := false
	if body, _ := in["body"].(string); body != "" || len(forward) > 0 {
		args, err := t.BuildArgs(in)
		if err != nil {
			return nil, err
		}
		res, err := s.run.Run(ctx, sparkcli.Call{Args: insertFlags(args, pathArgs("--attach", forward)...), Agent: agent})
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, res.Text())
	}
	for _, f := range files {
		res, err := s.run.AttachStream(ctx, "comment", msgID, f.name, f.data, share, sparkcli.Call{Agent: agent})
		if err != nil {
			failed = true
			outputs = append(outputs, fmt.Sprintf("Error attaching %s: %s", sparkcli.SafeFileName(f.name), messageOf(err)))
			continue
		}
		outputs = append(outputs, res.Text())
	}
	r := textResult(strings.Join(outputs, "\n\n"))
	r.IsError = failed
	return r, nil
}

func messageOf(err error) string {
	if se, ok := err.(*sparkcli.Error); ok {
		return se.Message
	}
	return err.Error()
}

// Images the Claude clients and API accept inline; others travel as blobs.
var inlineImages = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

var textLike = map[string]bool{"application/json": true, "application/xml": true, "application/x-ndjson": true, "application/javascript": true, "application/ecmascript": true}

// attachmentTool returns an attachment as MCP content.
func (s *Server) attachmentTool(ctx context.Context, req *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, error) {
	id := strings.TrimSpace(fmt.Sprint(in["id"]))
	if in["id"] == nil || !sparkcli.ValidAttachmentID(id) {
		return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "id must be a positive integer attachment ID from the thread Attachments table")
	}
	call := sparkcli.Call{Agent: s.agentOf(req), Timeout: s.opts.AttachmentTimeout}
	info, err := s.run.StatAttachment(ctx, id, call)
	if err != nil {
		return nil, err
	}
	if info.Size > s.opts.MaxAttachment {
		hint := "open it in Spark Desktop"
		if s.opts.Remote {
			hint = "use " + toolAttachmentLink + " to download it in a browser"
		}
		return nil, sparkcli.E(sparkcli.CodeTooLarge, "attachment %s (%s) is %s, above the %s inline limit; %s", id, info.Name, humanSize(info.Size), humanSize(s.opts.MaxAttachment), hint)
	}
	data, err := s.run.ReadAttachment(ctx, id, s.opts.MaxAttachment, call)
	if err != nil {
		return nil, err
	}
	mime := sparkcli.SniffMIME(data)
	if mime == "" {
		mime = info.MediaType
	}
	summary := fmt.Sprintf("Attachment: %s (%s, %s, message %s)", info.Name, humanSize(int64(len(data))), mime, info.MessageID)
	var block mcp.Content
	switch {
	case strings.HasPrefix(mime, "image/") && inlineImages[mime]:
		block = &mcp.ImageContent{Data: data, MIMEType: mime}
	case strings.HasPrefix(mime, "audio/"):
		block = &mcp.AudioContent{Data: data, MIMEType: mime}
	case strings.HasPrefix(mime, "text/") || textLike[mime]:
		block = &mcp.TextContent{Text: string(data)}
	default:
		if strings.HasPrefix(mime, "image/") {
			summary += ". This image format cannot be displayed inline; the raw file is attached as a resource."
		}
		summary += " The file is attached as a binary resource; if your client saved it to a local path, read the whole file from there."
		block = &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
			URI:      "spark-attachment://" + id + "/" + url.PathEscape(info.Name),
			MIMEType: mime,
			Blob:     data,
		}}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: summary}, block}}, nil
}

// ---- link tools (HTTP transport only) ----

type linkOut struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Name      string    `json:"name,omitempty"`
	Size      int64     `json:"size,omitempty"`
	MediaType string    `json:"media_type,omitempty"`
	Target    string    `json:"target,omitempty"`
}

func (s *Server) ttl(minutes int64) time.Duration {
	switch {
	case minutes <= 0:
		return s.opts.LinkTTL
	case minutes > 24*60:
		minutes = 24 * 60
	}
	return time.Duration(minutes) * time.Minute
}

// addServerTool registers a tool implemented by this server. The handler's
// value is returned as JSON text and structured content.
func (s *Server) addServerTool(t *mcp.Tool, h func(ctx context.Context, req *mcp.CallToolRequest, in map[string]any) (any, error)) {
	s.mcp.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in, err := decodeArgs(req.Params.Arguments)
		if err == nil {
			var out any
			if out, err = h(ctx, req, in); err == nil {
				raw, _ := json.Marshal(out)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: out}, nil
			}
		}
		s.log.Info("tool failed", "tool", t.Name, "err_code", codeOf(err))
		return errorResult(err), nil
	})
}

// argString reads an optional string (numbers are accepted for IDs).
func argString(in map[string]any, key string) string {
	switch v := in[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	}
	return ""
}

func argInt(in map[string]any, key string) int64 {
	switch v := in[key].(type) {
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		var n int64
		_, _ = fmt.Sscan(v, &n)
		return n
	}
	return 0
}

var ttlProp = map[string]any{"type": "integer", "description": "Link lifetime in minutes (default 15, at most 1440)"}

func (s *Server) addAttachmentLinkTool() {
	f := false
	s.addServerTool(&mcp.Tool{
		Name:        toolAttachmentLink,
		Title:       "Attachment Link",
		Description: "Create a short-lived HTTPS link that opens or downloads an email attachment in any browser without signing in. Give it to the user when they want to view or save a file on their phone or computer, or when the file is too large for the attachment tool.",
		Annotations: &mcp.ToolAnnotations{Title: "Attachment Link", ReadOnlyHint: true, OpenWorldHint: &f},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":          map[string]any{"type": []string{"integer", "string"}, "description": "Attachment ID from the thread Attachments table"},
				"ttl_minutes": ttlProp,
				"download":    map[string]any{"type": "boolean", "description": "Ask the browser to save the file instead of displaying it"},
			},
			"required": []string{"id"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in map[string]any) (any, error) {
		id := argString(in, "id")
		base := s.publicBase(req)
		if base == "" {
			return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "cannot build a public link: set SPARK_MCP_PUBLIC_URL")
		}
		info, err := s.run.StatAttachment(ctx, id, sparkcli.Call{Agent: s.agentOf(req), Timeout: s.opts.AttachmentTimeout})
		if err != nil {
			return nil, err
		}
		link, exp, err := s.opts.Signer.Sign(base, links.KindDownload, "/attachments/"+id, nil, s.ttl(argInt(in, "ttl_minutes")))
		if err != nil {
			return nil, sparkcli.E(sparkcli.CodeInternal, "%s", err)
		}
		if b, _ := in["download"].(bool); b {
			link += "&download=1"
		}
		return linkOut{URL: link, ExpiresAt: exp, Name: info.Name, Size: info.Size, MediaType: info.MediaType}, nil
	})
}

func (s *Server) addUploadLinkTool() {
	f, tr := false, true
	s.addServerTool(&mcp.Tool{
		Name:        toolUploadLink,
		Title:       "Attachment Upload Link",
		Description: "Create a short-lived HTTPS page where the user picks files on their phone or computer; the files are attached to a draft (draft_id) or posted as team comments on a thread (message_id). Pass exactly one of draft_id or message_id. Use it whenever the user wants to attach a file that you do not have.",
		Annotations: &mcp.ToolAnnotations{Title: "Attachment Upload Link", DestructiveHint: &tr, OpenWorldHint: &f},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"draft_id":    map[string]any{"type": []string{"string", "integer"}, "description": "Numeric ID of an existing draft to attach the uploaded files to (create the draft with the draft tool first)"},
				"message_id":  map[string]any{"type": []string{"string", "integer"}, "description": "Numeric ID of a message whose thread receives each uploaded file as a team comment"},
				"team":        map[string]any{"type": "string", "description": "Team name for comments when you belong to several teams"},
				"ttl_minutes": ttlProp,
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in map[string]any) (any, error) {
		draftID, msgID := argString(in, "draft_id"), argString(in, "message_id")
		kind, target := "", ""
		switch {
		case draftID != "" && msgID == "":
			kind, target = "draft", draftID
		case msgID != "" && draftID == "":
			kind, target = "comment", msgID
		default:
			return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "pass exactly one of draft_id or message_id")
		}
		if !sparkcli.ValidAttachmentID(target) {
			return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "the %s ID must be numeric", kind)
		}
		if _, ok := s.tool(kind); !ok {
			return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "Spark does not currently allow the %s tool (check the access level in Spark Desktop > Settings > AI Agents)", kind)
		}
		base := s.publicBase(req)
		if base == "" {
			return nil, sparkcli.E(sparkcli.CodeInvalidArgument, "cannot build a public link: set SPARK_MCP_PUBLIC_URL")
		}
		var bound url.Values
		if team := argString(in, "team"); kind == "comment" && team != "" {
			bound = url.Values{"team": {team}}
		}
		link, exp, err := s.opts.Signer.Sign(base, links.KindUpload, "/upload/"+kind+"/"+target, bound, s.ttl(argInt(in, "ttl_minutes")))
		if err != nil {
			return nil, sparkcli.E(sparkcli.CodeInternal, "%s", err)
		}
		return linkOut{URL: link, ExpiresAt: exp, Target: kind + " " + target}, nil
	})
}
