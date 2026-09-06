//go:build darwin

package recovery

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplaceAt atomically fails if destination already exists.
func renameNoReplaceAt(fromDir *os.File, from string, toDir *os.File, to string) error {
	return unix.RenameatxNp(int(fromDir.Fd()), from, int(toDir.Fd()), to, unix.RENAME_EXCL)
}
