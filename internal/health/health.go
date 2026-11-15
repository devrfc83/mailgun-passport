// Package health serves the relay's HTTP health endpoints and queries them
// for the -healthcheck flag.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Handler serves two endpoints, answering 200 "ok" or 503 with the reason:
//
//   - /health: the process runs and the SMTP listener accepts connections.
//   - /ready: also, the last Mailgun call did not fail in a way that makes
//     clients retry. Not ready means clients are getting 451.
//
// smtpServing and mailgun are smtpd.Server.Serving and mailgunx.Client.Health;
// with the queue enabled, the second is queue.Queue.Health, since a Mailgun
// outage then no longer makes clients retry.
func Handler(smtpServing func() bool, mailgun func() error, opts ...Option) http.Handler {
	mux := http.NewServeMux()
	for _, opt := range opts {
		opt(mux)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		if !smtpServing() {
			reply(w, http.StatusServiceUnavailable, "SMTP listener is not running")
			return
		}
		reply(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		if !smtpServing() {
			reply(w, http.StatusServiceUnavailable, "SMTP listener is not running")
			return
		}
		if err := mailgun(); err != nil {
			reply(w, http.StatusServiceUnavailable, "Mailgun: "+reason(err))
			return
		}
		reply(w, http.StatusOK, "ok")
	})
	return mux
}

// Option adds an endpoint to the handler.
type Option func(*http.ServeMux)

// WithMetrics serves h, the Prometheus metrics, at GET /metrics.
func WithMetrics(h http.Handler) Option {
	return func(mux *http.ServeMux) { mux.Handle("GET /metrics", h) }
}

func reply(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text+"\n")
}

// clientError is a send failure with a short description meant for SMTP
// clients, which leaves out Mailgun's response body.
type clientError interface {
	error
	ClientMessage() string
}

// reason prefers the client description when err has one.
func reason(err error) string {
	if ce, ok := errors.AsType[clientError](err); ok {
		return ce.ClientMessage()
	}
	return err.Error()
}

// Check queries path on the endpoints listening at listen (HEALTH_LISTEN) and
// returns an error unless they answer 200. A wildcard host is reached over
// loopback.
func Check(listen, path string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		// Never send a local health check through HTTP_PROXY.
		Transport: &http.Transport{Proxy: nil},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
