## 1. Runner

- [x] 1.1 Mark "app not running" errors; split `Run` into `Run` and `exec`; retry once after a successful launch
- [x] 1.2 Launcher: bundle derived from the CLI symlink, `open -g -j -a`, single-flight, one attempt a minute, readiness probe with `accounts`; verify tests for launch, failure, timeout, disabled runner and missing binary

## 2. Configuration and wiring

- [x] 2.1 `spark.auto_launch`, `spark.app`, `spark.launch_wait` with env overrides and validation; verify table tests
- [x] 2.2 Enable in `main` on macOS only

## 3. Fixtures and docs

- [x] 3.1 `FAKE_SPARK_DOWN` switch in the fake CLI
- [x] 3.2 Document the options and the behaviour in README, `docs/configuration.md` and `config.example.yaml`
