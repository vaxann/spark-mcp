## Context

The CLI reports a closed app with a non-zero exit and a message the runner already recognises (`unavailableRE`), mapped to `spark_unavailable`. The same code covers a missing binary and an unparsable catalog, which launching cannot fix. The server runs as a LaunchAgent in the user's GUI session, so `open` can start GUI apps. The CLI is a symlink into the application bundle, so the bundle path can be derived.

## Decisions

### D1. Retry inside the runner, not in the MCP layer
`Runner.Run` is the single place every CLI process goes through (tool calls, catalog refresh, `-check`), so the retry lives there. `Run` is split into `Run` (semaphore, launch, retry) and `exec` (one process); the readiness probe uses `exec` because the caller already holds a slot.

### D2. Only "app not running" triggers a launch
`cliError` marks the recognised messages with an unexported `notRunning` flag on `*Error`. A missing binary, a timeout or a rejected command never launch anything.

### D3. Single-flight, rate limited, probe first
A mutex serialises launch attempts. The holder first probes with `accounts` (`spark tools` is answered by the CLI itself without the app, so it proves nothing; `accounts` is the cheapest command that needs IPC; another caller may just have brought the app up), then refuses if a launch happened less than a minute ago, then runs `open -g -j -a <app>` and probes once a second until the app answers or `launch_wait` elapses. Waiting callers find the app up and simply retry.

### D4. Hidden background launch
`-g` keeps the app out of the foreground and `-j` launches it hidden, so a request from a phone does not steal focus on the Mac.

### D5. Testable without macOS
The launch command is a function on `Launch` that tests replace; the fake CLI fails every command while `FAKE_SPARK_DOWN` names an existing file, and the test's fake launcher removes that file.
