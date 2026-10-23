package acme

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/certs"
)

// ServedSerial connects to a NATS client port, reads INFO, upgrades to TLS with the given SNI and returns the serial of
// the leaf the server presents, verified against roots. It never authenticates: the handshake is all it needs.
func ServedSerial(ctx context.Context, addr, serverName string, roots *x509.CertPool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, "INFO ") {
		return "", errors.New("acme: not a NATS server")
	}
	tc := tls.Client(conn, &tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err := tc.HandshakeContext(ctx); err != nil {
		return "", err
	}
	pcs := tc.ConnectionState().PeerCertificates
	if len(pcs) == 0 {
		return "", errors.New("acme: no peer certificate")
	}
	return certs.SerialHex(pcs[0]), nil
}
