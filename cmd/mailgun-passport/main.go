package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/devrfc83/mailgun-passport/internal/config"
	"github.com/devrfc83/mailgun-passport/internal/health"
	"github.com/devrfc83/mailgun-passport/internal/mailgunx"
	"github.com/devrfc83/mailgun-passport/internal/metrics"
	"github.com/devrfc83/mailgun-passport/internal/parser"
	"github.com/devrfc83/mailgun-passport/internal/queue"
	"github.com/devrfc83/mailgun-passport/internal/smtpd"
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
	healthcheck := flag.Bool("healthcheck", false,
		"query /health on the running relay (HEALTH_LISTEN) and exit 0 if it is healthy")
	flag.Parse()
	// `go install …@vX.Y.Z` skips the Makefile's -ldflags but records the module version.
	if info, ok := debug.ReadBuildInfo(); ok && version == "dev" &&
		info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = strings.TrimPrefix(info.Main.Version, "v")
	}
	if *showVersion {
		fmt.Printf("mailgun-passport %s (commit %s, built %s, %s)\n", version, commit, buildDate, runtime.Version())
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck())
	}
	os.Exit(run())
}

// run serves until SIGINT or SIGTERM. SIGHUP reloads the configuration.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	return serve(ctx, hup)
}

// runHealthcheck is meant for container HEALTHCHECKs: the distroless image
// has no curl. It exits 0 when healthy and 1 otherwise, as Docker expects.
func runHealthcheck() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration:", err)
		return 1
	}
	if cfg.HealthListen == "" {
		fmt.Fprintln(os.Stderr, "HEALTH_LISTEN is not set")
		return 1
	}
	if err := health.Check(cfg.HealthListen, "/health"); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	return 0
}

// serve runs the relay until ctx ends and returns the exit code. A value on
// hup reloads the configuration.
func serve(ctx context.Context, hup <-chan os.Signal) int {
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("invalid configuration", "err", err)
		return exitConfig
	}
	logs, err := newLogger(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("cannot open LOG_FILE", "err", err)
		return exitConfig
	}
	defer logs.Close()
	log := logs.Logger
	log.Info("starting mailgun-passport", "version", version, "commit", commit, "mode", cfg.MailgunSendMode)

	m := metrics.New(version, commit)
	client := mailgunx.New(cfg, log, mailgunx.WithMetrics(m))
	// A wrong key or domain would otherwise only show up as 451 replies to
	// every client. An unreachable API is not a configuration error.
	if err := client.Verify(ctx); err == nil {
		log.Info("Mailgun accepted the API key and domain", "domain", cfg.MailgunDomain)
	} else if mailgunx.Fatal(err) {
		log.Error("Mailgun rejected the API key or domain; check MAILGUN_API_KEY, "+
			"MAILGUN_DOMAIN and MAILGUN_API_BASE", "domain", cfg.MailgunDomain, "err", err)
		return exitConfig
	} else {
		log.Warn("cannot verify the Mailgun API key, starting anyway", "err", err)
	}

	// Without the queue, the SMTP client waits for Mailgun and gets its error.
	// With it, the client gets 250 once the message is on disk, and what makes
	// clients retry is no longer Mailgun failing but the queue not accepting.
	var sender smtpd.Sender = client
	ready := client.Health
	var spool *queue.Queue
	if cfg.QueueDir != "" {
		spool, err = queue.Open(queue.Options{
			Dir:      cfg.QueueDir,
			MaxBytes: cfg.QueueMaxBytes,
			MaxAge:   cfg.QueueMaxAge,
			Workers:  cfg.QueueWorkers,
			Metrics:  m,
		}, client, reparser(cfg), log)
		if err != nil {
			log.Error("cannot use QUEUE_DIR", "dir", cfg.QueueDir, "err", err)
			return exitConfig
		}
		m.GaugeFunc("queue_messages", "Messages waiting for delivery in the queue.",
			func() float64 { n, _ := spool.Stats(); return float64(n) })
		m.GaugeFunc("queue_bytes", "Size of the messages waiting in the queue.",
			func() float64 { _, b := spool.Stats(); return float64(b) })
		spool.Start()
		sender, ready = spool, spool.Health
		log.Info("queueing messages on disk before delivery", "dir", cfg.QueueDir,
			"max_bytes", cfg.QueueMaxBytes, "max_age", cfg.QueueMaxAge.String(), "workers", cfg.QueueWorkers)
	}

	srv, err := smtpd.New(cfg, sender, log, smtpd.WithMetrics(m))
	if err != nil {
		log.Error("failed to create SMTP server", "err", err)
		return exitConfig
	}

	if cfg.HealthListen != "" {
		hl, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HealthListen)
		if err != nil {
			log.Error("cannot listen on HEALTH_LISTEN", "err", err)
			return exitFailure
		}
		var opts []health.Option
		endpoints := "/health and /ready"
		if cfg.MetricsEnabled {
			opts = append(opts, health.WithMetrics(m.Handler()))
			endpoints += " and /metrics"
		}
		hs := &http.Server{
			Handler:           health.Handler(srv.Serving, ready, opts...),
			ReadHeaderTimeout: 5 * time.Second,
		}
		defer hs.Close()
		log.Info("serving "+endpoints, "addr", hl.Addr().String())
		go func() {
			if err := hs.Serve(hl); !errors.Is(err, http.ErrServerClosed) {
				log.Error("health endpoints stopped", "err", err)
			}
		}()
	}

	a := &app{cfg: cfg, log: log, logs: logs, client: client, srv: srv, metrics: m}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	for {
		select {
		case <-hup:
			a.reload(ctx)
		case <-ctx.Done():
			// Give in-flight Mailgun calls time to finish so their clients get an answer.
			shutCtx, cancel := context.WithTimeout(context.Background(), cfg.MailgunTimeout+5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutCtx); err != nil {
				log.Error("error stopping SMTP server", "err", err)
				_ = srv.Close()
			}
			if spool != nil {
				// Deliveries in progress finish; the rest waits on disk for the next start.
				if err := spool.Shutdown(shutCtx); err != nil {
					log.Error("queued deliveries still running at shutdown; they stay queued", "err", err)
				}
				n, size := spool.Stats()
				log.Info("messages left in the queue", "count", n, "bytes", size)
			}
			log.Info("service stopped")
			return 0
		case err := <-errCh:
			if err != nil && !smtpd.IsClosed(err) {
				log.Error("SMTP server terminated", "err", err)
				return exitFailure
			}
			return 0
		}
	}
}

