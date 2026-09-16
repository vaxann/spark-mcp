# Configuration

Settings come from built-in defaults, then an optional YAML file (`-config path` or `SPARK_MCP_CONFIG`, see [config.example.yaml](../config.example.yaml)), then `SPARK_MCP_*` environment variables, which win.

## Options

| Env var | YAML | Default | Meaning |
|---|---|---|---|
| `SPARK_MCP_BIN` | `spark.bin` | `/usr/local/bin/spark` | Absolute path of the CLI. GUI and launchd sessions do not have `/usr/local/bin` on `PATH`. |
| `SPARK_MCP_TIMEOUT` | `spark.timeout` | `60s` | Limit for one CLI call. |
| `SPARK_MCP_ATTACHMENT_TIMEOUT` | `spark.attachment_timeout` | `120s` | Limit for calls that may wait for an IMAP download. |
| `SPARK_MCP_CONCURRENCY` | `spark.concurrency` | `4` | CLI processes allowed at once; further calls wait. |
| `SPARK_MCP_MAX_OUTPUT` | `spark.max_output` | `10MB` | Text output beyond this is truncated with a marker. |
| `SPARK_MCP_CATALOG_REFRESH` | `spark.catalog_refresh` | `60s` | Minimum age before `spark tools` is re-read on `tools/list` or `tools/call`. `0` reads the catalog once. |
| `SPARK_MCP_AGENT` | `spark.agent` | `spark-mcp` | `AI_AGENT` value for Spark's audit log when a client sends no name (otherwise the client's name is used). |
| `SPARK_MCP_AUTO_LAUNCH` | `spark.auto_launch` | `true` | When a call fails because Spark Desktop is closed, launch the app hidden in the background (`open -g -j -a`), wait for it to answer and retry the call once. Attempted at most once a minute. macOS only. |
| `SPARK_MCP_APP` | `spark.app` | derived | Bundle path or name passed to `open -a`. Derived from `spark.bin`, which is a symlink into the bundle. |
| `SPARK_MCP_LAUNCH_WAIT` | `spark.launch_wait` | `30s` | How long a call waits for the app to answer after launching it. |
| `SPARK_MCP_LOG_LEVEL` | `server.log_level` | `info` | `debug`, `info`, `warn`, `error`. Logs go to stderr. |
| `SPARK_MCP_STATE_DIR` | `server.state_dir` | `~/Library/Application Support/spark-mcp` | OAuth state and the HMAC key of signed links (created `0700`, files `0600`). |
| `SPARK_MCP_MAX_ATTACHMENT` | `server.max_attachment` | `10MB` | Largest attachment returned inline by `attachment`. |
| `SPARK_MCP_MAX_UPLOAD` | `server.max_upload` | `25MB` | Largest uploaded file (Spark's own limit is 25 MB). |
| `SPARK_MCP_LINK_TTL` | `server.link_ttl` | `15m` | Default lifetime of signed links (clients may ask for up to 24 h). |
| `SPARK_MCP_HTTP_LISTEN` | `server.http.listen` | empty | `host:port` to serve HTTP; empty means stdio. |
| `SPARK_MCP_HTTP_TOKEN` | `server.http.token` | empty | Bearer token; required unless the listen address is loopback. |
| `SPARK_MCP_HTTP_TLS_CERT`, `SPARK_MCP_HTTP_TLS_KEY` | `server.http.tls_cert`, `tls_key` | empty | Serve HTTPS directly. |
| `SPARK_MCP_OAUTH_PASSWORD` | `server.http.oauth_password` | the token | Password typed on the OAuth sign-in page. |
| `SPARK_MCP_PUBLIC_URL` | `server.http.public_url` | derived | Public base URL for OAuth metadata and signed links; derived from `X-Forwarded-Proto`/`X-Forwarded-Host`/`Host` when empty. |
| `SPARK_MCP_OAUTH_STATE` | `server.http.oauth_state` | `<state_dir>/oauth-state.json` | Registered OAuth clients and token hashes. |

## Access control

What an agent may do is decided by Spark, not by this server: *Spark Desktop → Settings → AI Agents* sets read-only, triage or send per account and shared inbox. `spark tools` lists only what those levels allow, so the server publishes exactly that list, and commands that need a higher level on a particular account fail with Spark's own explanation (`cli_error`).

The server's own gate is authentication: every `/mcp` request needs the bearer token or an OAuth access token.

## Error codes

Tool errors are results with `isError: true`, text `Error (<code>): <message>` and structured content `{code, message}`.

| Code | Meaning |
|---|---|
| `spark_unavailable` | The CLI is missing, Spark Desktop is not running (and could not be launched), or the catalog cannot be read. |
| `cli_error` | Spark rejected the command (unknown ID, insufficient access level, invalid combination); the message is Spark's. |
| `invalid_argument` | Unknown parameter, wrong type, missing required value, bad base64, local path over a remote connection. |
| `too_large` | Attachment above `max_attachment`, upload above `max_upload`. |
| `timeout` | The CLI did not finish in time. |
| `internal` | Anything else. |

HTTP routes return the same `{code, message}` as JSON with a matching status (400, 413, 422, 503, 504, 500).
