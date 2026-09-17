## Purpose

Defines how clients connect and how the server runs: transports, authentication, deployment, configuration and observability.

## ADDED Requirements

### Requirement: stdio transport by default
WHEN no HTTP listen address is configured, the server SHALL speak MCP over standard input and output and MUST write logs only to standard error.

#### Scenario: Launched by Claude Code
- **WHEN** a local client launches the binary
- **THEN** initialisation completes and the catalog tools are usable

### Requirement: Streamable HTTP with bearer authentication
WHEN an HTTP listen address is configured, the server SHALL serve MCP at `/mcp` and an unauthenticated `GET /healthz` returning `{"status":"ok"}`. Every `/mcp` request MUST carry a valid bearer token (compared in constant time) or OAuth access token; otherwise the server responds `401` with `WWW-Authenticate` and runs nothing. The server MUST refuse to start on a non-loopback address without a token. TLS MAY be served directly; otherwise a tunnel or reverse proxy is expected in front.

#### Scenario: Missing token
- **WHEN** a request to `/mcp` has no valid token
- **THEN** the response is `401` and no CLI process starts

### Requirement: OAuth sign-in for the Claude apps
The HTTP transport SHALL embed an OAuth 2.1 authorization server: RFC 9728 protected-resource and RFC 8414 authorization-server metadata, RFC 7591 dynamic registration with `https` (or loopback `http`) redirect URIs, the authorization code grant with mandatory PKCE S256, a sign-in page accepting the configured password (defaulting to the token), 24-hour access tokens and rotating 90-day refresh tokens stored hashed in a state file that survives restarts. Wrong passwords MUST be delayed. The password SHALL be independent of any other service's credentials.

#### Scenario: Custom connector
- **WHEN** a Claude app adds the server URL as a custom connector and the person enters the password
- **THEN** the app obtains tokens and can call tools

### Requirement: Deployment on the Spark Desktop machine
The project SHALL provide macOS binaries, a LaunchAgent template running in the user's session with restart on failure and logs under `~/Library/Logs/spark-mcp`, and Makefile targets to install and remove it. Secrets SHALL live only in git-ignored local files. The documentation SHALL explain exposing the server through a tunnel or TLS proxy and SHALL warn against publishing the plain-HTTP port.

#### Scenario: Login
- **WHEN** the user logs in on the Mac
- **THEN** the agent starts the server, which serves tools as soon as Spark Desktop answers

### Requirement: Configuration and logging
Configuration SHALL load from defaults, an optional YAML file and `SPARK_MCP_*` environment variables in increasing precedence, and invalid values MUST stop startup with a clear message. Logs SHALL record tool names, durations and error codes and MUST NOT contain message content, recipients, subjects, argument values, attachment bytes or secrets. A `-check` flag SHALL verify that the CLI and catalog are reachable.

#### Scenario: Check
- **WHEN** `spark-mcp -check` runs with Spark Desktop open
- **THEN** it prints the CLI version and the tool names and exits 0