// reparser returns what the queue uses to rebuild a stored message: the parser
// the SMTP side uses for the configured send mode.
func reparser(cfg config.Config) queue.Parse {
	parse := parser.Parse
	if cfg.MailgunSendMode == config.SendModeMIME {
		parse = parser.ParseHeaders
	}
	return func(mailFrom string, rcptTo []string, raw []byte) (*parser.Message, error) {
		// The message was within the limit when it was accepted.
		return parse(mailFrom, rcptTo, bytes.NewReader(raw), int64(len(raw)))
	}
}

// app holds what a reload changes.
type app struct {
	cfg     config.Config
	log     *slog.Logger
	logs    *logging
	client  *mailgunx.Client
	srv     *smtpd.Server
	metrics *metrics.Set
}

// reload applies a SIGHUP: it reads the configuration again and applies what
// can change while running (see config.Config.Reload), loads the STARTTLS
// certificate again and reopens LOG_FILE, so logrotate needs no copytruncate.
// A configuration that is invalid, or that Mailgun rejects, is refused and the
// relay keeps running with the one it has.
func (a *app) reload(ctx context.Context) {
	a.log.Info("reloading on SIGHUP")
	failed := false
	fail := func(msg string, err error) {
		failed = true
		a.log.Error(msg, "err", err)
	}

	if err := a.logs.Reopen(); err != nil {
		fail("cannot reopen LOG_FILE, still writing to the previous file", err)
	}
	if err := a.srv.ReloadCertificate(); err != nil {
		fail("cannot reload the STARTTLS certificate, still using the previous one", err)
	}

	next, err := config.Reload()
	if err != nil {
		fail("invalid configuration, keeping the current one", err)
		a.reloaded(failed)
		return
	}
	merged, changed, restart := a.cfg.Reload(next)
	if len(restart) > 0 {
		a.log.Warn("these settings changed but need a restart to take effect", "settings", restart)
	}
	if len(changed) == 0 {
		a.log.Info("configuration unchanged")
		a.reloaded(failed)
		return
	}

	// Try the new key and domain before switching to them.
	if slices.ContainsFunc(changed, func(name string) bool { return strings.HasPrefix(name, "MAILGUN_") }) {
		probe := mailgunx.New(merged, a.log)
		if err := probe.Verify(ctx); mailgunx.Fatal(err) {
			fail("Mailgun rejected the new API key or domain, keeping the current configuration", err)
			a.reloaded(failed)
			return
		} else if err != nil {
			a.log.Warn("cannot verify the new Mailgun settings, applying them anyway", "err", err)
		}
	}

	a.client.Reconfigure(merged)
	a.srv.Reconfigure(merged)
	a.logs.SetLevel(merged.LogLevel)
	a.cfg = merged
	a.log.Info("configuration reloaded", "changed", changed)
	a.reloaded(failed)
}

func (a *app) reloaded(failed bool) {
	if failed {
		a.metrics.Reloads.Inc("failure")
	} else {
		a.metrics.Reloads.Inc("success")
	}
}

// logging is the relay's logger and the way to change it while running.
type logging struct {
	*slog.Logger
	level *slog.LevelVar
	out   *logOutput
}

// logOutput writes to stderr (the journal under systemd, the container log
// under Docker/Podman) and additionally to a file when one is configured.
type logOutput struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func (o *logOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := os.Stderr.Write(p)
	if o.file != nil {
		if _, ferr := o.file.Write(p); ferr != nil && err == nil {
			err = ferr
		}
	}
	return n, err
}

func (o *logOutput) open() error {
	// Group-readable on purpose, so an operator in the log group can read it.
	f, err := os.OpenFile(o.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640) //nolint:gosec
	if err != nil {
		return err
	}
	o.mu.Lock()
	old := o.file
	o.file = f
	o.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// newLogger logs to stderr and additionally to logFile when one is configured.
func newLogger(level string, logFile string) (*logging, error) {
	lv := &slog.LevelVar{}
	lv.Set(parseLevel(level))
	out := &logOutput{}
	if logFile = strings.TrimSpace(logFile); logFile != "" && logFile != "-" {
		out.path = logFile
		if err := out.open(); err != nil {
			return nil, err
		}
	}
	return &logging{
		Logger: slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: lv})),
		level:  lv,
		out:    out,
	}, nil
}

// SetLevel changes the log level.
func (l *logging) SetLevel(level string) {
	l.level.Set(parseLevel(level))
}

// Reopen opens LOG_FILE again, so a rotated file is replaced by a fresh one.
// It does nothing when logging goes to stderr only. On error the previous
// file stays in use.
func (l *logging) Reopen() error {
	if l.out.path == "" {
		return nil
	}
	return l.out.open()
}

// Close closes LOG_FILE.
func (l *logging) Close() {
	l.out.mu.Lock()
	defer l.out.mu.Unlock()
	if l.out.file != nil {
		_ = l.out.file.Close()
		l.out.file = nil
	}
}
