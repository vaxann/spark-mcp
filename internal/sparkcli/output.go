package sparkcli

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// AttachmentInfo is the metadata printed by `spark attachment <id>`.
type AttachmentInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
	MessageID string `json:"message_id"`
}

// ParseKeyValues reads "Key: value" lines, splitting on the first ": ".
func ParseKeyValues(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if i := strings.Index(line, ": "); i > 0 {
			m[strings.TrimSpace(line[:i])] = line[i+2:]
		}
	}
	return m
}

var digitsRE = regexp.MustCompile(`^\d+$`)

// ValidAttachmentID reports whether id is a positive decimal attachment pk.
func ValidAttachmentID(id string) bool { return digitsRE.MatchString(id) && id != "0" }

// StatAttachment fetches metadata; it also makes Spark download the file, so a
// following --stream call finds it on disk.
func (r *Runner) StatAttachment(ctx context.Context, id string, c Call) (*AttachmentInfo, error) {
	if !ValidAttachmentID(id) {
		return nil, E(CodeInvalidArgument, "attachment id must be a positive integer, got %q", id)
	}
	c.Args = []string{"attachment", "--", id}
	res, err := r.Run(ctx, c)
	if err != nil {
		return nil, err
	}
	out := strings.TrimSpace(string(res.Stdout))
	if strings.HasPrefix(out, "Error:") {
		return nil, cliError(out, "", 1)
	}
	kv := ParseKeyValues(out)
	for _, k := range []string{"ID", "Name", "Size", "MIME Type", "Message ID"} {
		if _, ok := kv[k]; !ok {
			return nil, E(CodeCLI, "attachment %s: unexpected metadata output (missing %s)", id, k)
		}
	}
	size, err := strconv.ParseInt(strings.TrimSpace(kv["Size"]), 10, 64)
	if err != nil || size < 0 {
		return nil, E(CodeCLI, "attachment %s: invalid size %q", id, kv["Size"])
	}
	return &AttachmentInfo{ID: id, Name: kv["Name"], Size: size, MediaType: NormalizeMIME(kv["MIME Type"]), MessageID: strings.TrimSpace(kv["Message ID"])}, nil
}

// ReadAttachment returns the attachment bytes, refusing more than max.
func (r *Runner) ReadAttachment(ctx context.Context, id string, max int64, c Call) ([]byte, error) {
	c.Args = []string{"attachment", "--stream", "--", id}
	c.MaxBytes = max
	res, err := r.Run(ctx, c)
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// StreamAttachment copies the attachment bytes to w.
func (r *Runner) StreamAttachment(ctx context.Context, id string, w io.Writer, c Call) error {
	c.Args = []string{"attachment", "--stream", "--", id}
	c.Stdout = w
	_, err := r.Run(ctx, c)
	return err
}

// AttachStream adds one file to an existing draft (`draft --edit`) or posts it
// as a comment message on a thread (`comment <message-id>`), piping the bytes
// through stdin. extra carries flags such as --team for comments.
func (r *Runner) AttachStream(ctx context.Context, kind, target, name string, data []byte, extra []string, c Call) (*Result, error) {
	name = SafeFileName(name)
	switch kind {
	case "draft":
		// No sharing flags: Spark rejects a content edit combined with them.
		c.Args = []string{"draft", "--edit=" + target, "--attach-stream=" + name}
	case "comment":
		c.Args = append([]string{"comment", "--attach-stream=" + name}, extra...)
		c.Args = append(c.Args, "--", target)
	default:
		return nil, E(CodeInvalidArgument, "unknown attachment target %q", kind)
	}
	c.Stdin = data
	return r.Run(ctx, c)
}

var (
	draftIDRE = regexp.MustCompile(`(?m)^\s*ID:\s*(\d+)\s*$`)
	linkRE    = regexp.MustCompile(`(?m)^\s*Link:\s*(\S+)\s*$`)
	unsafeRE  = regexp.MustCompile(`[\x00-\x1f\x7f/\\]+`)
)

// ParseDraftID extracts the "ID: <n>" line printed for a draft.
func ParseDraftID(out string) string {
	if m := draftIDRE.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// ParseLink extracts the "Link: <url>" deep link.
func ParseLink(out string) string {
	if m := linkRE.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// SafeFileName strips path separators and control characters from a name
// shown to recipients.
func SafeFileName(name string) string {
	name = strings.TrimSpace(unsafeRE.ReplaceAllString(name, "_"))
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = "attachment"
	}
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	return name
}

var mimeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// NormalizeMIME lowercases, drops parameters and rejects malformed types.
func NormalizeMIME(raw string) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
	if mimeRE.MatchString(base) {
		return base
	}
	return "application/octet-stream"
}

// SniffMIME recognises the formats whose declared type is often wrong.
func SniffMIME(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("%PDF")):
		return "application/pdf"
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF8")):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
