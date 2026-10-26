package smtpd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/smtp"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCert writes a self-signed certificate for cn and returns its paths.
func writeCert(t *testing.T, dir, cn string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	write := func(name, typ string, data []byte) {
		if err := os.WriteFile(name, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: data}), 0o600); err != nil {
			t.Fatal(err)
		}
		// Filesystems with coarse timestamps would hide a quick rewrite.
		later := time.Now().Add(time.Duration(len(cn)) * time.Minute)
		if err := os.Chtimes(name, later, later); err != nil {
			t.Fatal(err)
		}
	}
	write(certFile, "CERTIFICATE", der)
	write(keyFile, "EC PRIVATE KEY", keyDER)
	return certFile, keyFile
}

func servedName(t *testing.T, r *certReloader) string {
	t.Helper()
	cert, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return cert.Leaf.Subject.CommonName
}

func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeCert(t, dir, "old")
	r, err := newCertReloader(certFile, keyFile, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got := servedName(t, r); got != "old" {
		t.Fatalf("initial certificate: %s", got)
	}

	writeCert(t, dir, "renewed")
	if got := servedName(t, r); got != "renewed" {
		t.Fatalf("after renewal: %s", got)
	}

	// A broken file keeps the previous certificate in service.
	if err := os.WriteFile(keyFile, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := servedName(t, r); got != "renewed" {
		t.Fatalf("after a broken key: %s", got)
	}
}

func TestCertReloaderNeedsValidFilesAtStart(t *testing.T) {
	dir := t.TempDir()
	if _, err := newCertReloader(filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"),
		slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("expected an error for missing files")
	}
}

func TestSTARTTLSServesReloadedCertificate(t *testing.T) {
	dir := t.TempDir()
	cfg := baseConfig()
	cfg.TLSCertFile, cfg.TLSKeyFile = writeCert(t, dir, "old")
	addr := startServer(t, cfg, &fakeSender{})

	peerName := func() string {
		t.Helper()
		c, err := smtp.Dial(addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := c.StartTLS(&tls.Config{InsecureSkipVerify: true}); err != nil {
			t.Fatal(err)
		}
		state, _ := c.TLSConnectionState()
		return state.PeerCertificates[0].Subject.CommonName
	}
	if got := peerName(); got != "old" {
		t.Fatalf("initial certificate: %s", got)
	}
	writeCert(t, dir, "renewed")
	if got := peerName(); got != "renewed" {
		t.Fatalf("after renewal: %s", got)
	}
}
