# Spark MCP

An [MCP](https://modelcontextprotocol.io) server, written in Go, that gives AI assistants access to [Spark](https://sparkmailapp.com) — email, calendars, meeting transcripts, contacts and teams — through the `spark` CLI that ships with Spark Desktop. Use it from Claude Code or Claude Desktop on the same Mac, or expose it over HTTPS with a bearer token and OAuth sign-in so the Claude apps (web, desktop, iOS, Android) and agents on other computers can reach your mailbox.

> Not affiliated with Spark Mail Limited. Requirements live in [`openspec/`](openspec/).

## What it does

- **Every Spark CLI capability as MCP tools** — the tool list is read from `spark tools`, the catalog Spark itself publishes, so tool names, parameters and descriptions always match the installed Spark version and follow the access levels you set in Spark Desktop (read-only, triage, send). Tools disappear and reappear as you change them, without restarting the server.
- **Attachments both ways** — read an attachment in the conversation; get a short-lived signed link to open or save it on your phone; attach files to drafts and post them as comments, inline or through a short-lived upload page you open on any device.
- **Runs where Spark runs, reachable from anywhere** — stdio for local clients, or a LaunchAgent serving streamable HTTP with a bearer token and built-in OAuth 2.1 sign-in for the Claude apps' custom connectors. Put a Cloudflare Tunnel or any TLS reverse proxy in front of it.
- **Safe by default** — no shell (the CLI gets an argument vector, values never turn into options); remote clients cannot make the server read local files; timeouts, output caps and a limit on concurrent CLI processes; logs carry tool names, durations and error codes, never message content, recipients or tokens.

```
Claude app / Claude Code / agent
        │  stdio, or HTTPS /mcp + bearer token / OAuth
        ▼
spark-mcp (Go, LaunchAgent on your Mac) ──exec──▶ spark CLI ──IPC──▶ Spark Desktop
```

## Installation

### 1. Prepare Spark (once)

1. Install [Spark Desktop](https://sparkmailapp.com) on your Mac and sign in to your accounts.
2. *Spark Desktop → Settings → AI Agents → Spark CLI Setup*: install the CLI (it creates `/usr/local/bin/spark`).
3. *Settings → AI Agents → Spark CLI Access*: choose per account what agents may do — **read-only**, **triage** (drafts, comments, archive, labels…) or **send** (sending mail, calendar invitations). This is the only permission switch; the server follows it.
4. Check: `spark --version` prints a version.

The server runs on this Mac: Spark's CLI talks to the app over local IPC, so a container or another computer cannot host it.

### 2. Install spark-mcp (one line)

**Local use only** (Claude Code or Claude Desktop on this Mac):

```bash
curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash
```

**Always-on service** (Claude apps on your phone, other computers):

```bash
curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash -s -- --service
```

The installer downloads the release binary for your CPU (verifying its SHA-256 checksum; it builds with `go install` if no release is available), checks that Spark answers, and with `--service` sets up everything below. Re-run the same command at any time to upgrade; secrets are kept.

| Created by the installer | Purpose |
|---|---|
| `~/.local/bin/spark-mcp` | The server binary |
| `~/Library/LaunchAgents/io.github.vaxann.spark-mcp.plist` | LaunchAgent (mode `0600`): starts at login, restarts on failure, holds the settings and secrets below |
| — `SPARK_MCP_HTTP_LISTEN` | `127.0.0.1:8766` (loopback only; publish it through a tunnel) |
| — `SPARK_MCP_HTTP_TOKEN` | Generated bearer token (64 hex characters) |
| — `SPARK_MCP_OAUTH_PASSWORD` | Generated password for the Claude apps' sign-in page |
| — `SPARK_MCP_PUBLIC_URL` | Your public HTTPS URL, set with `--public-url` |
| `~/Library/Application Support/spark-mcp/` | OAuth clients and token hashes, signing key of attachment links |
| `~/Library/Logs/spark-mcp/spark-mcp.log` | Server log |

Installer options: `--service`, `--public-url URL`, `--port N`, `--listen HOST:PORT`, `--rotate-secrets`, `--version vX.Y.Z`, `--from-source`, `--uninstall`, `--help`. Pass them after `bash -s --`.

If `~/.local/bin` is not on your `PATH`, the installer tells you how to add it.

### 3. Connect a client on the same Mac

```bash
claude mcp add spark -- ~/.local/bin/spark-mcp        # Claude Code
```

Claude Desktop (`~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{ "mcpServers": { "spark": { "command": "/Users/<you>/.local/bin/spark-mcp" } } }
```

Ask *"what's in my inbox today?"* to try it.

### 4. Publish the service over HTTPS (Cloudflare Tunnel)

The service listens on `127.0.0.1:8766` only. To use it from your phone, publish it through a tunnel; Cloudflare's is free and needs no open ports.

1. Install the connector on the Mac: `brew install cloudflared`.
2. In the Cloudflare dashboard: *Zero Trust → Networks → Tunnels → Create a tunnel* (type *Cloudflared*), name it, and run the install command it shows (`sudo cloudflared service install <token>`), so the tunnel starts with the Mac.
3. In the tunnel, add a *Public hostname*: subdomain `spark`, your domain (e.g. `spark.example.com`), service type `HTTP`, URL `localhost:8766`.
4. Tell the server its public URL (re-running keeps the secrets):

   ```bash
   curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash -s -- --service --public-url https://spark.example.com
   ```

5. Verify from anywhere:

   ```bash
   curl -fsS https://spark.example.com/healthz                                              # {"status":"ok"}
   curl -fsS https://spark.example.com/.well-known/oauth-authorization-server | grep issuer  # your public URL
   ```

If a client gets **Cloudflare error 1010** (or 403 from Cloudflare rather than the server), the zone's *Browser Integrity Check* or *Bot Fight Mode* is rejecting non-browser clients: add a WAF custom rule for the hostname that skips those checks. The server's token and OAuth still protect every call.

Any TLS reverse proxy (Caddy, nginx) or a private network (Tailscale, WireGuard) works as well; never publish the plain-HTTP port itself.

### 5. Connect the Claude apps (web, desktop, iOS, Android)

1. Show the sign-in password on the Mac:

   ```bash
   plutil -extract EnvironmentVariables.SPARK_MCP_OAUTH_PASSWORD raw ~/Library/LaunchAgents/io.github.vaxann.spark-mcp.plist
   ```

2. Open [claude.ai](https://claude.ai) or Claude Desktop → *Settings → Connectors → Add custom connector*.
   - Name: `Spark`
   - Remote MCP server URL: `https://spark.example.com/mcp`
   - Leave *OAuth Client ID* and *Client Secret* empty (the app registers itself).
3. Click *Connect*. A page titled *spark-mcp* opens: enter the password and press *Allow access*.
4. The connector now belongs to your Claude account and appears in the **iOS and Android apps** too. In a chat, open the tools menu (the slider/"+" button) and make sure *Spark* is enabled.
5. Try: *"Summarise unread email from today"*, *"Give me a download link for the PDF in the last email from Alice"*, *"Draft a reply and give me an upload page for the attachment"*.

On Team and Enterprise plans an owner may need to allow custom connectors first. Sessions last until revoked: the app refreshes its token automatically (access 24 h, refresh 90 days).

### 6. Connect Claude Code on another computer

```bash
# on the Mac: show the token
plutil -extract EnvironmentVariables.SPARK_MCP_HTTP_TOKEN raw ~/Library/LaunchAgents/io.github.vaxann.spark-mcp.plist
# on the other computer
claude mcp add --transport http spark https://spark.example.com/mcp --header "Authorization: Bearer <token>"
```

## Managing the service

| Task | Command |
|---|---|
| Status | `launchctl print gui/$(id -u)/io.github.vaxann.spark-mcp \| grep -E 'state\|pid'` |
| Health | `curl -fsS http://127.0.0.1:8766/healthz` |
| Logs | `tail -f ~/Library/Logs/spark-mcp/spark-mcp.log` |
| Restart | `launchctl kickstart -k gui/$(id -u)/io.github.vaxann.spark-mcp` |
| Upgrade | re-run the `--service` one-liner |
| Change URL or port | re-run with `--public-url …` or `--port …` |
| New token and password | re-run with `--rotate-secrets`, then reconnect clients |
| Sign out every Claude app | `rm ~/Library/Application\ Support/spark-mcp/oauth-state.json` and restart |
| Uninstall | `curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh \| bash -s -- --uninstall` |

Tools work only while the Mac is awake and Spark Desktop is running. For an always-available server on a Mac that stays plugged in, prevent sleep: *System Settings → Battery/Energy → Options → Prevent automatic sleeping when the display is off*, or `sudo pmset -c sleep 0`. Also add Spark Desktop to *System Settings → General → Login Items*.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `spark_unavailable`, or the connector shows no tools | Spark Desktop is closed or its CLI is not enabled. Start Spark; tools appear on the next request. Check with `~/.local/bin/spark-mcp -check`. |
| A write tool is missing | Its access level is off in *Spark → Settings → AI Agents → Spark CLI Access*. The tool list follows within a minute. |
| `cli_error: … access level …` | That particular account has a lower level than the command needs; raise it in Spark. |
| `https://…/healthz` returns 502 | The tunnel runs but the service does not: check status and logs above; the tunnel must point to `localhost:8766`. |
| Cloudflare error 1010 / 403 page | Browser Integrity Check or Bot Fight Mode: add a WAF skip rule (step 4). |
| Sign-in page says *Wrong password* | Copy the password again with the `plutil` command; it is case-sensitive. |
| OAuth metadata shows `http://127.0.0.1…` as issuer | Set `--public-url https://your-hostname`. |
| `service did not start` from the installer | Another process uses port 8766 (`lsof -iTCP:8766`), or see the log. Re-run with `--port 8767` and point the tunnel there. |

## Tools

Tools come from Spark's catalog and keep Spark's names: `accounts`, `folders`, `emails`, `search`, `thread`, `attachment`, `events`, `event`, `availability`, `contacts`, `team`, `meetings`, `meeting`, `templates`, `template`, `draft`, `comment`, `action`, `contact-action` (the exact set depends on your Spark version and access levels).

The server adds:

| Tool / parameter | Use |
|---|---|
| `draft.attachments`, `comment.attachments` | Attach files given inline as `[{name, content_base64}]` (each up to 25 MB). |
| `draft.attachment_ids` | Copy attachments of received emails onto a draft. |
| `draft.attach`, `comment.attach` | Local file paths — stdio only. |
| `attachment_link` | Short-lived signed HTTPS link that opens or downloads an attachment in any browser (HTTP only). |
| `attachment_upload_link` | Short-lived upload page that attaches the files you pick to a draft, or posts them as comments on a thread (HTTP only). |

More detail: [docs/clients.md](docs/clients.md) (transports, routes, attachments) and [docs/configuration.md](docs/configuration.md) (every option, error codes).

## Development

```bash
make build          # bin/spark-mcp
make race           # go test -race ./...  (uses the fake CLI in testdata/)
make lint           # golangci-lint
make vuln           # govulncheck
make install-agent  # dev install from a local plist (deploy/launchd/spark-mcp.local.plist)
```

Releases are built by GoReleaser when a `v*` tag is pushed. Every feature starts as an OpenSpec change (`openspec/changes/<name>/`) with a proposal, delta specs, design and tasks; once implemented it is archived into `openspec/specs/`.

## Credits and privacy

The catalog-driven tool mapping and the attachment handling follow the official [Spark for Claude](https://github.com/readdle/spark-claude-extension) extension by Spark Mail Limited (MIT); see [NOTICE](NOTICE). This repository contains no mailbox data: tests run against a fake CLI with synthetic fixtures.

## License

[MIT](LICENSE)
