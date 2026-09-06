// Package recovery moves unsafe-to-delete paths into adjacent, reversible storage.
package recovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	recoveryDirectory = ".agent-recovery"
	receiptName       = "receipt.json"
	payloadName       = "payload"
	ignoreName        = ".gitignore"
	ignoreContents    = "*\n"
	maxSymlinkHops    = 32
)

type receipt struct {
	Version      int       `json:"version"`
	OriginalPath string    `json:"original_path"`
	OriginalDir  string    `json:"original_dir"`
	ParentDevice uint64    `json:"parent_device"`
	ParentInode  uint64    `json:"parent_inode"`
	StoredAt     time.Time `json:"stored_at"`
	State        string    `json:"state"`
	RestoredAt   time.Time `json:"restored_at,omitempty"`
}

// Remove atomically moves path to its parent/.agent-recovery storage and
// returns the receipt that Restore accepts. It never copies or deletes data.
func Remove(path string) (string, error) {
	target, parent, err := safeTarget(path)
	if err != nil {
		return "", err
	}
	if err := rejectProtected(target); err != nil {
		return "", err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("refusing to remove a symlink")
	}
	if err := sameFilesystem(target, parent); err != nil {
		return "", err
	}
	device, inode, err := directoryIdentity(parent)
	if err != nil {
		return "", err
	}
	recoveryRoot, err := reserveRecoveryRoot(parent)
	if err != nil {
		return "", err
	}
	dir, err := reserveReceiptDirectory(recoveryRoot)
	if err != nil {
		return "", err
	}
	receiptPath := filepath.Join(dir, receiptName)
	entry := receipt{
		Version:      1,
		OriginalPath: target,
		OriginalDir:  parent,
		ParentDevice: device,
		ParentInode:  inode,
		StoredAt:     time.Now().UTC(),
		State:        "stored",
	}
	if err := writeReceipt(receiptPath, entry); err != nil {
		return "", err
	}
	fromDir, err := openDirectory(parent)
	if err != nil {
		return "", err
	}
	defer fromDir.Close()
	toDir, err := openDirectory(dir)
	if err != nil {
		return "", err
	}
	defer toDir.Close()
	if err := renameNoReplaceAt(fromDir, filepath.Base(target), toDir, payloadName); err != nil {
		return "", fmt.Errorf("move to recovery storage: %w", err)
	}
	return receiptPath, nil
}

// Restore atomically returns a recovered payload to its original missing path.
func Restore(receiptPath string) error {
	receiptFile, entry, storage, err := loadReceipt(receiptPath)
	if err != nil {
		return err
	}
	if entry.State != "stored" || entry.Version != 1 {
		return errors.New("recovery receipt is not restorable")
	}
	target, parent, err := safeTarget(entry.OriginalPath)
	if err != nil {
		return err
	}
	if err := rejectProtected(target); err != nil {
		return err
	}
	if parent != entry.OriginalDir {
		return errors.New("original parent changed since removal")
	}
	device, inode, err := directoryIdentity(parent)
	if err != nil {
		return err
	}
	if device != entry.ParentDevice || inode != entry.ParentInode {
		return errors.New("original parent changed since removal")
	}
	if filepath.Dir(storage) != filepath.Join(parent, recoveryDirectory) || filepath.Base(storage) == "" {
		return errors.New("recovery receipt is outside the original sibling storage")
	}
	payload := filepath.Join(storage, payloadName)
	info, err := os.Lstat(payload)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("recovery payload must not be a symlink")
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := sameFilesystem(payload, parent); err != nil {
		return err
	}
	fromDir, err := openDirectory(storage)
	if err != nil {
		return err
	}
	defer fromDir.Close()
	toDir, err := openDirectory(parent)
	if err != nil {
		return err
	}
	defer toDir.Close()
	if device, inode, err := directoryIdentityFile(toDir); err != nil || device != entry.ParentDevice || inode != entry.ParentInode {
		return errors.New("original parent changed since removal")
	}
	if err := renameNoReplaceAt(fromDir, payloadName, toDir, filepath.Base(target)); err != nil {
		return fmt.Errorf("restore payload: %w", err)
	}
	entry.State = "restored"
	entry.RestoredAt = time.Now().UTC()
	return writeReceipt(receiptFile, entry)
}

func safeTarget(path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", errors.New("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	if containsUnsafeComponent(abs) {
		return "", "", errors.New("refusing Trash or recovery storage path")
	}
	parent, err := resolveParent(abs, false)
	if err != nil {
		return "", "", err
	}
	target := filepath.Join(parent, filepath.Base(abs))
	if containsUnsafeComponent(target) {
		return "", "", errors.New("refusing Trash or recovery storage path")
	}
	return target, parent, nil
}

// resolveParentNoTrash resolves only ancestors. The final component remains
// unresolved so callers can reject a symlink target instead of following it.
func resolveParent(path string, allowRecovery bool) (string, error) {
	return resolveParentDepth(path, allowRecovery, 0)
}

