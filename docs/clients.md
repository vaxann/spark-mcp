# Connecting clients

The server runs on the Mac where Spark Desktop runs: the `spark` CLI is a thin client that talks to the app over local IPC, so neither a container nor another machine can host it. Clients reach it over stdio (same Mac) or streamable HTTP (anywhere, behind a tunnel or TLS proxy).

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash                   # binary only
curl -fsSL https://raw.githubusercontent.com/vaxann/spark-mcp/main/install.sh | bash -s -- --service    # + LaunchAgent over HTTP
~/.local/bin/spark-mcp -check
```

The [README](../README.md#installation) walks through preparing Spark, the service, Cloudflare Tunnel and the Claude apps. `-check` prints the CLI version and the tools Spark currently allows; if it fails with `spark_unavailable`, start Spark Desktop and enable the CLI (*Settings → AI Agents → Spark CLI Setup*).

## Same Mac: stdio

Claude Code:

```bash
claude mcp add spark -- /absolute/path/to/spark-mcp
```

Claude Desktop (JSON config):

```json
{
  "mcpServers": {
    "spark": { "command": "/absolute/path/to/spark-mcp" }
  }
}
```

Over stdio `draft` and `comment` also accept `attach` with absolute local paths.

## Long-running instance: LaunchAgent + HTTP

`install.sh --service` writes `~/Library/LaunchAgents/io.github.vaxann.spark-mcp.plist` (mode `0600`) with a generated token and OAuth password, listens on `127.0.0.1:8766` and starts the agent; re-running upgrades and keeps secrets, `--rotate-secrets` replaces them, `--uninstall` removes everything but the state directory.

The agent runs in your user session (a LaunchDaemon could not reach Spark Desktop), restarts on failure and starts at login. The Mac must be awake and Spark Desktop running for tools to work; while Spark is unreachable the tool list is empty and fills in on the next request once Spark answers.

For development, `make install-agent` installs the working tree build with settings from `deploy/launchd/spark-mcp.local.plist` (a git-ignored copy of `deploy/launchd/spark-mcp.plist`).

### Ports and paths

| What | Where |
|---|---|
| Listen address | `SPARK_MCP_HTTP_LISTEN`, e.g. `127.0.0.1:8766` (plain HTTP) |
| MCP endpoint | `/mcp` (bearer token or OAuth access token) |
| Health check | `/healthz` (no auth) |
| OAuth | `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource`, `/oauth/register`, `/oauth/authorize`, `/oauth/token` |
| Attachment download | `GET /attachments/<id>` (token or signed link) |
| Upload page | `GET`/`POST /upload/draft/<id>`, `/upload/comment/<message-id>` (signed link) |
| Raw uploads | `PUT /drafts/<id>/attachments/<name>`, `POST /comments/<message-id>/attachments/<name>[?team=]` (token) |

Everything a tunnel or proxy forwards is the single origin, e.g. `http://127.0.0.1:8766`.

### Claude Code or Desktop with the token

```bash
claude mcp add --transport http spark https://mcp.example.com/mcp --header "Authorization: Bearer $SPARK_MCP_HTTP_TOKEN"
```

```json
{
  "mcpServers": {
    "spark": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

### Claude apps (web, iOS, Android): OAuth connector

The Claude apps add remote servers as *custom connectors* and sign in with OAuth, not static tokens. The server embeds a small OAuth 2.1 authorization server (RFC 8414/9728 metadata, dynamic client registration, PKCE S256, rotating refresh tokens) that is on whenever HTTP runs with a token or password.

1. Expose the server over HTTPS, e.g. `https://mcp.example.com`.
2. In the Claude app: *Add custom connector*, URL `https://mcp.example.com/mcp`, leave client ID and secret empty.
3. On the sign-in page, type `SPARK_MCP_OAUTH_PASSWORD`. The app gets an access token (24 h) and a refresh token (90 days, rotated on use).

Tokens and clients are stored hashed in `oauth-state.json` in the state directory; delete it and restart to revoke every session. Each wrong password costs one second. Spark's audit log names the connector's client (`AI_AGENT`).

### Cloudflare Tunnel

With `cloudflared` on the Mac, point a public hostname at the listen address:

```yaml
# ~/.cloudflared/config.yml
tunnel: spark
credentials-file: /Users/me/.cloudflared/<tunnel-id>.json
ingress:
  - hostname: mcp.example.com
    service: http://127.0.0.1:8766
  - service: http_status:404
```

When the tunnel runs on another host that reaches the Mac over a private network, bind `SPARK_MCP_HTTP_LISTEN` to that interface (the token is then mandatory) and point the hostname there. Cloudflare forwards `X-Forwarded-Proto: https` and the public `Host`, so OAuth metadata and signed links use the public URL without configuration. Verify:

```bash
curl -fsS https://mcp.example.com/healthz
curl -fsS https://mcp.example.com/.well-known/oauth-authorization-server | jq .issuer   # must print the public URL
```

Never publish the plain-HTTP port on a public interface: the token would travel in clear text.

Cloudflare's *Browser Integrity Check* (and Bot Fight Mode) can reject non-browser clients with error 1010 before they reach the server; MCP clients are not browsers. If a client fails that way, add a WAF custom rule that skips those checks for the hostname. The server's token and OAuth still gate every call.

## Attachments

| Direction | How |
|---|---|
| Read in the conversation | `attachment` with the ID from the `thread` Attachments table: images inline, text as text, PDFs and other files as a binary resource (up to `SPARK_MCP_MAX_ATTACHMENT`). |
| Open or save on a device | `attachment_link` → signed `https://…/attachments/<id>?exp&sig` link (15 min by default). Served with `nosniff` and a sandboxing CSP. |
| Attach a file the model has | `draft` / `comment` with `attachments: [{name, content_base64}]`. For a new draft the server creates it first, then streams each file onto it. |
| Attach a file from a device | Create the draft, then `attachment_upload_link` with `draft_id` (or `message_id` for comments) and open the page on the phone or computer. |
| Scripts | `curl -H "Authorization: Bearer $TOKEN" --upload-file scan.pdf https://mcp.example.com/drafts/123/attachments/scan.pdf` |

Links are HMAC-signed with a key kept in the state directory and bound to one resource, kind, expiry and (for comment uploads) team.
