package focus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolCannotLeavePlanModeAndAmendmentAnnouncedOnce(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	task, err := s.Begin(BeginInput{Project: root, SessionID: "s", Goal: "task", Paths: []string{"src"}})
	if err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	turn := ""
	call := func(event, permission string, input map[string]any) map[string]any {
		if event == "UserPromptSubmit" {
			turn = "turn-" + permission
			if err := os.WriteFile(rollout, []byte(nativeModeLine(turn, `{"mode":"`+permission+`"}`)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		r, err := s.Evaluate(mustPayload(map[string]any{"hook_event_name": event, "session_id": "s", "cwd": root, "permission_mode": "default", "turn_id": turn, "transcript_path": rollout, "tool_name": "apply_patch", "tool_input": input}), root)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	call("UserPromptSubmit", "plan", nil)
	r := call("PreToolUse", "default", map[string]any{"command": "*** Update File: src/main.go\n"})
	if r["hookSpecificOutput"].(map[string]any)["permissionDecision"] != "deny" {
		t.Fatal(r)
	}
	stored, _ := s.Get(task.ID)
	if stored.Mode != "planning" {
		t.Fatal("tool downgraded Plan mode")
	}
	call("UserPromptSubmit", "default", nil)
	if _, err := s.Amend(task.ID, Amendment{Reason: "shared contract required", Evidence: "src imports shared", Paths: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	if r := call("PostToolUse", "default", nil); r["systemMessage"] != "Scope expanded — shared contract required" {
		t.Fatal(r)
	}
	if r := call("PostToolUse", "default", nil); len(r) != 0 {
		t.Fatal("notice repeated", r)
	}
}

func TestCheckWrapperPreservesOtherRequiredResults(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	task, err := s.Begin(BeginInput{Project: root, SessionID: "s", Goal: "two checks", Criteria: []Criterion{{ID: "one", Kind: "check"}, {ID: "two", Kind: "check"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCheck(task.ID, Check{CriterionID: "one", Status: "pass", Source: "sparestep-exec"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "PreToolUse", "session_id": "s", "cwd": root, "tool_name": "Bash", "tool_input": map[string]any{"command": "sparestep focus check --criterion two -- go test ./..."}}), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCheck(task.ID, Check{CriterionID: "two", Status: "pass", Source: "sparestep-exec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(task.ID, "completed"); err != nil {
		t.Fatal(err)
	}
}
