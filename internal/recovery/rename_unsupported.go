//go:build !darwin && !linux

package recovery

import (
	"errors"
	"os"
)

// Unsupported platforms fail closed because os.Rename can overwrite a race.
func renameNoReplaceAt(fromDir *os.File, from string, toDir *os.File, to string) error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}
