package fileguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallHooksPreservesExistingHandlersAndIsIdempotent(t *testing.T) {
	for _, surface := range []string{"codex", "claude", "cursor"} {
		t.Run(surface, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			original := []byte(`{"other_setting":true,"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"command":"existing-tally","type":"command"}]}]}}`)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := installHooks(path, "/safe/bin/agent-file-guard", surface); err != nil {
				t.Fatal(err)
			}
			first, _ := os.ReadFile(path)
			if err := installHooks(path, "/safe/bin/agent-file-guard", surface); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(path)
			if string(first) != string(second) {
				t.Fatal("installer duplicates or rewrites guards on repeat")
			}
			var config map[string]any
			if err := json.Unmarshal(second, &config); err != nil {
				t.Fatal(err)
			}
			if config["other_setting"] != true {
				t.Fatal("unrelated setting lost")
			}
			hooks := config["hooks"].(map[string]any)
			if surface == "cursor" {
				for _, event := range []string{"preToolUse", "beforeShellExecution", "beforeMCPExecution", "beforeReadFile", "beforeTabFileRead"} {
					entries := hooks[event].([]any)
					if len(entries) != 1 || entries[0].(map[string]any)["failClosed"] != true {
						t.Fatalf("unguarded event %s", event)
					}
				}
			} else if len(hooks["PreToolUse"].([]any)) != 2 {
				t.Fatal("existing handler lost or guard missing")
			}
		})
	}
}
