package bootstrap

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/natsconf"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
)

// BootstrapLeafTTL is the validity of the internal-CA leaf for 4222; it is renewed when less than RenewBefore remains.
const (
	BootstrapLeafTTL = 90 * 24 * time.Hour
	RenewBefore      = 30 * 24 * time.Hour
)

// Options configure Init.
type Options struct {
	Layout          Layout
	MasterKey       []byte
	RawHost         string   // public raw NATS hostname (SAN on the bootstrap leaf), e.g. r.events.wan.lt
	ExtraSANs       []string // additional names/IPs for the leaf
	ClientListen    string
	MonitorListen   string
	WebsocketListen string
	MaxFileStore    int64
	Now             func() time.Time
}

// Result lists what Init changed (empty when nothing changed).
type Result struct {
	Changed []string
}

// LeafNames are the SANs of the bootstrap leaf: compose service name, loopback, and the raw hostname.
func (o Options) LeafNames() []string {
	names := []string{"nats", "localhost", "127.0.0.1", "::1"}
	if o.RawHost != "" {
		names = append(names, o.RawHost)
	}
	for _, n := range o.ExtraSANs {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// Init creates or completes the bootstrap state. It never rotates existing keys (PL-KEY-003).
func Init(o Options) (*Result, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	l := o.Layout
	res := &Result{}
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{
		{l.DataDir, 0o700}, {filepath.Dir(l.CACertFile()), 0o700}, {filepath.Dir(l.BanagentTokenFile()), 0o700},
		{l.NATSDir, 0o755}, {filepath.Dir(l.ServerCert()), 0o700},
	} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return nil, err
		}
	}

	ks, err := LoadKeySet(l.KeysFile(), o.MasterKey)
	if errors.Is(err, ErrNoKeySet) {
		ks, err = &KeySet{}, nil
	}
	if err != nil {
		return nil, err
	}
	changed, err := ks.fill(now())
	if err != nil {
		return nil, err
	}
	if changed {
		if err := ks.Save(l.KeysFile(), o.MasterKey); err != nil {
			return nil, err
		}
		res.Changed = append(res.Changed, l.KeysFile())
	}

	write := func(path string, data []byte, mode os.FileMode) error {
		cur, err := os.ReadFile(path)
		if err == nil && bytes.Equal(cur, data) {
			return nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := secretbox.WriteFileAtomic(path, data, mode); err != nil {
			return err
		}
		res.Changed = append(res.Changed, path)
		return nil
	}

	if err := write(l.CACertFile(), []byte(ks.CACert), 0o644); err != nil {
		return nil, err
	}
	if err := write(l.OperatorJWT(), []byte(ks.OperatorJWT), 0o644); err != nil {
		return nil, err
	}
	var extra []natsconf.CertPair
	if fileExists(l.ACMECert()) && fileExists(l.ACMEKey()) {
		extra = append(extra, natsconf.CertPair{CertFile: l.ACMECert(), KeyFile: l.ACMEKey()})
	}
	conf, err := natsconf.Render(natsconf.Params{
		ExtraCerts:      extra,
		ClientListen:    o.ClientListen,
		MonitorListen:   o.MonitorListen,
		WebsocketListen: o.WebsocketListen,
		OperatorJWTPath: l.OperatorJWT(),
		SystemAccount:   ks.Pub(ks.SysAccount),
		SysAccountJWT:   ks.SysAccountJWT,
		ResolverDir:     l.ResolverDir(),
		JetStreamDir:    l.JetStreamDir(),
		MaxFileStore:    o.MaxFileStore,
		TLSCertFile:     l.ServerCert(),
		TLSKeyFile:      l.ServerKey(),
	})
	if err != nil {
		return nil, err
	}
	if err := write(l.NATSConf(), conf, 0o644); err != nil {
		return nil, err
	}

	ca, err := ks.CA()
	if err != nil {
		return nil, err
	}
	need, err := needsBootstrapLeaf(l, ca, o.LeafNames(), now())
	if err != nil {
		return nil, err
	}
	if need {
		leaf, err := ca.IssueServer(o.LeafNames(), BootstrapLeafTTL, now())
		if err != nil {
			return nil, err
		}
		// Key first: NATS reloads both, and a new cert with an old key would fail to load.
		if err := secretbox.WriteFileAtomic(l.ServerKey(), leaf.KeyPEM, 0o600); err != nil {
			return nil, err
		}
		if err := secretbox.WriteFileAtomic(l.ServerCert(), leaf.CertPEM, 0o644); err != nil {
			return nil, err
		}
		res.Changed = append(res.Changed, l.ServerCert(), l.ServerKey())
	}

	if _, err := os.Stat(l.BanagentTokenFile()); errors.Is(err, fs.ErrNotExist) {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := write(l.BanagentTokenFile(), []byte(base64.RawURLEncoding.EncodeToString(b)), 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return res, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// needsBootstrapLeaf reports whether the 4222 certificate must be (re)issued from the internal CA. A certificate that
// was not issued by the internal CA (the ACME certificate) is left alone; ACME renews it (M9).
func needsBootstrapLeaf(l Layout, ca *certs.CA, names []string, now time.Time) (bool, error) {
	certPEM, err := os.ReadFile(l.ServerCert())
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(l.ServerKey()); errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	cert, err := certs.ParseLeaf(certPEM)
	if err != nil {
		return true, nil //nolint:nilerr // an unparsable certificate is replaced
	}
	if !ca.IssuedBy(cert) {
		return false, nil
	}
	if cert.NotAfter.Sub(now) < RenewBefore {
		return true, nil
	}
	return !coversNames(cert, names), nil
}

// coversNames reports whether every name (DNS or IP literal) is a SAN of cert.
func coversNames(cert *x509.Certificate, names []string) bool {
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			if !slices.ContainsFunc(cert.IPAddresses, ip.Equal) {
				return false
			}
		} else if !slices.Contains(cert.DNSNames, n) {
			return false
		}
	}
	return true
}
