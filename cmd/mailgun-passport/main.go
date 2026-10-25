package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"mailgun-passport/internal/config"
	"mailgun-passport/internal/mailgunx"
	"mailgun-passport/internal/smtpd"
)

// Set at build time with -ldflags "-X main.version=...".
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// Exit codes. The systemd unit uses RestartPreventExitStatus=2 so a bad
// configuration does not end up in a restart loop.
const (
	exitFailure = 1
	exitConfig  = 2
)

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("mailgun-passport %s (commit %s, built %s, %s)\n", version, commit, buildDate, runtime.Version())
		return
	}
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("invalid configuration", "err", err)
		return exitConfig
	}
	log, closeLog, err := newLogger(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("cannot open LOG_FILE", "err", err)
		return exitConfig
	}
	defer closeLog()
	log.Info("starting mailgun-passport", "version", version, "commit", commit, "mode", cfg.MailgunSendMode)

	client, err := mailgunx.New(cfg, log)
	if err != nil {
		log.Error("failed to create Mailgun client", "err", err)
		return exitConfig
	}

	srv, err := smtpd.New(cfg, client, log)
	if err != nil {
		log.Error("failed to create SMTP server", "err", err)
		return exitConfig
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		// Give in-flight Mailgun calls time to finish so their clients get an answer.
		shutCtx, cancel := context.WithTimeout(context.Background(), cfg.MailgunTimeout+5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Error("error stopping SMTP server", "err", err)
			_ = srv.Close()
		}
		log.Info("service stopped")
	case err := <-errCh:
		if err != nil && !smtpd.IsClosed(err) {
			log.Error("SMTP server terminated", "err", err)
			return exitFailure
		}
	}
	return 0
}

// newLogger logs to stderr (the journal under systemd, the container log under
// Docker/Podman) and additionally to logFile when one is configured.
func newLogger(level string, logFile string) (*slog.Logger, func(), error) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}

	var w io.Writer = os.Stderr
	closeFn := func() {}
	if logFile = strings.TrimSpace(logFile); logFile != "" && logFile != "-" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, nil, err
		}
		w = io.MultiWriter(os.Stderr, f)
		closeFn = func() { _ = f.Close() }
	}

	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv})), closeFn, nil
}
