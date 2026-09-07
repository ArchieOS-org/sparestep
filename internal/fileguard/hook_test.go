package fileguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileguardTestDir(t *testing.T) string {
	t.Helper()
	if os.Getenv("SHIP_IT_KEEP_TEST_DIRS") == "" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("", "one-shot-tally-fileguard-test-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDenyDeletionAcrossToolShapes(t *testing.T) {
	if got := Inspect("Bash", map[string]any{"command": "rm -f build"}, fileguardTestDir(t)); got != removalAdvice {
		t.Fatalf("removal advice = %q, want %q", got, removalAdvice)
	}
	for _, command := range []string{"rm -rf build", "/bin/rm -f file", "sudo /bin/rm -rf /", "find . -type f -delete", "python3 -c 'import shutil; shutil.rmtree(\"data\")'", "node -e 'fs.rmSync(\"data\", {recursive:true})'", "osascript -e 'tell application \"Finder\" to empty trash'", "ls ~/.Trash", "cat /Volumes/drive/.Trashes/501/file", "rsync --delete src/ dst/", "git clean -fdx", "bash"} {
		t.Run(command, func(t *testing.T) {
			if Inspect("Bash", map[string]any{"command": command}, fileguardTestDir(t)) == "" {
				t.Fatal("dangerous command accepted")
			}
		})
	}
	if Inspect("mcp__filesystem__delete_directory", map[string]any{"path": "build"}, fileguardTestDir(t)) == "" {
		t.Fatal("MCP deletion accepted")
	}
	if Inspect("exec_command", map[string]any{"cmd": "cat", "tty": true}, fileguardTestDir(t)) == "" {
		t.Fatal("interactive transport accepted")
	}
}

func TestSafeCommandsAndRecoveryRemainAvailable(t *testing.T) {
	for _, command := range []string{"go test ./...", "rg TODO src", "mkdir -p output", "agent-file-guard remove -- ./build", "agent-file-guard restore -- ./saved/receipt.json", "git status --short"} {
		if reason := Inspect("Bash", map[string]any{"command": command}, fileguardTestDir(t)); reason != "" {
			t.Fatalf("%s: %s", command, reason)
		}
	}
	if reason := Inspect("Read", map[string]any{"file_path": "README.md", "content": "Discuss rm and ~/.Trash safety"}, fileguardTestDir(t)); reason != "" {
		t.Fatal("documentation mistaken for execution:", reason)
	}
	if reason := Inspect("apply_patch", map[string]any{"command": "*** Begin Patch\n*** Update File: guard.go\n+// Block rm and ~/.Trash\n*** End Patch"}, fileguardTestDir(t)); reason != "" {
		t.Fatal("source edit mistaken for execution:", reason)
	}
	if Inspect("apply_patch", map[string]any{"command": "*** Begin Patch\n*** Delete File: important.txt\n*** End Patch"}, fileguardTestDir(t)) == "" {
		t.Fatal("permanent patch deletion accepted")
	}
}

func TestTrashSymlinkAliasRejectedWithoutFollowingTarget(t *testing.T) {
	dir := fileguardTestDir(t)
	// Dangling fake Trash: no real Trash directory is accessed or created.
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(filepath.Join(dir, ".Trash"), alias); err != nil {
		t.Fatal(err)
	}
	for _, input := range []any{map[string]any{"path": alias}, map[string]any{"command": "ls " + alias}} {
		if Inspect("Read", input, dir) == "" {
			t.Fatal("Trash alias accepted")
		}
	}
}

func TestHookProtocolsAndMalformedInput(t *testing.T) {
	for _, surface := range []string{"codex", "claude", "cursor-shell"} {
		for _, input := range []string{`{"tool_name":"Bash","tool_input":{"command":"find . -delete"}}`, `{`} {
			var out bytes.Buffer
			if err := Hook(strings.NewReader(input), &out, surface); err != nil {
				t.Fatal(err)
			}
			var response map[string]any
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if surface == "cursor-shell" {
				if response["permission"] != "deny" {
					t.Fatal(response)
				}
			} else if response["hookSpecificOutput"].(map[string]any)["permissionDecision"] != "deny" {
				t.Fatal(response)
			}
		}
	}
	var out bytes.Buffer
	if err := Hook(strings.NewReader(`{"command":"go test ./..."}`), &out, "cursor"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"permission":"allow"`) {
		t.Fatal("Cursor allow response missing:", out.String())
	}
}
