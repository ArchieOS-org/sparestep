package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSharedHookRouting(t *testing.T) {
	root := t.TempDir()
	main, linked := filepath.Join(root, "main"), filepath.Join(root, "linked")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	git("init", "-q", main)
	git("-C", main, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	git("-C", main, "worktree", "add", "-qb", "linked", linked)
	if got := SharedHookProject(linked); got != main {
		t.Fatalf("shared source = %q", got)
	}
	if _, ok := ResolveHookProject(main, linked); ok {
		t.Fatal("unregistered worktree routed")
	}
	if _, err := Connect(linked, "/bin/sparestep", filepath.Join(root, "state")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(linked, "packages", "app")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if got, ok := ResolveHookProject(main, nested); !ok || got != linked {
		t.Fatalf("linked route = %q, %v", got, ok)
	}
	if got, ok := ResolveHookProject(main, main); !ok || got != main {
		t.Fatal("main not routed")
	}
	if _, ok := ResolveHookProject(linked, main); ok {
		t.Fatal("reverse cross-project routing")
	}
	other := filepath.Join(main, "unrelated")
	git("init", "-q", other)
	if _, ok := ResolveHookProject(main, other); ok {
		t.Fatal("nested unrelated repo routed")
	}
	if err := Disconnect(linked); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveHookProject(main, nested); ok {
		t.Fatal("disconnected worktree routed")
	}
	if _, ok := ResolveHookProject(main, ""); ok {
		t.Fatal("missing cwd routed")
	}
}

func TestSharedHookProjectFallback(t *testing.T) {
	project := t.TempDir()
	for _, content := range []string{"", "bad metadata", "gitdir: missing", "gitdir: ../.git/modules/submodule"} {
		if err := os.WriteFile(filepath.Join(project, ".git"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if got := SharedHookProject(project); got != project {
			t.Fatalf("invalid gitfile routed to %q", got)
		}
	}
}

func TestSharedConnectionPreservesExistingState(t *testing.T) {
	project := t.TempDir()
	path, err := Connect(project, "/bin/sparestep", "/state/one")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := ConnectShared(project, "/bin/sparestep", "/state/two"); err != ErrSharedConflict {
		t.Fatalf("state conflict: %v", err)
	}
	if _, err := ConnectShared(project, "/other/sparestep", "/state/one"); err != ErrSharedConflict {
		t.Fatalf("binary conflict: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("changed working shared hooks")
	}
	if _, err := ConnectShared(project, "/bin/sparestep", "/state/one"); err != nil {
		t.Fatal(err)
	}
}
