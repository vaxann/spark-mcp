## Purpose

Defines how the server discovers Spark's tools, turns tool calls into CLI invocations and reports results.

## ADDED Requirements

### Requirement: Catalog-driven tool list
The server SHALL obtain its tools from the JSON catalog printed by `spark tools` and register one MCP tool per catalog entry with the entry's name, title, description and a JSON Schema derived from its parameters (types `string`, `integer`, `number`, `boolean`, `array`; required parameters listed as required). The catalog's `instructions` SHALL be the server instructions, followed by guidance on attachment handling. Known read tools SHALL carry `readOnlyHint`; all other catalog tools SHALL carry `destructiveHint`. The server MUST NOT filter or extend the catalog by any permission policy of its own.

#### Scenario: Tools mirror the catalog
- **WHEN** the catalog lists `accounts`, `emails` and `draft`
- **THEN** `tools/list` returns those tools with schemas built from their parameters

#### Scenario: Spark unreachable at startup
- **WHEN** `spark tools` fails while the server starts
- **THEN** the server still starts, lists no catalog tools, and its instructions tell the model that Spark Desktop must be running

### Requirement: Catalog refresh
The server SHALL re-read the catalog on `tools/list` or `tools/call` when the last attempt is older than the configured refresh interval, after a short backoff when no catalog is loaded, and when a call names a tool that is not registered. Added, changed and removed entries SHALL be applied to the tool list and connected clients SHALL receive `notifications/tools/list_changed`. An unchanged catalog MUST NOT trigger notifications.

#### Scenario: Access level lowered in Spark Desktop
- **WHEN** the catalog stops listing `draft` and `comment` and a client lists tools after the refresh interval
- **THEN** those tools and the upload-link tool are gone and the client was notified

### Requirement: Safe argument mapping
Tool arguments SHALL be mapped to an argument vector of the catalog `command`, then flagged parameters in `--flag=value` form (booleans as the bare flag when true, arrays as a repeated flag), then `--` followed by positional parameters in catalog order. Empty strings, empty arrays and false booleans SHALL be omitted. Unknown arguments, missing required arguments and type mismatches MUST fail with `invalid_argument` without running the CLI. The CLI MUST be executed without a shell.

#### Scenario: Option-looking values
- **WHEN** `emails` is called with folder `--help` and filter `-x`
- **THEN** the CLI receives `emails --filter=-x -- --help`

#### Scenario: Thread paths hidden
- **WHEN** `thread` is called
- **THEN** the invocation includes `--hide-attachment-paths`

### Requirement: Execution limits and attribution
Each CLI call SHALL run under a timeout (longer for attachment work), with at most the configured number of concurrent processes. Text output beyond the configured limit SHALL be truncated with a visible marker. Every call SHALL export `AI_AGENT` set to the sanitised MCP client name, or the configured fallback.

#### Scenario: Slow command
- **WHEN** the CLI does not finish within the timeout
- **THEN** the process is killed and the tool fails with `timeout`

### Requirement: Results and errors
A successful call SHALL return the CLI's text output as text content. A failure SHALL return a tool error whose text is `Error (<code>): <message>` and whose structured content is `{code, message}`, with codes `spark_unavailable`, `cli_error` (message is Spark's own text), `invalid_argument`, `too_large`, `timeout` or `internal`.

#### Scenario: Unknown message
- **WHEN** `thread` is called with an ID Spark does not know
- **THEN** the result is an error with code `cli_error` and Spark's message
