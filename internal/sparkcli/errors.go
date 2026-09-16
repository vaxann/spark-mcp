// Package sparkcli runs the spark CLI of Spark Desktop: it reads the tool
// catalog (`spark tools`), turns tool arguments into an argument vector and
// executes the binary without a shell, with timeouts, output limits and a cap
// on concurrent processes.
package sparkcli

import (
	"fmt"
	"regexp"
	"strings"
)

// Error is a tool-visible failure with a stable code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// notRunning marks an unavailable error caused by the app being closed
	// (as opposed to a missing binary), the case auto-launch can fix.
	notRunning bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// E builds an Error.
func E(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Stable error codes.
const (
	// CodeUnavailable: the binary is missing, Spark Desktop is not running, or
	// the catalog cannot be read.
	CodeUnavailable = "spark_unavailable"
	// CodeCLI: the CLI ran and reported a failure (unknown ID, insufficient
	// access level, validation error, ...). The message is the CLI's own text.
	CodeCLI             = "cli_error"
	CodeInvalidArgument = "invalid_argument"
	CodeUnknownTool     = "unknown_tool"
	CodeTooLarge        = "too_large"
	CodeTimeout         = "timeout"
	CodeInternal        = "internal"
)

// unavailableRE recognises CLI messages that mean the app cannot be reached.
var unavailableRE = regexp.MustCompile(`(?i)(spark desktop (is )?not running|can'?t access your spark desktop|cannot access your spark desktop|check that the app is running|could not connect|couldn't connect|unable to connect|failed to connect|connection refused|launch spark desktop)`)

// cliError classifies a non-zero exit.
func cliError(stdout, stderr string, exit int) *Error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if msg == "" {
		msg = fmt.Sprintf("spark exited with code %d", exit)
	}
	msg = strings.TrimPrefix(msg, "Error: ")
	if unavailableRE.MatchString(msg) {
		return &Error{Code: CodeUnavailable, Message: msg, notRunning: true}
	}
	return &Error{Code: CodeCLI, Message: msg}
}
