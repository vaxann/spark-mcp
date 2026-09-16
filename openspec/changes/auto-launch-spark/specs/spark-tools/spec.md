## MODIFIED Requirements

### Requirement: Execution limits and attribution
Each CLI call SHALL run under a timeout (longer for attachment work), with at most the configured number of concurrent processes. Text output beyond the configured limit SHALL be truncated with a visible marker. Every call SHALL export `AI_AGENT` set to the sanitised MCP client name, or the configured fallback.

When auto-launch is enabled and a call fails because Spark Desktop is not running, the server SHALL launch the configured application hidden in the background, wait up to the configured time for the app to answer (`accounts` succeeds; `spark tools` is answered without the app), and retry the call once. Concurrent failures SHALL share one launch attempt, and attempts MUST be at least one minute apart. A missing or unusable binary MUST NOT trigger a launch. If the app cannot be launched or does not answer in time, the call SHALL fail with the original `spark_unavailable` error.

#### Scenario: Slow command
- **WHEN** the CLI does not finish within the timeout
- **THEN** the process is killed and the tool fails with `timeout`

#### Scenario: App closed
- **WHEN** `draft` is called while Spark Desktop is closed and auto-launch is on
- **THEN** the app is launched, and once `accounts` answers the same `draft` call runs again and its result is returned

#### Scenario: App cannot be launched
- **WHEN** `open` fails and three calls arrive within a minute
- **THEN** the app is opened once, and each call fails with `spark_unavailable`
