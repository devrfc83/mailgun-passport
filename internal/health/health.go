// Package health serves the relay's HTTP health endpoints and queries them
// for the -healthcheck flag.
package health

import (
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
// smtpServing and mailgun are smtpd.Server.Serving and mailgunx.Client.Health.
func Handler(smtpServing func() bool, mailgun func() error) http.Handler {
	mux := http.NewServeMux()
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

func reply(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text+"\n")
}

// reason prefers the short description meant for SMTP clients, which leaves
// out Mailgun's response body.
func reason(err error) string {
	var de interface{ ClientMessage() string }
	if errors.As(err, &de) {
		return de.ClientMessage()
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
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + path)
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
