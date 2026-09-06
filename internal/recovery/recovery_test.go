package recovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveRestoreDirectoryRoundTrip(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "bundle")
	file := filepath.Join(target, "nested", "proof.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("recover me"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath, err := Remove(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("target still exists: %v", err)
	}
	if !strings.Contains(receiptPath, string(filepath.Separator)+recoveryDirectory+string(filepath.Separator)) {
		t.Fatalf("receipt stored outside sibling recovery directory: %q", receiptPath)
	}
	for _, path := range []string{filepath.Dir(filepath.Dir(receiptPath)), filepath.Dir(receiptPath), receiptPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("recovery permissions for %q = %v, %v", path, info.Mode(), err)
		}
	}
	ignore := filepath.Join(filepath.Dir(filepath.Dir(receiptPath)), ignoreName)
	if data, err := os.ReadFile(ignore); err != nil || string(data) != ignoreContents {
		t.Fatalf("recovery ignore = %q, %v", data, err)
	}
	if err := Restore(receiptPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "recover me" {
		t.Fatalf("restored payload = %q, %v", data, err)
	}
	if err := Restore(receiptPath); err == nil {
		t.Fatal("restored receipt was accepted twice")
	}
}

func TestRestoreRejectsExistingDestination(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptPath, err := Remove(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("new destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(receiptPath); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination restore = %v", err)
	}
	payload := filepath.Join(filepath.Dir(receiptPath), payloadName)
	if data, err := os.ReadFile(payload); err != nil || string(data) != "original" {
		t.Fatalf("payload was overwritten or lost: %q, %v", data, err)
	}
}

func TestRemoveRejectsProtectedPaths(t *testing.T) {
	paths := []string{string(filepath.Separator)}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, home)
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, cwd)
	}
	for _, path := range paths {
		if _, err := Remove(path); err == nil {
			t.Fatalf("protected path %q removal = %v", path, err)
		}
	}
}

func TestRemoveRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink removal = %v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestRenameNoReplacePreservesConcurrentDestination(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "payload")
	to := filepath.Join(dir, "destination")
	if err := os.WriteFile(from, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("racing destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(from, to); err == nil {
		t.Fatal("no-replace rename overwrote an existing destination")
	}
	if data, err := os.ReadFile(to); err != nil || string(data) != "racing destination" {
		t.Fatalf("existing destination was overwritten: %q, %v", data, err)
	}
	if data, err := os.ReadFile(from); err != nil || string(data) != "payload" {
		t.Fatalf("source disappeared after failed no-replace rename: %q, %v", data, err)
	}
}

func TestRemoveRejectsUnsafeExistingRecoveryIgnore(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, recoveryDirectory)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ignoreName), []byte("!keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(target); err == nil || !strings.Contains(err.Error(), "ignore") {
		t.Fatalf("unsafe recovery ignore removal = %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("target moved despite unsafe recovery storage: %q, %v", data, err)
	}
}

func TestRemoveBoundsSymlinkAncestorCycles(t *testing.T) {
	parent := t.TempDir()
	if err := os.Symlink("b", filepath.Join(parent, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(parent, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(filepath.Join(parent, "a", "target")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink cycle removal = %v", err)
	}
}
