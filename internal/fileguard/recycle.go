package fileguard

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Recycle accepts ordinary removal arguments after the shell has expanded them.
// Each item gets a separate receipt, including when another item later fails.
func Recycle(args []string, out, errOut io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	native := filepath.Join(home, ".local", "libexec", "agent-native-trash")
	dir := filepath.Join(home, ".local", "state", "agent-file-guard", "receipts")
	return recycle(args, out, errOut, native, dir)
}

func recycle(args []string, out, errOut io.Writer, native, receiptDir string) error {
	force, recursive, dirs, options := false, false, false, true
	var paths []string
	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(arg, "-") && arg != "-" {
			switch arg {
			case "--force":
				force = true
			case "--recursive":
				recursive = true
			case "--dir":
				dirs = true
			case "--verbose", "--preserve-root":
			default:
				if strings.HasPrefix(arg, "--") {
					return fmt.Errorf("unsupported removal option %q; no files moved", arg)
				}
				for _, flag := range arg[1:] {
					switch flag {
					case 'f':
						force = true
					case 'r', 'R':
						recursive = true
					case 'd':
						dirs = true
					case 'v':
					default:
						return fmt.Errorf("unsupported removal option -%c; no files moved", flag)
					}
				}
			}
			continue
		}
		paths = append(paths, arg)
	}
	if len(paths) == 0 && !force {
		return errors.New("a removal path is required")
	}
	var failures []error
	moved := 0
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		// Check the parent only: moving a symlink removes the link, not its target.
		if TrashPath(abs) || pathReason(filepath.Dir(abs), "") != "" {
			failures = append(failures, fmt.Errorf("cannot move protected path %q", path))
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil {
			if force && errors.Is(err, os.ErrNotExist) {
				continue
			}
			failures = append(failures, err)
			continue
		}
		if info.IsDir() && !recursive {
			if !dirs {
				failures = append(failures, fmt.Errorf("%q is a directory; use -r", path))
				continue
			}
			entries, readErr := os.ReadDir(abs)
			if readErr != nil || len(entries) != 0 {
				failures = append(failures, fmt.Errorf("%q is not an empty readable directory", path))
				continue
			}
		}
		if err := os.MkdirAll(receiptDir, 0700); err != nil {
			failures = append(failures, err)
			continue
		}
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			failures = append(failures, err)
			continue
		}
		receipt := filepath.Join(receiptDir, hex.EncodeToString(id)+".json")
		cmd := exec.Command(native, "move", "--receipt", receipt, "--", abs)
		cmd.Stderr = errOut
		if output, err := cmd.Output(); err != nil {
			fmt.Fprintf(errOut, "Move to Trash failed for %q; receipt (if created): %s\n%s", path, receipt, output)
			failures = append(failures, fmt.Errorf("move %q to Trash: %w", path, err))
			continue
		}
		moved++
		fmt.Fprintf(out, "Moved to Trash: %s\nUndo: %s restore --receipt %s\n", abs, shellQuote(native), shellQuote(receipt))
	}
	if moved == 0 && len(failures) == 0 {
		fmt.Fprintln(out, "No files moved to Trash.")
	}
	return errors.Join(failures...)
}
