//go:build unix

package system

import (
	"os"
	"syscall"
)

// matchOwner gives path the owner of like when running as root, so that
// copies made through sudo stay readable by the user whose data they are.
func matchOwner(path string, like os.FileInfo) {
	if os.Geteuid() != 0 || like == nil {
		return
	}
	if st, ok := like.Sys().(*syscall.Stat_t); ok {
		_ = os.Lchown(path, int(st.Uid), int(st.Gid))
	}
}
