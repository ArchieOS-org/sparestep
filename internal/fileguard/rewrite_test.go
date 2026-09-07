package fileguard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteOnlyActualRemovalCommands(t *testing.T) {
	helper := "/tools with space/agent-file-guard"
	for _, source := range []string{
		`rm -rf -- "two words" ./*.tmp && echo done`,
		`if test -e "$target"; then /bin/rm -f "$target"; fi`,
		`command -- rm -v -- -leading-dash`,
		`printf '%s' "$(rm -f ./temporary)"`,
	} {
		updated, changed, err := RewriteRemovals(source, helper)
		if err != nil || !changed || !strings.Contains(updated, shellQuote(helper)+" recycle") {
			t.Fatalf("rewrite %q = %q, %v, %v", source, updated, changed, err)
		}
		if destructiveCommand(updated) {
			t.Fatalf("rewritten command still deletes: %s", updated)
		}
	}
	for _, source := range []string{`rg -n 'rm|rmdir|unlink' src`, `printf '%s' 'rm -rf build'`, `cat README.md`, "# rm build\necho keep"} {
		updated, changed, err := RewriteRemovals(source, helper)
		if err != nil || changed || updated != source || destructiveCommand(source) {
			t.Fatalf("read-only command changed or denied: %q", source)
		}
	}
}

func TestHookRewritesRemovalWithoutLosingToolOptions(t *testing.T) {
	for _, surface := range []string{"codex", "claude", "cursor"} {
		event := `{"tool_name":"exec_command","cwd":"/tmp","tool_input":{"cmd":"rm -rf -- 'two words'","workdir":"/tmp","yield_time_ms":1000}}`
		var out bytes.Buffer
		if err := Hook(strings.NewReader(event), &out, surface); err != nil {
			t.Fatal(err)
		}
		var response map[string]any
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		var updated map[string]any
		if surface == "cursor" {
			if response["permission"] != "allow" {
				t.Fatal(response)
			}
			updated = response["updated_input"].(map[string]any)
		} else {
			specific := response["hookSpecificOutput"].(map[string]any)
			if specific["permissionDecision"] != "allow" {
				t.Fatal(response)
			}
			updated = specific["updatedInput"].(map[string]any)
			if !strings.Contains(specific["additionalContext"].(string), "Undo") {
				t.Fatal(response)
			}
		}
		if updated["workdir"] != "/tmp" || updated["yield_time_ms"] != float64(1000) || !strings.Contains(updated["cmd"].(string), " recycle -rf -- 'two words'") {
			t.Fatal(updated)
		}
	}
}

func TestRecycleReportsEverySuccessfulUndoEvenAfterPartialFailure(t *testing.T) {
	dir := fileguardTestDir(t)
	native := filepath.Join(dir, "fake-native")
	// This stand-in tests dispatch/reporting only; native move semantics have
	// separate Swift tests and never use the real Trash during unit testing.
	script := "#!/bin/sh\ncase \"$5\" in *fail) exit 1;; esac\nprintf '%s' '{}' > \"$3\"\n"
	if err := os.WriteFile(native, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	first, failed := filepath.Join(dir, "first"), filepath.Join(dir, "fail")
	for _, path := range []string{first, failed} {
		if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errors bytes.Buffer
	err := recycle([]string{"-fv", first, failed}, &out, &errors, native, filepath.Join(dir, "receipts"))
	if err == nil || strings.Count(out.String(), "Undo:") != 1 || !strings.Contains(out.String(), first) || strings.Contains(out.String(), "Moved to Trash: "+failed) {
		t.Fatalf("err=%v out=%s", err, &out)
	}
}

func TestRecycleForceMissingAndInvalidOptionsDoNotMoveAnything(t *testing.T) {
	dir := fileguardTestDir(t)
	var out bytes.Buffer
	if err := recycle([]string{"-f", filepath.Join(dir, "absent")}, &out, &out, "/not/an/executable", filepath.Join(dir, "receipts")); err != nil || !strings.Contains(out.String(), "No files moved") {
		t.Fatalf("%s %v", &out, err)
	}
	if err := recycle([]string{"--not-supported", dir}, &out, &out, "/not/an/executable", filepath.Join(dir, "receipts")); err == nil {
		t.Fatal("unknown option accepted")
	}
}
