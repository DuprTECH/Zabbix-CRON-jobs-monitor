//go:build unix

package crontab

import (
	"os"
	"syscall"
)

func copyOwner(st os.FileInfo, path string) error {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return os.Lchown(path, int(s.Uid), int(s.Gid))
	}
	return nil
}
