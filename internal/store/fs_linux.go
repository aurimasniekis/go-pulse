//go:build linux

package store

import (
	"fmt"
	"syscall"
)

// Network filesystems break SQLite locking and durability (PL-DB-006).
var networkFS = map[int64]string{
	0x6969:     "nfs",
	0x517b:     "smb",
	0xff534d42: "cifs",
	0xfe534d42: "smb2",
}

func checkLocalFS(dir string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	if name, bad := networkFS[int64(st.Type)]; bad { //nolint:unconvert // Type width differs per arch
		return fmt.Errorf("store: %s is on a network filesystem (%s); SQLite needs local storage", dir, name)
	}
	return nil
}
