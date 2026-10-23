package edge

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// CertReloader serves a certificate from files and reloads it when either file's mtime changes (PL-EDGE-002).
type CertReloader struct {
	certFile, keyFile string
	log               *slog.Logger
	every             time.Duration

	mu        sync.Mutex
	cert      *tls.Certificate
	mtime     time.Time
	lastCheck time.Time
}

// NewCertReloader loads the certificate once (failing fast) and returns the reloader.
func NewCertReloader(certFile, keyFile string, log *slog.Logger) (*CertReloader, error) {
	if log == nil {
		log = slog.Default()
	}
	c := &CertReloader{certFile: certFile, keyFile: keyFile, log: log, every: 10 * time.Second}
	if err := c.reload(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *CertReloader) modTime() (time.Time, error) {
	a, err := os.Stat(c.certFile)
	if err != nil {
		return time.Time{}, err
	}
	b, err := os.Stat(c.keyFile)
	if err != nil {
		return time.Time{}, err
	}
	if b.ModTime().After(a.ModTime()) {
		return b.ModTime(), nil
	}
	return a.ModTime(), nil
}

func (c *CertReloader) reload() error {
	mt, err := c.modTime()
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("load origin certificate: %w", err)
	}
	if cert.Leaf == nil && len(cert.Certificate) > 0 {
		cert.Leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	c.mu.Lock()
	c.cert, c.mtime = &cert, mt
	c.mu.Unlock()
	return nil
}

// GetCertificate implements tls.Config.GetCertificate, checking for new files at most every 10 s. A broken new pair is
// logged and the previous certificate keeps serving.
func (c *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	check := time.Since(c.lastCheck) > c.every
	if check {
		c.lastCheck = time.Now()
	}
	cur, mt := c.cert, c.mtime
	c.mu.Unlock()
	if check {
		if nmt, err := c.modTime(); err == nil && !nmt.Equal(mt) {
			if err := c.reload(); err != nil {
				c.log.Error("origin certificate reload failed; keeping the previous one", "err", err)
			} else {
				c.log.Info("origin certificate reloaded", "file", c.certFile)
				c.mu.Lock()
				cur = c.cert
				c.mu.Unlock()
			}
		}
	}
	return cur, nil
}

// Leaf returns the current certificate (for the System page expiry).
func (c *CertReloader) Leaf() *x509.Certificate {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cert.Leaf
}

// ServerTLS builds the public listener TLS config. With an AOP CA file, every connection must present a client
// certificate that chains to it (PL-EDGE-003); the handshake fails otherwise.
func ServerTLS(certs *CertReloader, aopCAFile string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: certs.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
	if aopCAFile != "" {
		pem, err := os.ReadFile(aopCAFile) //nolint:gosec // operator-supplied path
		if err != nil {
			return nil, fmt.Errorf("read AOP CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("AOP CA file contains no certificates")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}
