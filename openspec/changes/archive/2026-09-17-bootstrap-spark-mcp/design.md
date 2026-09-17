## Context

- `spark` is a thin client: every command talks over IPC to the Spark Desktop app in the user's GUI session. The server must run on that Mac, in that session.
- `spark tools` prints a JSON catalog (`instructions`, and per tool `name`, `title`, `command`, `description`, `parameters` with `type`, `required`, optional `flag`, `items`). Parameters without a flag are positional. The catalog only contains tools the configured access levels allow.
- CLI output is human-readable text with `ID:` and `Link:` lines that models understand; errors are `Error: …` on stdout or stderr with a non-zero exit.
- The official extension (Node, MIT) established the reference behaviour: dynamic catalog, `--hide-attachment-paths` for threads, attachment content blocks, and streaming files into drafts and comments with `--attach-stream`.
- The server reuses the knowledge-base MCP server's HTTP layer: bearer middleware, embedded OAuth, health check, signed links.

## Goals / Non-Goals

**Goals:** identical capabilities to the local extension for remote clients; attachments usable from a phone; zero drift from Spark's tool definitions; the security posture of an internet-facing service.

**Non-Goals:** duplicating Spark's access control; structured parsing of CLI output; running anywhere but the Mac with Spark Desktop.

## Decisions

### D1. Go, official MCP SDK, same layout as the knowledge-base server
Static binary, fast start for stdio launches, shared code and conventions (`addTool` style handlers, OAuth package, config precedence, error codes). *Alternative:* extend the Node extension with an HTTP transport — rejected: OAuth, signed links and upload pages would be written from scratch in a codebase the maintainer does not own.

### D2. Tools are the Spark catalog, names unchanged
Tools are registered with Spark's names, descriptions and a JSON Schema built from the catalog, so Spark's skills and recipes apply unchanged and new CLI features appear without a release. Annotations follow the extension: known read tools are read-only, everything else destructive. Server-added tools use names Spark does not (`attachment_link`, `attachment_upload_link`). *Alternative:* hand-written typed tools — rejected: drift with every Spark release and a second source of truth.

### D3. Catalog refresh on use
The catalog is read at startup (its `instructions` become the server instructions) and re-read on `tools/list` or `tools/call` when older than `catalog_refresh` (60 s), after 5 s when missing, or immediately when a call names an unknown tool. Changes are applied with the SDK's add/remove, which sends `notifications/tools/list_changed`. No background polling while idle. If Spark is unreachable at startup the server still starts with fallback instructions and an empty tool list.

### D4. Argument vector: `command`, `--flag=value`…, `--`, positionals
Flags use the equals form and positionals follow a `--` terminator, so any value (including one beginning with `-`) stays a value. Booleans emit the bare flag when true; arrays repeat the flag; empty strings and arrays are omitted; unknown arguments and type mismatches fail with `invalid_argument` before the CLI runs. The binary is executed directly, never through a shell.

### D5. Access control is Spark's
No server-side ceiling. The maintainer configures read-only/triage/send in Spark Desktop; the catalog and the CLI's own checks enforce it. The server's responsibility is authentication.

### D6. Attachments
- *Inline read:* `attachment` stats the attachment (which also triggers the download), refuses sizes above `max_attachment`, then reads `attachment --stream`. Content type is sniffed from magic bytes before trusting the declared MIME; PNG/JPEG/GIF/WebP become image blocks, audio audio blocks, text types text, everything else an embedded binary resource.
- *Inline write:* `draft`/`comment` gain `attachments: [{name, content_base64}]`. A draft is created or edited first (its ID parsed from `ID:`), then each file is streamed with `draft --edit=<id> --attach-stream=<name>`; for comments the text is posted first and each file becomes its own comment. `draft` also gains `attachment_ids` (`--attach-id`).
- *Local paths:* `attach` is honoured only over stdio, where the client already runs on the Mac. Over HTTP the parameter is removed from the schema and rejected, because it would let a remote caller mail out arbitrary local files.
- *Links:* HMAC-SHA256 over kind, path, bound query parameters and expiry; key in the state directory. Download links stream `attachment --stream` to the browser with `nosniff` and a sandboxing CSP. Upload links render a mobile-friendly page and stream each chosen file through `--attach-stream`. The public base URL comes from configuration or the request (`X-Forwarded-Proto`, `X-Forwarded-Host`, `Host`), passed to tool handlers through an internal header set by the HTTP layer.

### D7. Execution limits
A semaphore caps concurrent CLI processes (4). Each call has a timeout (60 s; 120 s for attachment work, 30 min for streaming downloads). Text output is capped (10 MB) and marked truncated; binary reads fail with `too_large`. `AI_AGENT` is set to the MCP client's name so Spark's audit log attributes calls.

### D8. Deployment on macOS
Binary via `go install` or release archive; LaunchAgent (not LaunchDaemon) for a permanent HTTP instance; loopback bind by default with a tunnel or reverse proxy in front; a token is mandatory for any other bind. No container image.

## Risks / Trade-offs

- **The Mac must be awake with Spark running.** Mitigation: clear `spark_unavailable` errors, empty tool list with instructions explaining why, automatic recovery on the next request.
- **Catalog format is undocumented.** Mitigation: strict validation with a clear error, a fake-CLI test suite, and the official extension as a reference for changes.
- **Signed links are bearer URLs.** Mitigation: short default lifetime, binding to one resource and kind, no listing endpoints.
- **Heuristic detection of "Spark not running" messages.** Mitigation: unknown failures still surface Spark's text as `cli_error`.
