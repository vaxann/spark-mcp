## 1. Scaffolding

- [x] 1.1 Create `cmd/spark-mcp` and `internal/{config,sparkcli,mcpserver,oauth,links,testutil}`; verify `go build ./...`
- [x] 1.2 Makefile, golangci-lint config, CI (build, vet, race tests on Linux and macOS, lint, govulncheck), goreleaser for darwin; verify lint and govulncheck are clean
- [x] 1.3 Fake CLI (`testdata/fake-spark`) with a synthetic catalog that records argv, stdin and `AI_AGENT`; verify tests can drive it

## 2. Configuration

- [x] 2.1 Defaults → YAML → `SPARK_MCP_*` env with validation (sizes, durations, listen address, token off-loopback, TLS pair, state dir); verify table tests

## 3. CLI runner and catalog

- [x] 3.1 Runner: no shell, semaphore, timeouts, stdin, streaming stdout, text truncation, strict binary cap, error classification; verify tests for each
- [x] 3.2 Catalog parsing and validation, JSON Schema generation, argument vector mapping with `--flag=value` and `--`; verify table tests including option-looking values and type errors
- [x] 3.3 Attachment helpers: metadata parsing, stream read, `--attach-stream` for drafts and comments, draft ID and deep link parsing, MIME normalisation and sniffing, safe file names; verify tests

## 4. MCP server

- [x] 4.1 Register catalog tools with annotations and instructions; fallback when Spark is unreachable; verify in-memory client tests
- [x] 4.2 Catalog refresh on `tools/list`/`tools/call` with list-changed notifications; verify removal and recovery tests
- [x] 4.3 `thread` with `--hide-attachment-paths`; structured errors; `AI_AGENT` from the client name; verify tests
- [x] 4.4 `draft`/`comment` inline attachments, `attachment_ids`, stdio-only `attach`; verify call sequences and rejections
- [x] 4.5 `attachment` content blocks with size limit; verify tests

## 5. HTTP transport and attachments

- [x] 5.1 Port bearer middleware, OAuth server and health check from the knowledge-base server; verify auth and OAuth flow tests
- [x] 5.2 Signed links (HMAC key in state dir, bound parameters); verify tampering, expiry and restart tests
- [x] 5.3 `attachment_link` and `GET /attachments/<id>` streaming with safe headers; public base from headers; verify tests
- [x] 5.4 `attachment_upload_link`, upload page, raw upload routes; verify multipart, team binding and size limit tests
- [x] 5.5 Smoke test against a live Spark Desktop over HTTP on loopback: tool list, read tool, CLI error, inline PDF attachment, signed download, tampered link
- [x] 5.6 Smoke test uploads against a live Spark Desktop through the public tunnel on a throwaway draft: inline attachment, signed upload page, attachments visible in the thread, draft deleted

## 6. Operations and docs

- [x] 6.1 LaunchAgent template, `make install-agent`/`uninstall-agent`, example config
- [x] 6.2 README, `docs/clients.md` (stdio, LaunchAgent, token, OAuth connector, Cloudflare Tunnel, attachments), `docs/configuration.md` (options, access control, error codes)
- [x] 6.3 Publish through the maintainer's tunnel as a LaunchAgent; verify health, OAuth metadata, 401 discovery, token calls, signed links and the full OAuth flow (registration, password, PKCE, token, refresh) from outside
- [ ] 6.4 Connect a Claude app custom connector
- [ ] 6.5 Create the public GitHub repository, push, tag the first release
