// Command spark-mcp serves the spark CLI of Spark Desktop over the Model
// Context Protocol, on stdio or streamable HTTP.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/vaxann/spark-mcp/internal/config"
	"github.com/vaxann/spark-mcp/internal/links"
	"github.com/vaxann/spark-mcp/internal/mcpserver"
	"github.com/vaxann/spark-mcp/internal/oauth"
	"github.com/vaxann/spark-mcp/internal/sparkcli"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	var (
		cfgPath     = flag.String("config", "", "path to config.yaml (or set SPARK_MCP_CONFIG)")
		showVersion = flag.Bool("version", false, "print version and exit")
		check       = flag.Bool("check", false, "validate configuration, reach Spark Desktop and print the tool catalog summary, then exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: spark-mcp [flags]\n\nServes the Spark CLI over MCP on stdio, or over streamable HTTP when SPARK_MCP_HTTP_LISTEN is set.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	cfg, err := config.Load(*cfgPath, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		return 2
	}
	log := newLogger(cfg.Server.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner := sparkcli.NewRunner(cfg.Spark.Bin, cfg.Spark.Concurrency, cfg.Spark.Timeout, cfg.Spark.MaxOutputBytes(), log)
	if cfg.Spark.AutoLaunch {
		if runtime.GOOS == "darwin" {
			runner.EnableAutoLaunch(sparkcli.Launch{App: cfg.Spark.App, Wait: cfg.Spark.LaunchWait})
		} else {
			log.Warn("spark.auto_launch is only supported on macOS, ignoring")
		}
	}
	if *check {
		return checkSpark(ctx, runner, cfg.Spark.Agent)
	}
	h := cfg.Server.HTTP
	remote := h.Listen != ""
	if err := os.MkdirAll(cfg.Server.StateDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "startup error:", err)
		return 1
	}
	srv := mcpserver.New(ctx, mcpserver.Options{
		Runner:            runner,
		Version:           version,
		Log:               log,
		LocalFiles:        !remote,
		Remote:            remote,
		AttachmentTimeout: cfg.Spark.AttachmentTimeout,
		MaxAttachment:     cfg.Server.MaxAttachmentBytes(),
		MaxUpload:         cfg.Server.MaxUploadBytes(),
		LinkTTL:           cfg.Server.LinkTTL,
		CatalogRefresh:    cfg.Spark.CatalogRefresh,
		Agent:             cfg.Spark.Agent,
		Signer:            links.NewSigner(filepath.Join(cfg.Server.StateDir, "link-secret")),
		PublicURL:         h.PublicURL,
	})
	log.Info("starting", "version", version, "spark", cfg.Spark.Bin, "transport", map[bool]string{true: "http", false: "stdio"}[remote])
	var runErr error
	if remote {
		opts := mcpserver.HTTPOptions{Listen: h.Listen, Token: h.Token, TLSCert: h.TLSCert, TLSKey: h.TLSKey}
		if pw := firstNonEmpty(h.OAuthPassword, h.Token); pw != "" {
			statePath := h.OAuthStatePath(cfg.Server.StateDir)
			opts.OAuth, err = oauth.New(oauth.Config{PublicURL: h.PublicURL, Password: pw, StaticToken: h.Token, StatePath: statePath, Logger: log})
			if err != nil {
				fmt.Fprintln(os.Stderr, "startup error:", err)
				return 1
			}
			log.Info("OAuth sign-in enabled", "public_url", h.PublicURL)
		}
		runErr = srv.RunHTTP(ctx, opts)
	} else {
		runErr = srv.Run(ctx)
	}
	if err := runErr; err != nil && ctx.Err() == nil {
		log.Error("server stopped", "err", err)
		return 1
	}
	return 0
}

func checkSpark(ctx context.Context, runner *sparkcli.Runner, agent string) int {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := runner.Run(ctx, sparkcli.Call{Args: []string{"--version"}, Agent: agent})
	if err != nil {
		fmt.Fprintln(os.Stderr, "spark CLI:", err)
		return 1
	}
	cat, err := runner.LoadCatalog(ctx, agent)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spark tools:", err)
		return 1
	}
	names := make([]string, 0, len(cat.Tools))
	for _, t := range cat.Tools {
		names = append(names, t.Name)
	}
	fmt.Fprintf(os.Stderr, "ok: spark %s, %d tools: %s\n", strings.TrimSpace(string(res.Stdout)), len(names), strings.Join(names, ", "))
	return 0
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	// Logs go to stderr only: stdout carries the MCP protocol in stdio mode.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
