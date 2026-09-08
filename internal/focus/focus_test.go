package focus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "focus.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestLifecycleCriteriaAmendmentAndCrossSession(t *testing.T) {
	s := testStore(t)
	task, e := s.Begin(BeginInput{Project: "p", SessionID: "s1", ThreadID: "root", Goal: "ship", Mode: "implementing", Paths: []string{"internal/focus"}, Criteria: []Criterion{{ID: "tests", Description: "tests pass", Kind: "check", Status: "pass"}}})
	if e != nil {
		t.Fatal(e)
	}
	if task.Criteria[0].Status != "pending" {
		t.Fatalf("initial status=%q", task.Criteria[0].Status)
	}
	if _, e = s.Current("p", "s2"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("cross-session current=%v", e)
	}
	if e = s.RecordCheck(task.ID, Check{CriterionID: "tests", Status: "pass", Source: "sparestep-exec", Evidence: "go test"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Finish(task.ID, "completed"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Amend(task.ID, Amendment{Reason: "late change", Evidence: "review requested", Paths: []string{"docs"}}); !errors.Is(e, ErrInactive) {
		t.Fatalf("amend completed=%v", e)
	}

	second, _ := s.Begin(BeginInput{Project: "p", SessionID: "s1", Goal: "second", Mode: "implementing", Criteria: []Criterion{{ID: "review", Kind: "review"}}})
	if e = s.RecordCheck(second.ID, Check{CriterionID: "review", Status: "pass", Source: "agent-review", Evidence: "reviewed"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Amend(second.ID, Amendment{CriterionID: "review", Reason: "scope changed", Evidence: "new requirement", Paths: []string{"docs"}}); e != nil {
		t.Fatal(e)
	}
	got, _ := s.Get(second.ID)
	if got.Criteria[0].Status != "pending" || got.Checks[0].Status != "invalidated" {
		t.Fatalf("amend did not invalidate: %+v", got)
	}
	if _, e = s.Finish(second.ID, "completed"); !errors.Is(e, ErrUnmetCriteria) {
		t.Fatalf("finish stale=%v", e)
	}
}

func TestFocusPersistsAcrossOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "focus.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Begin(BeginInput{Project: "p", SessionID: "s", Goal: "persist", Mode: "implementing"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get(task.ID)
	if err != nil || got.Goal != "persist" {
		t.Fatalf("persisted task=%+v err=%v", got, err)
	}
	if err = s.RequestCompletion(task.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(task.ID)
	if err != nil || !got.CompletionRequested {
		t.Fatalf("completion request=%+v err=%v", got, err)
	}
	if _, err = s.Finish(task.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestForSession("p", "s")
	if err != nil || latest.Status != "completed" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestCrossProcessRevisionCAS(t *testing.T) {
	p := filepath.Join(t.TempDir(), "focus.db")
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer b.Close()
	task, err := a.Begin(BeginInput{Project: "p", SessionID: "s", Goal: "cas", Mode: "implementing"})
	if err != nil {
		t.Fatal(err)
	}
	left, err := a.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	right, err := b.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	left.Goal, right.Goal = "left", "right"
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	for _, pair := range []struct {
		s *Store
		t Task
	}{{a, *left}, {b, *right}} {
		go func(pair struct {
			s *Store
			t Task
		}) {
			defer wg.Done()
			<-start
			errs <- pair.s.saveLocked(pair.t)
		}(pair)
	}
	close(start)
	wg.Wait()
	close(errs)
	conflicts := 0
	successes := 0
	for e := range errs {
		if errors.Is(e, ErrConflict) {
			conflicts++
		} else if e == nil {
			successes++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS results successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestNativePolicyBoundariesPlanningCompactionAndStop(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	task, e := s.Begin(BeginInput{Project: root, SessionID: "sess", Goal: "edit focus", Mode: "implementing", Paths: []string{"internal/focus"}, Criteria: []Criterion{{ID: "verify", Description: "verification passes", Kind: "check"}}})
	if e != nil {
		t.Fatal(e)
	}
	patch := func(path string) []byte {
		return mustPayload(map[string]any{"hook_event_name": "PreToolUse", "session_id": "sess", "cwd": root, "tool_name": "apply_patch", "tool_input": map[string]any{"command": "*** Update File: " + path + "\n"}})
	}
	r, e := s.Evaluate(patch("internal/focus/x.go"), root)
	if e != nil || len(r) != 0 {
		t.Fatalf("in boundary=%v %v", r, e)
	}
	r, e = s.Evaluate(patch("outside/x.go"), root)
	if e != nil || r["hookSpecificOutput"].(map[string]any)["permissionDecision"] != "deny" {
		t.Fatalf("out boundary=%v %v", r, e)
	}
	r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "SessionStart", "source": "compact", "session_id": "sess", "cwd": root}), root)
	if e != nil || r["hookSpecificOutput"].(map[string]any)["additionalContext"] == "" {
		t.Fatalf("compact=%v %v", r, e)
	}
	r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "PostCompact", "session_id": "sess", "cwd": root}), root)
	if e != nil {
		t.Fatal(e)
	}
	if len(r) != 0 {
		t.Fatalf("postcompact=%v", r)
	}
	r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "PreToolUse", "session_id": "sess", "cwd": root, "permission_mode": "plan", "tool_name": "apply_patch", "tool_input": map[string]any{"command": "*** Update File: internal/focus/x.go\n"}}), root)
	if e != nil || r["hookSpecificOutput"].(map[string]any)["permissionDecision"] != "deny" {
		t.Fatalf("plan=%v %v", r, e)
	}
	planTask, e := s.Begin(BeginInput{Project: root, SessionID: "plan", Goal: "plan only", Mode: "planning", Criteria: []Criterion{{ID: "plan-review", Description: "review plan", Kind: "review"}}})
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "Stop", "session_id": "plan", "cwd": root}), root)
	if e != nil || len(r) != 0 {
		t.Fatalf("planning stop=%v %v", r, e)
	}
	_ = planTask
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte(nativeModeLine("resume", `{"mode":"default"}`)), 0600); err != nil {
		t.Fatal(err)
	}
	if r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "UserPromptSubmit", "permission_mode": "default", "turn_id": "resume", "transcript_path": rollout, "session_id": "sess", "cwd": root}), root); e != nil || r["hookSpecificOutput"] == nil {
		t.Fatalf("resume implementation=%v %v", r, e)
	}
	// A normal Stop is quiet until the caller explicitly requests completion.
	r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "Stop", "session_id": "sess", "cwd": root}), root)
	if e != nil || len(r) != 0 {
		t.Fatalf("ordinary stop=%v %v", r, e)
	}
	if e = s.RequestCompletion(task.ID); e != nil {
		t.Fatal(e)
	}
	r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "Stop", "session_id": "sess", "cwd": root}), root)
	if e != nil || r["decision"] != "block" {
		t.Fatalf("stop=%v %v", r, e)
	}
	r, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "Stop", "session_id": "sess", "cwd": root}), root)
	if e != nil || len(r) != 0 {
		t.Fatalf("stop repeat=%v %v", r, e)
	}
}

