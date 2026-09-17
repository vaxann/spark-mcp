## Why

Spark Desktop ships a `spark` CLI that lets AI agents read and act on email, calendars, meeting transcripts, contacts and teams, and an official Claude Desktop extension that wraps it as a local stdio MCP server. Both only work on the Mac where Spark Desktop runs. Assistants elsewhere — the Claude apps on the web and on phones, Claude Code on another machine, autonomous agents — cannot reach the mailbox at all, and attachments cannot travel between the mailbox and a phone.

## What Changes

- New Go MCP server (`spark-mcp`) that runs on the Mac next to Spark Desktop and exposes the CLI over **stdio** or **streamable HTTP protected by a bearer token**, with an embedded **OAuth 2.1** authorization server for the Claude apps' custom connectors — the same transport, authentication and operations model as the maintainer's knowledge-base MCP server.
- **Catalog-driven tools**: the tool list, parameters, descriptions and server instructions come from `spark tools`, so they track the installed Spark version and the access levels configured in Spark Desktop; the catalog is re-read while the server runs and clients are notified when tools change. No server-side permission layer duplicates Spark's.
- **Attachments in both directions**: inline attachment content; signed short-lived download links; inline base64 attachments for drafts and comments; signed upload pages that attach files picked on any device to a draft or post them as comments; token-authenticated raw upload and download routes.
- **Safety**: no shell, argument vectors with option-proof values, no local file access for remote clients, timeouts, output caps, a concurrency limit, and logs without message content or secrets.
- Operations: LaunchAgent template and Makefile targets, CI (build, race tests on Linux and macOS with a fake CLI, lint, vulnerability check), release pipeline for macOS binaries.

## Capabilities

### New Capabilities
- `spark-tools`: catalog discovery and refresh, mapping of tool arguments to CLI invocations, execution limits, results, errors and audit attribution.
- `attachments`: reading attachments, signed download links, inline uploads, signed upload pages and raw HTTP routes.
- `mcp-transport`: stdio and HTTP transports, bearer and OAuth authentication, health check, deployment on macOS, configuration and logging.

### Modified Capabilities
<!-- none: this is the first change in the project -->

## Impact

- New codebase: `cmd/spark-mcp`, `internal/{config,sparkcli,mcpserver,oauth,links,testutil}`, `testdata/fake-spark`.
- Dependencies: the official MCP Go SDK and a YAML parser. Runtime requirement: macOS with Spark Desktop running and its CLI enabled.
- Security surface: in HTTP mode every MCP call needs the token or an OAuth access token; signed links grant access to one attachment or one upload target for a short time; the server never reads local files on behalf of remote clients.
- Non-goals: multi-user access, a permission model on top of Spark's access levels, parsing CLI text output into structured data, caching or indexing mail, a container image, hosting the server on a machine without Spark Desktop.
