package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mailgun-passport/internal/config"
	"mailgun-passport/internal/mailgunx"
	"mailgun-passport/internal/smtpd"
)

func main() {
	cfg, err := config.Load()
	log := newLogger(envLogLevel(), envLogFile())
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	log = newLogger(cfg.LogLevel, cfg.LogFile)

	client, err := mailgunx.New(cfg, log)
	if err != nil {
		log.Error("failed to create Mailgun client", "err", err)
		os.Exit(1)
	}

	srv, err := smtpd.New(cfg, client, log)
	if err != nil {
		log.Error("failed to create SMTP server", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Error("error stopping SMTP server", "err", err)
			_ = srv.Close()
		}
		log.Info("service stopped")
	case err := <-errCh:
		if err != nil && !smtpd.IsClosed(err) {
			log.Error("SMTP server terminated", "err", err)
			os.Exit(1)
		}
	}
}

func envLogLevel() string {
	return strings.TrimSpace(os.Getenv("LOG_LEVEL"))
}

func envLogFile() string {
	if v := strings.TrimSpace(os.Getenv("LOG_FILE")); v != "" {
		return v
	}
	return "/var/log/mailgun-passport.log"
}

func newLogger(level string, logFile string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}

	var w io.Writer = os.Stdout
	logFile = strings.TrimSpace(logFile)
	if logFile != "" && logFile != "-" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0755); err == nil {
			if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640); err == nil {
				w = io.MultiWriter(os.Stdout, f)
			}
		}
	}

	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv}))
}
