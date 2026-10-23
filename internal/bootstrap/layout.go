// Package bootstrap implements `pulsed init` (PL-KEY-003/004): the sealed key set, operator JWT, nats.conf,
// the bootstrap TLS leaf for 4222 and the banagent token. Every step is idempotent.
package bootstrap

import "path/filepath"

// Layout locates files on the pulsed and nats volumes. Both containers mount the nats volume at the same path,
// so paths written into nats.conf are valid in either container.
type Layout struct {
	DataDir string // pulsed volume (0700)
	NATSDir string // shared with the nats container
}

func (l Layout) KeysFile() string          { return filepath.Join(l.DataDir, "keys.enc") }
func (l Layout) CACertFile() string        { return filepath.Join(l.DataDir, "tls", "ca.crt") }
func (l Layout) BanagentTokenFile() string { return filepath.Join(l.DataDir, "banagent", "token") }
func (l Layout) DatabaseFile() string      { return filepath.Join(l.DataDir, "pulse.db") }
func (l Layout) LockFile() string          { return filepath.Join(l.DataDir, "pulsed.lock") }
func (l Layout) ACMEDir() string           { return filepath.Join(l.DataDir, "acme") }
func (l Layout) NATSConf() string          { return filepath.Join(l.NATSDir, "nats.conf") }
func (l Layout) OperatorJWT() string       { return filepath.Join(l.NATSDir, "operator.jwt") }
func (l Layout) ServerCert() string        { return filepath.Join(l.NATSDir, "tls", "server.crt") }
func (l Layout) ServerKey() string         { return filepath.Join(l.NATSDir, "tls", "server.key") }
func (l Layout) ResolverDir() string       { return filepath.Join(l.NATSDir, "jwt") }
func (l Layout) JetStreamDir() string      { return filepath.Join(l.NATSDir, "js") }
