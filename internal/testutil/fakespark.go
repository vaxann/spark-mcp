// Package testutil provides the fake spark CLI used by tests.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// FakeSpark is a recorded fake CLI installation.
type FakeSpark struct {
	Bin     string
	Dir     string
	Catalog string
}

// Call is one recorded invocation.
type Call struct {
	Agent string
	Args  []string
	Stdin []byte
}

func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// NewFakeSpark points the fake CLI at a fresh record directory and a private
// copy of the synthetic catalog (so tests may rewrite it).
func NewFakeSpark(t *testing.T) *FakeSpark {
	t.Helper()
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join(root(), "testdata", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalog, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SPARK_DIR", dir)
	t.Setenv("FAKE_SPARK_CATALOG", catalog)
	return &FakeSpark{Bin: filepath.Join(root(), "testdata", "fake-spark"), Dir: dir, Catalog: catalog}
}

// Calls returns every invocation so far, in order.
func (f *FakeSpark) Calls(t *testing.T) []Call {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.Dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []Call
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		parts := strings.Split(line, "\x1f")
		c := Call{Agent: strings.TrimPrefix(parts[0], "agent="), Args: parts[1:]}
		c.Stdin, _ = os.ReadFile(filepath.Join(f.Dir, fmt.Sprintf("stdin-%d", i+1)))
		calls = append(calls, c)
	}
	return calls
}

// Last returns the most recent invocation.
func (f *FakeSpark) Last(t *testing.T) Call {
	t.Helper()
	calls := f.Calls(t)
	if len(calls) == 0 {
		t.Fatal("spark was never called")
	}
	return calls[len(calls)-1]
}

// Reset forgets recorded calls.
func (f *FakeSpark) Reset(t *testing.T) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(f.Dir, "stdin-*"))
	for _, m := range append(matches, filepath.Join(f.Dir, "calls.log")) {
		_ = os.Remove(m)
	}
}
