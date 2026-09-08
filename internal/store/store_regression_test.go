package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/model"
)

func boolp(v bool) *bool { return &v }
func intp(v int) *int    { return &v }
func taskEvent(id, session, turn, kind string, ts time.Time) model.Event {
	return model.Event{ID: id, Project: "p", SessionID: session, TurnID: turn, Kind: kind, Timestamp: ts}
}

func TestReportScopesCurrentTaskAndUsesHost(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := time.Now().UTC()
	for _, e := range []model.Event{
		taskEvent("start-1", "s1", "t1", "task_started", n),
		{ID: "check-1", Project: "p", SessionID: "s1", TurnID: "t1", Kind: "tool_completed", Tool: "check", Success: boolp(true), Timestamp: n.Add(time.Second)},
		taskEvent("done-1", "s1", "t1", "task_completed", n.Add(2*time.Second)),
		taskEvent("start-2", "s2", "t2", "task_started", n.Add(3*time.Second)),
		{ID: "check-2", Project: "p", SessionID: "s2", TurnID: "t2", Kind: "tool_completed", Tool: "check", ExitCode: intp(1), Timestamp: n.Add(4 * time.Second)},
	} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Report("p")
	if err != nil {
		t.Fatal(err)
	}
	if r.TaskID != "s2:t2" || r.Checks != 1 || r.FailedChecks != 1 || r.ElapsedMS != 1000 || r.Hostname == "" {
		t.Fatalf("scope=%+v", r)
	}
}

func TestStatusFailedCheckEditThenPassAndUnknownNeverPasses(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := time.Now().UTC()
	for _, e := range []model.Event{
		taskEvent("start", "s", "t", "task_started", n),
		{ID: "failed", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Tool: "check", Success: boolp(false), Timestamp: n.Add(time.Second)},
		{ID: "edit", Project: "p", SessionID: "s", TurnID: "t", Kind: "edit", Timestamp: n.Add(2 * time.Second)},
		{ID: "passed", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Tool: "check", Success: boolp(true), Timestamp: n.Add(3 * time.Second)},
		taskEvent("done", "s", "t", "task_completed", n.Add(4*time.Second)),
	} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := s.Report("p")
	if r.Status != "checks_passed" || r.Checks != 2 || r.FailedChecks != 1 {
		t.Fatalf("%+v", r)
	}
	unknown := model.Event{ID: "unknown", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Tool: "check", Timestamp: n.Add(5 * time.Second)}
	if err := s.Record(unknown); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Report("p")
	if r.Status == "checks_passed" || r.UnknownChecks != 1 {
		t.Fatalf("unknown passed: %+v", r)
	}
}

func TestStatusKeepsDistinctFailedCheckAdvisoryToPass(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := time.Now().UTC()
	for _, e := range []model.Event{
		taskEvent("start", "s", "t", "task_started", n),
		{ID: "fail", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Tool: "check", Command: "go test ./a", Success: boolp(false), Timestamp: n.Add(time.Second)},
		{ID: "pass", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Tool: "check", Command: "go test ./b", Success: boolp(true), Timestamp: n.Add(2 * time.Second)},
		taskEvent("done", "s", "t", "task_completed", n.Add(3*time.Second)),
	} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Report("p")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "needs_attention" {
		t.Fatalf("distinct failed check was not surfaced: %+v", r)
	}
}

func TestConflictingResultSignalsNeverPass(t *testing.T) {
	if ok, known := eventResult(model.Event{Success: boolp(true), ExitCode: intp(1)}); !known || ok {
		t.Fatalf("conflicting success signals passed: ok=%v known=%v", ok, known)
	}
	if ok, known := eventResult(model.Event{Success: boolp(false), ExitCode: intp(0)}); !known || ok {
		t.Fatalf("conflicting failure signals passed: ok=%v known=%v", ok, known)
	}
}

