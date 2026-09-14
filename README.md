# Spark MCP

An [MCP](https://modelcontextprotocol.io) server, written in Go, that gives AI assistants access to [Spark](https://sparkmailapp.com) — email, calendars, meeting transcripts, contacts and teams — through the `spark` CLI that ships with Spark Desktop. Use it from Claude Code or Claude Desktop on the same Mac, or expose it over HTTPS with a bearer token and OAuth sign-in so the Claude apps (web, iOS, Android) and agents on other machines can reach your mailbox.

> **Status:** first implementation complete and covered by tests; not yet released. Requirements live in [`openspec/`](openspec/). Not affiliated with Spark Mail Limited.

## What it does

- **Every Spark CLI capability as MCP tools** — the tool list is read from `spark tools`, the catalog Spark itself publishes, so tool names, parameters and descriptions always match the installed Spark version and follow the access levels you set in *Spark Desktop → Settings → AI Agents* (read-only, triage, send). Tools disappear and reappear as you change them, without restarting the server.
- **Attachments both ways** — read an attachment as inline content (images, text, PDFs as resources); get a short-lived signed HTTPS link to open or save it on your phone; attach files to drafts and post them as comments, either inline (base64) or through a short-lived upload page you open on any device.
- **Runs where Spark runs, reachable from anywhere** — stdio for local clients, or one long-running instance (LaunchAgent) over streamable HTTP protected by a bearer token, with built-in OAuth 2.1 sign-in for the Claude apps' custom connectors. Put a Cloudflare Tunnel or any TLS reverse proxy in front of it.
- **Safe by default** — no shell: the CLI is executed with an argument vector, values can never turn into options; remote clients cannot make the server read local files; timeouts, output caps and a limit on concurrent CLI processes; logs carry tool names, durations and error codes, never message content, recipients or tokens.

## Architecture at a glance

```
MCP client (Claude app, Claude Code, agent)
        │  stdio, or HTTPS /mcp + bearer token / OAuth
        ▼
┌──────────────────────────────────────┐
│  spark-mcp (Go, on the Mac)          │
│  ├─ stdio | HTTP + bearer + OAuth    │
│  ├─ tools from `spark tools`         │
│  ├─ attachments: inline, links, pages│
│  └─ runner: exec spark, limits       │
└──────────────────┬───────────────────┘
                   ▼  local IPC
            Spark Desktop app
```

## Requirements

- macOS with Spark Desktop running and signed in.
- The Spark CLI enabled: *Spark Desktop → Settings → AI Agents → Spark CLI Setup* (installs `/usr/local/bin/spark`), and access levels chosen per account under *Spark CLI Access*.
- Go 1.26+ to build.

## Quick start

```bash
go install github.com/vaxann/spark-mcp/cmd/spark-mcp@latest
spark-mcp -check                          # reaches Spark Desktop, prints the tool catalog
claude mcp add spark -- spark-mcp         # Claude Code on the same Mac (stdio)
```

For a permanent instance reachable from the Claude apps, see [docs/clients.md](docs/clients.md): LaunchAgent, bearer token, OAuth connector, Cloudflare Tunnel.

## Tools

Tools come from Spark's catalog and keep Spark's names: `accounts`, `folders`, `emails`, `search`, `thread`, `attachment`, `events`, `event`, `availability`, `contacts`, `team`, `meetings`, `meeting`, `templates`, `template`, `draft`, `comment`, `action`, `contact-action` (the exact set depends on your Spark version and access levels).

The server adds:

| Tool / parameter | Use |
|---|---|
| `draft.attachments`, `comment.attachments` | Attach files given inline as `[{name, content_base64}]` (each up to 25 MB). |
| `draft.attachment_ids` | Copy attachments of received emails onto a draft. |
| `draft.attach`, `comment.attach` | Local file paths — stdio only. |
| `attachment_link` | Short-lived signed HTTPS link that opens or downloads an attachment in any browser (HTTP transport). |
| `attachment_upload_link` | Short-lived upload page that attaches the files you pick to a draft, or posts them as comments on a thread (HTTP transport). |

## Configuration

| Env var | Purpose | Default |
|---|---|---|
| `SPARK_MCP_BIN` | Path of the spark CLI | `/usr/local/bin/spark` |
| `SPARK_MCP_HTTP_LISTEN` | Serve HTTP at `/mcp` instead of stdio, e.g. `127.0.0.1:8766` | stdio |
| `SPARK_MCP_HTTP_TOKEN` | Bearer token (mandatory off-loopback) | — |
| `SPARK_MCP_OAUTH_PASSWORD` | Password of the OAuth sign-in page | the token |
| `SPARK_MCP_PUBLIC_URL` | Public HTTPS URL (OAuth issuer, signed links) | from request headers |

See [docs/configuration.md](docs/configuration.md) for every option and the error codes. Real configuration files and credentials are git-ignored and must never be committed.

## Development

```bash
make build      # bin/spark-mcp
make race       # go test -race ./...  (uses the fake CLI in testdata/)
make lint       # golangci-lint
make vuln       # govulncheck
```

Workflow: every feature starts as an OpenSpec change (`openspec/changes/<name>/`) with a proposal, delta specs, design and tasks; once implemented it is archived into `openspec/specs/`.

## Credits and privacy

The catalog-driven tool mapping and the attachment handling follow the official [Spark for Claude](https://github.com/readdle/spark-claude-extension) extension by Spark Mail Limited (MIT); see [NOTICE](NOTICE). This repository is public and contains no mailbox data: tests run against a fake CLI with synthetic fixtures.

## License

[MIT](LICENSE)