func TestNativeContextSessionAndOpaqueBash(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	task, e := s.Begin(BeginInput{Project: root, SessionID: "s", Goal: "x", Mode: "implementing", Criteria: []Criterion{{ID: "verify", Description: "verify", Kind: "check"}}})
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range []string{"UserPromptSubmit", "SubagentStart"} {
		r, err := s.Evaluate(mustPayload(map[string]any{"hook_event_name": event, "session_id": "s", "cwd": root}), root)
		if err != nil {
			t.Fatal(err)
		}
		hook, ok := r["hookSpecificOutput"].(map[string]any)
		if !ok || hook["additionalContext"].(string) == "" {
			t.Fatalf("%s context=%v", event, r)
		}
	}
	if r, e := s.Evaluate(mustPayload(map[string]any{"hook_event_name": "SessionStart", "source": "compact", "session_id": "other", "cwd": root}), root); e != nil || len(r) != 0 {
		t.Fatalf("other session=%v %v", r, e)
	}
	if r, e := s.Evaluate(mustPayload(map[string]any{"hook_event_name": "SessionStart", "source": "compact", "session_id": "s", "cwd": filepath.Dir(root)}), root); e != nil || len(r) != 0 {
		t.Fatalf("sibling cwd=%v %v", r, e)
	}
	if e = s.RecordCheck(task.ID, Check{CriterionID: "verify", Status: "pass", Source: "sparestep-exec", Evidence: "verified"}); e != nil {
		t.Fatal(e)
	}
	_, e = s.Evaluate(mustPayload(map[string]any{"hook_event_name": "PreToolUse", "session_id": "s", "cwd": root, "tool_name": "bash", "tool_input": map[string]any{"command": "sh -c 'echo unsafe'"}}), root)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(task.ID)
	if e != nil || got.Checks[0].Status != "invalidated" {
		t.Fatalf("opaque bash did not stale check: %+v err=%v", got, e)
	}
	if e = s.SetProjectPaused(root, true); e != nil {
		t.Fatal(e)
	}
	if r, e := s.Evaluate(mustPayload(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s", "cwd": root}), root); e != nil || len(r) != 0 {
		t.Fatalf("paused=%v %v", r, e)
	}
	if !errors.Is(s.RecordCheck(task.ID, Check{CriterionID: "verify", Status: "unknown"}), ErrInactive) {
		t.Fatal("paused check was not inactive")
	}
}

func TestSymlinkBoundary(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(root, "internal")); e != nil {
		t.Skip(e)
	}
	task, e := s.Begin(BeginInput{Project: root, SessionID: "s", Goal: "x", Mode: "implementing", Paths: []string{"internal"}})
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Evaluate(mustPayload(map[string]any{"hook_event_name": "PreToolUse", "session_id": "s", "cwd": root, "tool_name": "apply_patch", "tool_input": map[string]any{"command": "*** Update File: internal/x.go\n"}}), root)
	if e != nil {
		t.Fatal(e)
	}
	hook, ok := r["hookSpecificOutput"].(map[string]any)
	if !ok || hook["permissionDecision"] != "deny" {
		t.Fatalf("symlink decision=%v task=%s", r, task.ID)
	}
}

func mustPayload(v any) []byte { b, _ := json.Marshal(v); return b }
