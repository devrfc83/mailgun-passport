package smtpd

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// certReloader serves the certificate in certFile/keyFile and loads it again
// when either file changes, so a renewed certificate is picked up without a
// restart. The files are checked on every STARTTLS handshake.
type certReloader struct {
	certFile, keyFile string
	log               *slog.Logger

	mu    sync.Mutex
	cert  *tls.Certificate
	stamp [2]fileStamp
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func newCertReloader(certFile, keyFile string, log *slog.Logger) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, log: log}
	r.stamp = r.stat()
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stamp := r.stat(); stamp != r.stamp {
		// The stamp is kept even if loading fails, so a half-written renewal
		// is retried once the other file changes rather than on every handshake.
		r.stamp = stamp
		if err := r.load(); err != nil {
			r.log.Error("cannot reload TLS certificate, still using the previous one", "err", err)
		}
	}
	return r.cert, nil
}

// load replaces the current certificate; on error the previous one stays.
func (r *certReloader) load() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	r.cert = &cert
	r.log.Info("TLS certificate loaded", "subject", cert.Leaf.Subject.String(),
		"not_after", cert.Leaf.NotAfter.Format(time.RFC3339))
	return nil
}

// stat follows symlinks, so a certbot "live" link that moves to a new file
// counts as a change.
func (r *certReloader) stat() [2]fileStamp {
	var out [2]fileStamp
	for i, name := range []string{r.certFile, r.keyFile} {
		if fi, err := os.Stat(name); err == nil {
			out[i] = fileStamp{fi.ModTime(), fi.Size()}
		}
	}
	return out
}
