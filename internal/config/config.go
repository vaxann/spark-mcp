// Package config loads the server configuration from defaults, an optional
// YAML file and SPARK_MCP_* environment variables (highest precedence).
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the fully resolved server configuration.
type Config struct {
	Spark  Spark  `yaml:"spark"`
	Server Server `yaml:"server"`
}

// Spark configures how the CLI is invoked.
type Spark struct {
	// Bin is the absolute path of the spark CLI. GUI and launchd sessions do
	// not have /usr/local/bin on PATH, so the default is absolute.
	Bin string `yaml:"bin"`
	// Timeout bounds one CLI call; AttachmentTimeout bounds calls that may wait
	// for an IMAP download (the CLI itself gives up after 90 s).
	Timeout           time.Duration `yaml:"timeout"`
	AttachmentTimeout time.Duration `yaml:"attachment_timeout"`
	// Concurrency is the number of CLI processes allowed at once.
	Concurrency int `yaml:"concurrency"`
	// MaxOutput caps the text a CLI call may return.
	MaxOutput string `yaml:"max_output"`
	// CatalogRefresh is how often `spark tools` is re-read so tools follow the
	// access levels configured in Spark Desktop. 0 disables periodic refresh.
	CatalogRefresh time.Duration `yaml:"catalog_refresh"`
	// Agent is reported to Spark's audit log (AI_AGENT) when the client did not
	// announce a name.
	Agent string `yaml:"agent"`
	// AutoLaunch starts Spark Desktop when a call fails because the app is not
	// running, then retries the call once. macOS only.
	AutoLaunch bool `yaml:"auto_launch"`
	// App is the bundle path or name given to `open -a`. Empty derives the
	// bundle from Bin (the CLI lives inside it).
	App string `yaml:"app"`
	// LaunchWait bounds how long a call waits for the app to answer after it
	// was launched.
	LaunchWait time.Duration `yaml:"launch_wait"`
}

// Server configures the process itself.
type Server struct {
	LogLevel string `yaml:"log_level"`
	// StateDir keeps the OAuth state and the signing key of attachment links.
	StateDir string `yaml:"state_dir"`
	// MaxAttachment caps attachment bytes returned inline by the attachment tool.
	MaxAttachment string `yaml:"max_attachment"`
	// MaxUpload caps one uploaded file (Spark accepts at most 25 MB per file).
	MaxUpload string `yaml:"max_upload"`
	// LinkTTL is the default lifetime of signed download/upload links.
	LinkTTL time.Duration `yaml:"link_ttl"`
	HTTP    HTTP          `yaml:"http"`
}

