package focus

import "testing"

func TestOperationParserKeepsReadOnlyCommandsNarrow(t *testing.T) {
	for _, command := range []string{"git status", "git diff --stat", "go test ./internal/focus", "cat README.md"} {
		op := inspectShell(command)
		if op.unknown || op.write {
			t.Fatalf("read-only command %q classified as %+v", command, op)
		}
	}
	for _, command := range []string{"git status && rm generated.txt", "cat file | sed -n '1p'", "go test ./...; touch marker"} {
		op := inspectShell(command)
		if !op.unknown || !op.write {
			t.Fatalf("compound/unsupported command %q was treated as read-only: %+v", command, op)
		}
	}
}

func TestApplyPatchOperationIncludesMovesAndRejectsOpaquePatch(t *testing.T) {
	op := inspectOperation("apply_patch", map[string]any{"command": "*** Update File: internal/a.go\n*** Move to: internal/b.go\n"})
	if op.unknown || !op.write || len(op.paths) != 2 || op.paths[1] != "internal/b.go" {
		t.Fatalf("move=%+v", op)
	}
	op = inspectOperation("apply_patch", map[string]any{"command": "opaque patch content"})
	if !op.unknown || !op.write {
		t.Fatalf("opaque patch=%+v", op)
	}
}