func TestMarkerlessSessionsHaveUnknownElapsed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := time.Now().UTC()
	for i, session := range []string{"s1", "s2", "s3"} {
		if err := s.Record(model.Event{ID: string(rune('a' + i)), Project: "p", SessionID: session, TurnID: "t", Kind: "tool_completed", Tool: "shell", Timestamp: n.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Report("p")
	if err != nil {
		t.Fatal(err)
	}
	if r.ElapsedMS != 0 || r.Status != "observed" {
		t.Fatalf("markerless activity was treated as one task: %+v", r)
	}
}

func TestUsageCumulativeAndInvalidSource(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := time.Now().UTC()
	start := taskEvent("start", "s", "t", "task_started", n)
	for _, e := range []model.Event{
		start,
		{ID: "u1", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Timestamp: n.Add(time.Second), Usage: &model.Usage{Input: 10, CachedInput: 2, Output: 5, ReasoningOutput: 1, Total: 15, Cumulative: true, Source: "codex"}},
		{ID: "u2", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Timestamp: n.Add(2 * time.Second), Usage: &model.Usage{Input: 20, CachedInput: 4, Output: 7, ReasoningOutput: 2, Total: 27, Cumulative: true, Source: "codex"}},
	} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := s.Report("p")
	if r.TokenUsage == nil || r.TokenUsage.Total != 27 {
		t.Fatalf("usage=%+v", r.TokenUsage)
	}
	bad := model.Event{ID: "bad", Project: "p", SessionID: "s", TurnID: "t", Kind: "tool_completed", Timestamp: n.Add(3 * time.Second), Usage: &model.Usage{Input: -1, Source: "codex"}}
	if err := s.Record(bad); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Report("p")
	if r.TokenUsage != nil || !strings.Contains(r.UsageNote, "unavailable") {
		t.Fatalf("invalid usage=%+v note=%q", r.TokenUsage, r.UsageNote)
	}
}

func TestRecurringFailureAndRepeatedHistorySurviveEdits(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := time.Now().UTC()
	code := 1
	for _, e := range []model.Event{
		{ID: "f1", Project: "p", SessionID: "s1", TurnID: "t1", Kind: "tool_completed", Tool: "shell", FailureKey: "missing", ExitCode: &code, DurationMS: 10, Timestamp: n},
		{ID: "edit", Project: "p", SessionID: "s1", TurnID: "t1", Kind: "edit", Timestamp: n.Add(time.Second)},
		{ID: "f2", Project: "p", SessionID: "s2", TurnID: "t2", Kind: "tool_completed", Tool: "shell", FailureKey: "missing", ExitCode: &code, DurationMS: 20, Timestamp: n.Add(2 * time.Second)},
		{ID: "r1", Project: "p", SessionID: "s3", TurnID: "t3", Kind: "tool_completed", Tool: "check", Success: boolp(true), ContextKnown: true, StateKey: "same", Command: "go test", DurationMS: 3, Timestamp: n.Add(3 * time.Second)},
		{ID: "r2", Project: "p", SessionID: "s3", TurnID: "t3", Kind: "tool_completed", Tool: "check", Success: boolp(true), ContextKnown: true, StateKey: "same", Command: "go test", DurationMS: 4, Timestamp: n.Add(4 * time.Second)},
		{ID: "r-edit", Project: "p", SessionID: "s3", TurnID: "t3", Kind: "edit", Timestamp: n.Add(5 * time.Second)},
	} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Report("p")
	if err != nil {
		t.Fatal(err)
	}
	var failure, repeat *model.Finding
	for i := range r.Findings {
		if r.Findings[i].Category == "recurring_failure" {
			failure = &r.Findings[i]
		}
		if r.Findings[i].Category == "repeated_check" {
			repeat = &r.Findings[i]
		}
	}
	if failure == nil || failure.Occurrences != 2 || failure.DurationMS != 30 {
		t.Fatalf("failure=%+v findings=%+v", failure, r.Findings)
	}
	if repeat == nil || repeat.Occurrences != 2 {
		t.Fatalf("historical repeat=%+v findings=%+v", repeat, r.Findings)
	}
}

func TestDraftAndLinkCreateMissingDraft(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n := time.Now().UTC()
	ok := true
	for _, e := range []model.Event{
		{ID: "a", Project: "p", SessionID: "s1", TurnID: "t1", Kind: "tool_completed", Tool: "shell", FailureKey: "bad", Success: &ok, Timestamp: n},
		{ID: "b", Project: "p", SessionID: "s2", TurnID: "t2", Kind: "tool_completed", Tool: "shell", FailureKey: "bad", Success: &ok, Timestamp: n.Add(time.Second)},
	} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	// Make both observations failures after constructing them; this also keeps
	// the test independent of the ExitCode-vs-Success precedence rule.
	falsev := false
	_, err = s.db.Exec(`UPDATE events SET success=? WHERE id IN ('a','b')`, boolInt(&falsev))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.Report("p")
	if len(r.Findings) != 1 {
		t.Fatalf("findings=%+v", r.Findings)
	}
	id := r.Findings[0].ID
	if err := s.SetIssueURL("p", id, "https://linear.app/test/issue/TEST-1/example"); err != nil {
		t.Fatal(err)
	}
	d, err := s.Draft("p", id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.LinearURL, "https://linear.new?") || d.State != "linked" || d.IssueURL == "" || !strings.Contains(d.Body, "Acceptance criteria") {
		t.Fatalf("draft=%+v", d)
	}
	if err := s.SetIssueURL("p", id, "https://linear.app.evil/test/issue/X"); err == nil {
		t.Fatal("accepted invalid host")
	}
	if err := s.SetDisposition("p", "missing", "dismissed"); err == nil {
		t.Fatal("accepted missing finding")
	}
}
