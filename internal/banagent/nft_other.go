//go:build !linux

package banagent

import (
	"errors"
	"time"
)

// NFT is only available on Linux.
type NFT struct {
	Table string
	SetV4 string
	SetV6 string
}

var errNotLinux = errors.New("banagent: nftables needs Linux")

// List is unsupported off Linux.
func (n *NFT) List() ([]Element, error) { return nil, errNotLinux }

// Apply is unsupported off Linux.
func (n *NFT) Apply([]Element, []Element, time.Time) error { return errNotLinux }
