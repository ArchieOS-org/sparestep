package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExplicitEditRootsTrackOnlyStructuredEditedRepositories(t *testing.T) {
	stateDir := retainedTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", stateDir)
	external, _ := committedTestRepo(t, "package external\n")
	readOnly, _ := committedTestRepo(t, "package readonly\n")
	alias := filepath.Join(retainedTestDir(t), "external alias")
	if err := os.Symlink(external, alias); err != nil {
		t.Fatal(err)
	}

	hook(t, stateDir, map[string]any{
		"session_id": "session", "turn_id": "turn", "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "external", "cwd": readOnly,
		"tool_input": map[string]any{"patch": "*** Update File: " + filepath.Join(alias, "app.go") + "\n@@\n"},
	})
	hook(t, stateDir, map[string]any{
		"session_id": "session", "turn_id": "turn", "hook_event_name": "PostToolUse",
		"tool_name": "apply_patch", "tool_use_id": "external", "cwd": readOnly,
		"tool_input":    map[string]any{"patch": "*** Update File: " + filepath.Join(alias, "app.go") + "\n@@\n"},
		"tool_response": map[string]any{"exit_code": 0},
	})
	hook(t, stateDir, map[string]any{
		"session_id": "session", "turn_id": "turn", "hook_event_name": "PreToolUse",
		"tool_name": "Read", "tool_use_id": "read-only", "cwd": readOnly,
		"tool_input": map[string]any{"file_path": filepath.Join(readOnly, "app.go")},
	})
	hook(t, stateDir, map[string]any{
		"session_id": "session", "turn_id": "turn", "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "content", "cwd": readOnly,
		"tool_input": map[string]any{"patch": "ordinary content mentioning " + filepath.Join(readOnly, "app.go")},
	})
	hook(t, stateDir, map[string]any{
		"session_id": "session", "turn_id": "turn", "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "command", "cwd": readOnly,
		"tool_input": map[string]any{"command": "*** Add File: " + filepath.Join(alias, "new", "nested", "app.go") + "\n"},
	})
	hook(t, stateDir, map[string]any{
		"session_id": "session", "turn_id": "turn", "hook_event_name": "PreToolUse",
		"tool_name": "apply_patch", "tool_use_id": "raw", "cwd": readOnly,
		"tool_input": "*** Add File: " + filepath.Join(alias, "raw.go") + "\n",
	})

	registryPath, err := deliveryRootRegistryPath("session")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := loadDeliveryRootRegistry(registryPath, "session")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(external)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Roots) != 1 || registry.Roots[canonical] != 4 {
		t.Fatalf("registry roots = %#v, want only %q", registry.Roots, canonical)
	}
}

func TestExplicitEditRootsRejectTrashComponents(t *testing.T) {
	stateDir := retainedTestDir(t)
	t.Setenv("ONE_SHOT_STATE_DIR", stateDir)
	repo, _ := committedTestRepo(t, "package example\n")
	trashPath := filepath.Join(repo, ".Trashes", "blocked.go")
	hook(t, stateDir, map[string]any{
		"session_id": "session", "turn_id": "turn", "hook_event_name": "PreToolUse",
		"tool_name": "Write", "tool_use_id": "blocked", "cwd": repo,
		"tool_input": map[string]any{"file_path": trashPath},
	})
	registryPath, err := deliveryRootRegistryPath("session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(registryPath); !os.IsNotExist(err) {
		t.Fatalf("Trash path created registry: %v", err)
	}
}

func TestForbiddenSymlinkTargetIsRejectedBeforeGitLookup(t *testing.T) {
	alias := filepath.Join(retainedTestDir(t), "safe-alias")
	// The target is deliberately absent. The resolver must reject its hidden
	// component from the link text, without traversing or reading it.
	if err := os.Symlink(filepath.Join(retainedTestDir(t), ".Trash", "missing"), alias); err != nil {
		t.Fatal(err)
	}
	if _, ok := canonicalEditedRoot(filepath.Join(alias, "new.go"), retainedTestDir(t)); ok {
		t.Fatal("forbidden symlink target was accepted")
	}
}

func TestTouchedRootLockDoesNotAgeSteal(t *testing.T) {
	path := filepath.Join(retainedTestDir(t), "registry.json")
	first, err := acquireDeliveryRootRegistryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first() }()
	acquired := make(chan func() error, 1)
	errs := make(chan error, 1)
	go func() {
		unlock, err := acquireDeliveryRootRegistryLock(path)
		if err != nil {
			errs <- err
			return
		}
		acquired <- unlock
	}()
	time.Sleep(1100 * time.Millisecond)
	select {
	case unlock := <-acquired:
		_ = unlock()
		t.Fatal("registry lock was stolen after one second")
	case err := <-errs:
		t.Fatalf("second lock failed before release: %v", err)
	default:
	}
	if err := first(); err != nil {
		t.Fatal(err)
	}
	select {
	case unlock := <-acquired:
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("second registry lock did not acquire after release")
	}
}
