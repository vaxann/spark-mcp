## Why

Every tool call needs Spark Desktop running: the CLI is a thin IPC client. When the app is closed (quit by hand, crashed, not yet started after login) every request fails with `spark_unavailable` until someone at the Mac starts it. For a server whose point is reaching the mailbox from elsewhere, that is a dead end: the remote client cannot fix it.

## What Changes

- When a CLI call fails because the app is not reachable, the server launches Spark Desktop hidden in the background (`open -g -j -a`), waits until the app answers (`accounts` succeeds) and retries the call once. Nothing happened on the first attempt, so the retry is safe for write commands too.
- Launch attempts are single-flight and rate limited (once a minute), so concurrent requests and the catalog refresh loop share one attempt and a broken installation is not hammered.
- New configuration: `spark.auto_launch` (default on), `spark.app` (derived from the CLI path by default), `spark.launch_wait` (30 s). macOS only; elsewhere the option is ignored with a warning.
- A missing or unusable binary is not a launch case and still fails immediately.

## Capabilities

### New Capabilities
<!-- none -->

### Modified Capabilities
- `spark-tools`: adds the auto-launch requirement to execution.

## Impact

- `internal/sparkcli` (launcher, runner retry), `internal/config`, `cmd/spark-mcp`, `testdata/fake-spark` (a "down" switch), docs.
- No new dependencies. Behavioural change: quitting Spark Desktop no longer keeps it closed while the server receives requests; the option can be turned off.
