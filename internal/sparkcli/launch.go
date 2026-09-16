package sparkcli

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Launch configures the automatic start of Spark Desktop when a CLI call
// fails because the app is not running.
type Launch struct {
	// App is what `open -a` receives: an application bundle path or name.
	// Empty derives the bundle from the CLI path (the CLI lives inside the
	// bundle) and falls back to the application name.
	App string
	// Wait bounds how long a call waits for the app to answer after launch.
	Wait time.Duration
	// Open starts the app. Nil means `open -g -j -a App` (background, hidden).
	Open func(ctx context.Context, app string) error
}

const (
	defaultApp        = "Spark Desktop"
	defaultLaunchWait = 30 * time.Second
	// launchInterval is the minimum time between two launch attempts, so a
	// broken installation is not hammered by every request.
	launchInterval = time.Minute
	probeTimeout   = 10 * time.Second
	probeInterval  = time.Second
)

// EnableAutoLaunch makes Run start Spark Desktop and retry once when a call
// fails because the app is not reachable.
func (r *Runner) EnableAutoLaunch(l Launch) {
	if l.App == "" {
		l.App = appFromBin(r.Bin)
	}
	if l.Wait <= 0 {
		l.Wait = defaultLaunchWait
	}
	if l.Open == nil {
		l.Open = openApp
	}
	r.launch = &l
}

// appFromBin returns the bundle that contains the CLI, or the app name.
func appFromBin(bin string) string {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return defaultApp
	}
	if i := strings.Index(real, ".app/"); i >= 0 {
		return real[:i+len(".app")]
	}
	return defaultApp
}

func openApp(ctx context.Context, app string) error {
	out, err := exec.CommandContext(ctx, "open", "-g", "-j", "-a", app).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// notRunning reports whether err means the app could not be reached, as
// opposed to a missing binary or a rejected command.
func notRunning(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.notRunning
}

// relaunch starts the app and waits until the app answers. Concurrent
// callers share one attempt: they block on the mutex and then find the app up.
func (r *Runner) relaunch(ctx context.Context) error {
	r.launchMu.Lock()
	defer r.launchMu.Unlock()
	if r.probe(ctx) == nil {
		return nil
	}
	if !r.lastLaunch.IsZero() && time.Since(r.lastLaunch) < launchInterval {
		return errors.New("launched recently, not retrying yet")
	}
	r.lastLaunch = time.Now()
	r.Log.Info("Spark Desktop is not reachable, launching it", "app", r.launch.App)
	if err := r.launch.Open(ctx, r.launch.App); err != nil {
		r.Log.Warn("cannot launch Spark Desktop", "app", r.launch.App, "err", err)
		return err
	}
	deadline := time.Now().Add(r.launch.Wait)
	for {
		err := r.probe(ctx)
		if err == nil {
			r.Log.Info("Spark Desktop launched", "took", time.Since(r.lastLaunch).Round(time.Millisecond))
			return nil
		}
		if time.Now().After(deadline) {
			r.Log.Warn("Spark Desktop did not answer after launch", "wait", r.launch.Wait, "err", err)
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(probeInterval):
		}
	}
}

// probe checks whether the app answers. `spark tools` is answered by the CLI
// itself, without the app, so the probe is `accounts`: the cheapest command
// that needs the IPC connection. It bypasses the semaphore: the caller
// already holds a slot.
func (r *Runner) probe(ctx context.Context) error {
	_, err := r.exec(ctx, Call{Args: []string{"accounts"}, Timeout: probeTimeout, Agent: "spark-mcp"})
	return err
}