func resolveParentDepth(path string, allowRecovery bool, hops int) (string, error) {
	if hops > maxSymlinkHops {
		return "", errors.New("too many symlink ancestors")
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", errors.New("path must resolve to an absolute path")
	}
	parts := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) < 1 || parts[0] == "" {
		return "", errors.New("refusing filesystem root")
	}
	parts = parts[:len(parts)-1]
	current := string(filepath.Separator)
	for index := 0; index < len(parts); index++ {
		part := parts[index]
		if forbiddenComponent(part, allowRecovery) {
			return "", errors.New("refusing Trash or recovery storage path")
		}
		candidate := filepath.Join(current, part)
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.IsDir() {
				return "", fmt.Errorf("path ancestor is not a directory: %s", candidate)
			}
			current = candidate
			continue
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(candidate), target)
		}
		target = filepath.Clean(target)
		if containsForbiddenComponent(target, allowRecovery) {
			return "", errors.New("refusing symlink path into Trash or recovery storage")
		}
		remaining := append(strings.Split(strings.TrimPrefix(target, string(filepath.Separator)), string(filepath.Separator)), parts[index+1:]...)
		return resolveParentDepth(filepath.Join(append([]string{string(filepath.Separator)}, append(remaining, filepath.Base(clean))...)...), allowRecovery, hops+1)
	}
	return current, nil
}

func rejectProtected(target string) error {
	protected := []string{string(filepath.Separator)}
	if home, err := os.UserHomeDir(); err == nil {
		if parent, resolveErr := resolveParent(home, false); resolveErr == nil {
			protected = append(protected, filepath.Join(parent, filepath.Base(home)))
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if parent, resolveErr := resolveParent(cwd, false); resolveErr == nil {
			protected = append(protected, filepath.Join(parent, filepath.Base(cwd)))
		}
	}
	for _, path := range protected {
		if sameOrAncestor(target, path) {
			return errors.New("refusing protected path or its ancestor")
		}
	}
	return nil
}

func sameOrAncestor(path, protected string) bool {
	relative, err := filepath.Rel(path, protected)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func containsUnsafeComponent(path string) bool {
	return containsForbiddenComponent(path, false)
}

func containsForbiddenComponent(path string, allowRecovery bool) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if forbiddenComponent(part, allowRecovery) {
			return true
		}
	}
	return false
}

func unsafeComponent(part string) bool {
	return forbiddenComponent(part, false)
}

func forbiddenComponent(part string, allowRecovery bool) bool {
	if strings.EqualFold(part, ".trash") || strings.EqualFold(part, ".trashes") {
		return true
	}
	return !allowRecovery && strings.EqualFold(part, recoveryDirectory)
}

func reserveRecoveryRoot(parent string) (string, error) {
	root := filepath.Join(parent, recoveryDirectory)
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	} else {
		if err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("recovery storage is unsafe")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	if err := ensureIgnore(root); err != nil {
		return "", err
	}
	return root, nil
}

func ensureIgnore(root string) error {
	path := filepath.Join(root, ignoreName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(createErr, os.ErrExist) {
			return ensureIgnore(root)
		}
		if createErr != nil {
			return createErr
		}
		if _, writeErr := file.WriteString(ignoreContents); writeErr != nil {
			_ = file.Close()
			return writeErr
		}
		return file.Close()
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("recovery .gitignore is unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(data) != ignoreContents {
		return errors.New("recovery .gitignore must ignore all contents")
	}
	return nil
}

func reserveReceiptDirectory(root string) (string, error) {
	for range 8 {
		id, err := randomID()
		if err != nil {
			return "", err
		}
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not reserve recovery receipt")
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func loadReceipt(path string) (string, receipt, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", receipt{}, "", errors.New("recovery receipt path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil || containsForbiddenComponent(abs, true) {
		return "", receipt{}, "", errors.New("recovery receipt path is unsafe")
	}
	parent, err := resolveParent(abs, true)
	if err != nil {
		return "", receipt{}, "", err
	}
	file := filepath.Join(parent, filepath.Base(abs))
	if filepath.Base(file) != receiptName {
		return "", receipt{}, "", errors.New("recovery receipt must be receipt.json")
	}
	info, err := os.Lstat(file)
	if err != nil {
		return "", receipt{}, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", receipt{}, "", errors.New("recovery receipt is unsafe")
	}
	if filepath.Base(parent) == recoveryDirectory {
		return "", receipt{}, "", errors.New("recovery receipt is missing its reservation directory")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", receipt{}, "", err
	}
	var entry receipt
	if err := json.Unmarshal(data, &entry); err != nil {
		return "", receipt{}, "", err
	}
	return file, entry, parent, nil
}

func writeReceipt(path string, entry receipt) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func sameFilesystem(path, parent string) error {
	pathInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return err
	}
	pathStat, pathOK := pathInfo.Sys().(*syscall.Stat_t)
	parentStat, parentOK := parentInfo.Sys().(*syscall.Stat_t)
	if !pathOK || !parentOK || pathStat.Dev != parentStat.Dev {
		return errors.New("cross-filesystem recovery move is refused")
	}
	return nil
}

func directoryIdentity(path string) (uint64, uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	return identityFromInfo(info)
}

func identityFromInfo(info os.FileInfo) (uint64, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return 0, 0, errors.New("original parent is unsafe")
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
