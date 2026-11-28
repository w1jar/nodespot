//go:build unix

package node

import (
	"os"
	"syscall"
)

// keepOwner gives f the owner and group of old. Only root can
// always do that; for anyone else a refusal is ignored and the file keeps
// the caller's own owner.
func keepOwner(f *os.File, old os.FileInfo) error {
	st, ok := old.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	err := f.Chown(int(st.Uid), int(st.Gid))
	if err != nil && os.Geteuid() != 0 {
		return nil
	}
	return err
}
