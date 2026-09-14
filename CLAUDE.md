# Spark MCP — guidance for AI coding agents

## Project
Go MCP server that exposes the `spark` CLI of Spark Desktop (email, calendars, meetings, contacts, teams) to MCP clients over stdio or streamable HTTP with bearer/OAuth authentication. The tool list is discovered from `spark tools`; the server adds attachment transfer (inline content, signed download and upload links).
Module: `github.com/vaxann/spark-mcp`. Go 1.26+. Runs on macOS next to a running Spark Desktop (the CLI talks to the app over IPC).

## Workflow
- Spec-driven with OpenSpec: `/opsx:propose`, `/opsx:apply`, `/opsx:archive`. Read `openspec/` before changing behavior.
- All repository content (specs, code, comments, docs, commits) is in **English**. Discussion with the maintainer may happen in other languages; that never leaks into the repo.

## Privacy and security (this repo is public)
- Never commit the maintainer's data: no email addresses, account names, message content, contact names, hostnames, IP addresses, tokens or real paths.
- Tests use the fake CLI under `testdata/` with synthetic fixtures only (example.com addresses). Never capture real `spark` output into fixtures.
- Logs must not contain message bodies, recipients, subjects, tool argument values, attachment bytes or secrets.
- Never run write commands (`draft`, `comment`, `action`, `contact-action`, `event`) against the maintainer's live Spark while developing without explicit permission.

## Code conventions
- Standard library first; keep dependencies few and pinned.
- `gofmt`, `go vet`, `golangci-lint` clean. Table-driven tests.
- Tool names come from the Spark catalog unchanged; tools added by this server are named so they cannot collide (`attachment_link`, `attachment_upload_link`). Errors carry a stable `code`.
- The `spark` binary is always executed without a shell, with an argument vector.
