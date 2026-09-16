package sparkcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Runner executes the spark binary.
type Runner struct {
	Bin       string
	MaxOutput int64
	Timeout   time.Duration
	Log       *slog.Logger
	sem       chan struct{}

	launch     *Launch
	launchMu   sync.Mutex
	lastLaunch time.Time
}

// NewRunner builds a runner allowing concurrency processes at once.
func NewRunner(bin string, concurrency int, timeout time.Duration, maxOutput int64, log *slog.Logger) *Runner {
	if concurrency < 1 {
		concurrency = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{Bin: bin, MaxOutput: maxOutput, Timeout: timeout, Log: log, sem: make(chan struct{}, concurrency)}
}

// Call describes one invocation.
type Call struct {
	Args  []string
	Stdin []byte
	// Stdout, when set, receives stdout directly (streaming, no capture).
	Stdout io.Writer
	// MaxBytes, when > 0 and Stdout is nil, fails the call with too_large once
	// stdout exceeds it (used for binary content). Otherwise text beyond the
	// runner's MaxOutput is dropped and Result.Truncated is set.
	MaxBytes int64
	Timeout  time.Duration
	// Agent is exported as AI_AGENT so Spark's audit log names the client.
	Agent string
}

// Result is the outcome of a successful call.
type Result struct {
	Stdout    []byte
	Stderr    string
	Truncated bool
}

// Text returns trimmed stdout, falling back to stderr.
func (r *Result) Text() string {
	out := strings.TrimSpace(string(r.Stdout))
	if out == "" {
		out = strings.TrimSpace(r.Stderr)
	}
	if r.Truncated {
		out += "\n\n[output truncated by spark-mcp]"
	}
	return out
}

var agentRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SanitizeAgent makes a client name safe for the AI_AGENT variable.
func SanitizeAgent(name string) string {
	name = strings.Trim(agentRE.ReplaceAllString(strings.TrimSpace(name), "-"), "-")
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// Run executes the call. Failures are *Error values. With auto-launch
// enabled, a call that fails because Spark Desktop is not running starts the
// app and is retried once; nothing has happened on the first attempt, so the
// retry is safe for write commands too.
func (r *Runner) Run(ctx context.Context, c Call) (*Result, error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, E(CodeTimeout, "gave up waiting for a free spark process slot")
	}
	defer func() { <-r.sem }()

	res, err := r.exec(ctx, c)
	if err != nil && r.launch != nil && notRunning(err) && ctx.Err() == nil {
		if r.relaunch(ctx) == nil {
			return r.exec(ctx, c)
		}
	}
	return res, err
}

// exec runs one process without touching the semaphore.
func (r *Runner) exec(ctx context.Context, c Call) (*Result, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = r.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, r.Bin, c.Args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = os.Environ()
	if c.Agent != "" {
		cmd.Env = append(cmd.Env, "AI_AGENT="+c.Agent)
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stderr capBuffer
	stderr.max = 64 << 10
	cmd.Stderr = &stderr
	var stdout *capBuffer
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	} else {
		stdout = &capBuffer{max: r.MaxOutput}
		if c.MaxBytes > 0 {
			stdout.max, stdout.strict, stdout.cancel = c.MaxBytes, true, cancel
		}
		cmd.Stdout = stdout
	}
	err := cmd.Run()
	cmdName := ""
	if len(c.Args) > 0 {
		cmdName = c.Args[0]
	}
	exit := -1
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	// Never log argument values: they carry recipients, subjects and bodies.
	r.Log.Debug("spark call", "command", cmdName, "args", len(c.Args)-1, "exit", exit, "duration", time.Since(start).Round(time.Millisecond))
	if stdout != nil && stdout.overflow && stdout.strict {
		return nil, E(CodeTooLarge, "output exceeds the %d byte limit", c.MaxBytes)
	}
	if err != nil {
		switch {
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission):
			return nil, E(CodeUnavailable, "spark CLI not usable at %s: enable it in Spark Desktop (Settings > AI Agents > Spark CLI Setup) or set SPARK_MCP_BIN", r.Bin)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, E(CodeTimeout, "spark %s did not finish within %s", cmdName, timeout)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			out := ""
			if stdout != nil {
				out = string(stdout.Bytes())
			}
			return nil, cliError(out, stderr.String(), ee.ExitCode())
		}
		return nil, E(CodeInternal, "run spark %s: %s", cmdName, err)
	}
	res := &Result{Stderr: stderr.String()}
	if stdout != nil {
		res.Stdout, res.Truncated = stdout.Bytes(), stdout.overflow
	}
	return res, nil
}

// capBuffer keeps at most max bytes. In strict mode it cancels the process on
// overflow; otherwise it silently drops the rest so the process can finish.
type capBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	max      int64
	strict   bool
	overflow bool
	cancel   context.CancelFunc
}

func (b *capBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.max - int64(b.buf.Len())
	if b.max <= 0 {
		room = int64(len(p))
	}
	if int64(len(p)) > room {
		if room > 0 && !b.strict {
			b.buf.Write(p[:room])
		}
		b.overflow = true
		if b.strict && b.cancel != nil {
			b.cancel()
			return 0, io.ErrShortWrite
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *capBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

func (b *capBuffer) String() string { return string(b.Bytes()) }
