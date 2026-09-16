package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	yml := "spark:\n  bin: /opt/spark\n  timeout: 30s\nserver:\n  max_upload: 5MB\n  http:\n    listen: 127.0.0.1:9000\n"
	if err := os.WriteFile(file, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file, envOf(map[string]string{
		"SPARK_MCP_TIMEOUT":     "45s",
		"SPARK_MCP_CONCURRENCY": "2",
		"SPARK_MCP_STATE_DIR":   dir,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spark.Bin != "/opt/spark" {
		t.Errorf("bin from file: %q", cfg.Spark.Bin)
	}
	if cfg.Spark.Timeout != 45*time.Second {
		t.Errorf("env must override file: %v", cfg.Spark.Timeout)
	}
	if cfg.Spark.Concurrency != 2 || cfg.Server.MaxUploadBytes() != 5<<20 || cfg.Server.HTTP.Listen != "127.0.0.1:9000" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if got := cfg.Server.HTTP.OAuthStatePath(dir); got != filepath.Join(dir, "oauth-state.json") {
		t.Errorf("oauth state path: %s", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"defaults", nil, ""},
		{"public listen without token", map[string]string{"SPARK_MCP_HTTP_LISTEN": "0.0.0.0:8766"}, "not loopback"},
		{"public listen with token", map[string]string{"SPARK_MCP_HTTP_LISTEN": "0.0.0.0:8766", "SPARK_MCP_HTTP_TOKEN": "t"}, ""},
		{"loopback without token", map[string]string{"SPARK_MCP_HTTP_LISTEN": "127.0.0.1:8766"}, ""},
		{"bad listen", map[string]string{"SPARK_MCP_HTTP_LISTEN": "8766"}, "host:port"},
		{"tls half", map[string]string{"SPARK_MCP_HTTP_LISTEN": "127.0.0.1:1", "SPARK_MCP_HTTP_TLS_CERT": "c"}, "together"},
		{"bad size", map[string]string{"SPARK_MCP_MAX_UPLOAD": "lots"}, "max_upload"},
		{"bad duration", map[string]string{"SPARK_MCP_TIMEOUT": "soon"}, "SPARK_MCP_TIMEOUT"},
		{"bad concurrency", map[string]string{"SPARK_MCP_CONCURRENCY": "0"}, "concurrency"},
		{"relative state dir", map[string]string{"SPARK_MCP_STATE_DIR": "state"}, "absolute"},
		{"bad log level", map[string]string{"SPARK_MCP_LOG_LEVEL": "loud"}, "log_level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load("", envOf(tc.env))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"100": 100, "2KB": 2048, "10mb": 10 << 20, "1GB": 1 << 30, "7B": 7} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x", "-1MB"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) must fail", in)
		}
	}
}

func TestAutoLaunch(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load("", envOf(map[string]string{"SPARK_MCP_STATE_DIR": dir}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Spark.AutoLaunch || cfg.Spark.LaunchWait != 30*time.Second || cfg.Spark.App != "" {
		t.Errorf("defaults: %+v", cfg.Spark)
	}
	cfg, err = Load("", envOf(map[string]string{
		"SPARK_MCP_STATE_DIR":   dir,
		"SPARK_MCP_AUTO_LAUNCH": "false",
		"SPARK_MCP_APP":         "/Applications/Example.app",
		"SPARK_MCP_LAUNCH_WAIT": "5s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Spark.AutoLaunch || cfg.Spark.App != "/Applications/Example.app" || cfg.Spark.LaunchWait != 5*time.Second {
		t.Errorf("env: %+v", cfg.Spark)
	}
	if _, err := Load("", envOf(map[string]string{"SPARK_MCP_STATE_DIR": dir, "SPARK_MCP_AUTO_LAUNCH": "maybe"})); err == nil || !strings.Contains(err.Error(), "SPARK_MCP_AUTO_LAUNCH") {
		t.Errorf("bad bool: %v", err)
	}
	if _, err := Load("", envOf(map[string]string{"SPARK_MCP_STATE_DIR": dir, "SPARK_MCP_LAUNCH_WAIT": "0s"})); err == nil || !strings.Contains(err.Error(), "launch_wait") {
		t.Errorf("zero wait: %v", err)
	}
}
