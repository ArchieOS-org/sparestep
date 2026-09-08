package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArchieOS-org/sparestep/internal/model"
	"github.com/ArchieOS-org/sparestep/internal/store"
)

func invoke(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errs bytes.Buffer
	err := Run(args, strings.NewReader(""), &out, &errs)
	return out.String(), err
}

func TestMalformedHookDoesNotBlockCodex(t *testing.T) {
	dir := t.TempDir()
	var out, errs bytes.Buffer
	err := Run([]string{"hook", "--state-dir", dir, "--project", t.TempDir()}, strings.NewReader("not-json"), &out, &errs)
	if err != nil || strings.TrimSpace(out.String()) != "{}" || errs.Len() != 0 {
		t.Fatalf("hook must remain quiet: %v %s %s", err, &out, &errs)
	}
	if _, err = os.Stat(filepath.Join(dir, "recording-error.txt")); err != nil {
		t.Fatal("recording error should be available to doctor", err)
	}
}

func TestHeadlessEmptyReportHonest(t *testing.T) {
	out, err := invoke(t, "status", "--state-dir", t.TempDir(), "--project", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Waiting for the first", "Token usage unavailable", "No clear opportunities"} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing %q: %s", s, out)
		}
	}
}

func TestNullInputDoesNotShowInteractiveMenu(t *testing.T) {
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var out, errs bytes.Buffer
	err = Run([]string{"demo"}, in, &out, &errs)
	if err != nil || strings.Contains(out.String(), "Choose:") {
		t.Fatalf("headless run should only print the report: %v %s", err, out.String())
	}
}

func TestDemoIsIsolatedAndLabeled(t *testing.T) {
	dir := t.TempDir()
	out, err := invoke(t, "demo", "--state-dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "EXAMPLE DATA") {
		t.Fatal(out)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("demo modified real state: %v %v", entries, err)
	}
}

func TestFeedbackDraftAndUndoFromTerminal(t *testing.T) {
	dir := t.TempDir()
	project := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "sparestep.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = seedDemo(s, project); err != nil {
		t.Fatal(err)
	}
	r, err := s.Report(project)
	s.Close()
	if err != nil || len(r.Findings) == 0 {
		t.Fatalf("missing seeded finding: %+v %v", r, err)
	}
	id := r.Findings[0].ID
	for _, value := range []string{"necessary", "open", "dismissed", "open"} {
		_, err = invoke(t, "feedback", "--state-dir", dir, "--project", project, id, value)
		if err != nil {
			t.Fatal(err)
		}
		out, e := invoke(t, "status", "--state-dir", dir, "--project", project, "--json")
		if e != nil {
			t.Fatal(e)
		}
		var rr model.Report
		if e = json.Unmarshal([]byte(out), &rr); e != nil {
			t.Fatal(e)
		}
		if len(rr.Findings) == 0 || rr.Findings[0].Disposition != value {
			t.Fatalf("feedback not persisted: %+v", rr.Findings)
		}
	}
	draft, err := invoke(t, "draft", "--state-dir", dir, "--project", project, id)
	if err != nil || !strings.Contains(draft, "# ") {
		t.Fatalf("draft: %s %v", draft, err)
	}
}

func TestPauseSurvivesNewInvocation(t *testing.T) {
	dir := t.TempDir()
	project := t.TempDir()
	if _, err := invoke(t, "pause", "--state-dir", dir, "--project", project); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	event := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","turn_id":"t1","cwd":` + strconvQuote(project) + `}`
	if err := Run([]string{"hook", "--state-dir", dir, "--project", project}, strings.NewReader(event), &out, &errs); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "sparestep.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Report(project)
	if err != nil || !r.Paused || r.EventsCount != 0 {
		t.Fatalf("pause ineffective: %+v %v", r, err)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestOversizedHookFailsOpen(t *testing.T) {
	dir := t.TempDir()
	var out, errs bytes.Buffer
	err := Run([]string{"hook", "--state-dir", dir}, strings.NewReader(strings.Repeat("x", 2*1024*1024+1)), &out, &errs)
	if err != nil || strings.TrimSpace(out.String()) != "{}" {
		t.Fatalf("%v %s", err, out.String())
	}
}