// HTTP configures the optional streamable HTTP transport.
type HTTP struct {
	Listen string `yaml:"listen"` // empty = stdio
	// Token is the bearer token. Prefer SPARK_MCP_HTTP_TOKEN over the file; it
	// is never logged.
	Token   string `yaml:"token"`
	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
	// PublicURL is the externally visible base URL used in OAuth metadata and
	// signed links (https://mcp.example.com). Derived from request headers when
	// empty.
	PublicURL string `yaml:"public_url"`
	// OAuthPassword is what a person types on the sign-in page. Falls back to
	// Token. Never logged.
	OAuthPassword string `yaml:"oauth_password"`
	// OAuthState is the file with registered clients and token hashes.
	// Defaults to <state_dir>/oauth-state.json.
	OAuthState string `yaml:"oauth_state"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Spark: Spark{
			Bin:               "/usr/local/bin/spark",
			Timeout:           60 * time.Second,
			AttachmentTimeout: 120 * time.Second,
			Concurrency:       4,
			MaxOutput:         "10MB",
			CatalogRefresh:    60 * time.Second,
			Agent:             "spark-mcp",
			AutoLaunch:        true,
			LaunchWait:        30 * time.Second,
		},
		Server: Server{
			LogLevel:      "info",
			StateDir:      defaultStateDir(),
			MaxAttachment: "10MB",
			MaxUpload:     "25MB",
			LinkTTL:       15 * time.Minute,
		},
	}
}

func defaultStateDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "spark-mcp")
	}
	return filepath.Join(os.TempDir(), "spark-mcp")
}

// Load builds the configuration: defaults, then the YAML file at path (or
// SPARK_MCP_CONFIG), then environment variables.
func Load(path string, env func(string) string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = env("SPARK_MCP_CONFIG")
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := applyEnv(&cfg, env); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func applyEnv(cfg *Config, env func(string) string) error {
	str := func(key string, dst *string) {
		if v := env(key); v != "" {
			*dst = v
		}
	}
	str("SPARK_MCP_BIN", &cfg.Spark.Bin)
	str("SPARK_MCP_MAX_OUTPUT", &cfg.Spark.MaxOutput)
	str("SPARK_MCP_AGENT", &cfg.Spark.Agent)
	str("SPARK_MCP_APP", &cfg.Spark.App)
	str("SPARK_MCP_LOG_LEVEL", &cfg.Server.LogLevel)
	str("SPARK_MCP_STATE_DIR", &cfg.Server.StateDir)
	str("SPARK_MCP_MAX_ATTACHMENT", &cfg.Server.MaxAttachment)
	str("SPARK_MCP_MAX_UPLOAD", &cfg.Server.MaxUpload)
	str("SPARK_MCP_HTTP_LISTEN", &cfg.Server.HTTP.Listen)
	str("SPARK_MCP_HTTP_TOKEN", &cfg.Server.HTTP.Token)
	str("SPARK_MCP_HTTP_TLS_CERT", &cfg.Server.HTTP.TLSCert)
	str("SPARK_MCP_HTTP_TLS_KEY", &cfg.Server.HTTP.TLSKey)
	str("SPARK_MCP_PUBLIC_URL", &cfg.Server.HTTP.PublicURL)
	str("SPARK_MCP_OAUTH_PASSWORD", &cfg.Server.HTTP.OAuthPassword)
	str("SPARK_MCP_OAUTH_STATE", &cfg.Server.HTTP.OAuthState)
	var err error
	durEnv := func(key string, dst *time.Duration) {
		if v := env(key); v != "" && err == nil {
			d, perr := time.ParseDuration(v)
			if perr != nil {
				err = fmt.Errorf("%s: %w", key, perr)
				return
			}
			*dst = d
		}
	}
	durEnv("SPARK_MCP_TIMEOUT", &cfg.Spark.Timeout)
	durEnv("SPARK_MCP_ATTACHMENT_TIMEOUT", &cfg.Spark.AttachmentTimeout)
	durEnv("SPARK_MCP_CATALOG_REFRESH", &cfg.Spark.CatalogRefresh)
	durEnv("SPARK_MCP_LAUNCH_WAIT", &cfg.Spark.LaunchWait)
	durEnv("SPARK_MCP_LINK_TTL", &cfg.Server.LinkTTL)
	if v := env("SPARK_MCP_AUTO_LAUNCH"); v != "" && err == nil {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return fmt.Errorf("SPARK_MCP_AUTO_LAUNCH: %w", perr)
		}
		cfg.Spark.AutoLaunch = b
	}
	if v := env("SPARK_MCP_CONCURRENCY"); v != "" && err == nil {
		n, perr := strconv.Atoi(v)
		if perr != nil {
			return fmt.Errorf("SPARK_MCP_CONCURRENCY: %w", perr)
		}
		cfg.Spark.Concurrency = n
	}
	return err
}

// Validate checks the configuration for problems that must stop startup.
func (c Config) Validate() error {
	if c.Spark.Bin == "" {
		return errors.New("spark.bin must not be empty (set SPARK_MCP_BIN)")
	}
	if c.Spark.Timeout <= 0 || c.Spark.AttachmentTimeout <= 0 {
		return errors.New("spark.timeout and spark.attachment_timeout must be positive")
	}
	if c.Spark.Concurrency < 1 {
		return fmt.Errorf("spark.concurrency must be at least 1, got %d", c.Spark.Concurrency)
	}
	if c.Spark.CatalogRefresh < 0 {
		return errors.New("spark.catalog_refresh must not be negative")
	}
	if c.Spark.AutoLaunch && c.Spark.LaunchWait <= 0 {
		return errors.New("spark.launch_wait must be positive when spark.auto_launch is on")
	}
	for name, v := range map[string]string{"spark.max_output": c.Spark.MaxOutput, "server.max_attachment": c.Server.MaxAttachment, "server.max_upload": c.Server.MaxUpload} {
		if n, err := ParseSize(v); err != nil || n <= 0 {
			return fmt.Errorf("%s must be a positive size such as 10MB, got %q", name, v)
		}
	}
	if c.Server.StateDir == "" || !filepath.IsAbs(c.Server.StateDir) {
		return fmt.Errorf("server.state_dir must be an absolute path, got %q", c.Server.StateDir)
	}
	if c.Server.LinkTTL <= 0 {
		return errors.New("server.link_ttl must be positive")
	}
	if h := c.Server.HTTP; h.Listen != "" {
		host, _, err := net.SplitHostPort(h.Listen)
		if err != nil {
			return fmt.Errorf("server.http.listen must be host:port, got %q", h.Listen)
		}
		ip := net.ParseIP(host)
		loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
		if !loopback && h.Token == "" {
			return fmt.Errorf("server.http.listen=%s is not loopback: set SPARK_MCP_HTTP_TOKEN (bearer token) before exposing the server", h.Listen)
		}
		if (h.TLSCert == "") != (h.TLSKey == "") {
			return errors.New("server.http.tls_cert and tls_key must be set together")
		}
	}
	switch strings.ToLower(c.Server.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("server.log_level must be debug, info, warn or error, got %q", c.Server.LogLevel)
	}
	return nil
}

// MaxOutputBytes returns the parsed CLI output limit.
func (s Spark) MaxOutputBytes() int64 { n, _ := ParseSize(s.MaxOutput); return n }

// MaxAttachmentBytes returns the parsed inline attachment limit.
func (s Server) MaxAttachmentBytes() int64 { n, _ := ParseSize(s.MaxAttachment); return n }

// MaxUploadBytes returns the parsed upload limit.
func (s Server) MaxUploadBytes() int64 { n, _ := ParseSize(s.MaxUpload); return n }

// OAuthStatePath returns the OAuth state file location.
func (h HTTP) OAuthStatePath(stateDir string) string {
	if h.OAuthState != "" {
		return h.OAuthState
	}
	return filepath.Join(stateDir, "oauth-state.json")
}

// ParseSize parses "2MB", "512KB", "100" (bytes).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, errors.New("empty size")
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}
