//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { //nolint:gosec // fd fits in int
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, path)
		}
		return nil, err
	}
	return f, nil
}

func releaseLock(f *os.File) error {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // fd fits in int
	return f.Close()
}
