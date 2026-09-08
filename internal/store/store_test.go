package store

import (
	"github.com/ArchieOS-org/sparestep/internal/model"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func ev(id, project, session, turn string, ts time.Time) model.Event {
	ok := true
	return model.Event{ID: id, Project: project, SessionID: session, TurnID: turn, Kind: "tool_completed", Tool: "check", Command: "go test ./...", StateKey: "s", ContextKnown: true, Success: &ok, Timestamp: ts}
}
func TestStoreRestartPauseDedupRetention(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db.sqlite")
	s, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	if e = s.Record(ev("a", "p", "s", "t", now)); e != nil {
		t.Fatal(e)
	}
	if e = s.Record(ev("a", "p", "s", "t", now)); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, _ := s.Events("p", 10)
	if len(got) != 1 || !got[0].Timestamp.Equal(now) {
		t.Fatalf("events=%+v", got)
	}
	if e = s.SetPaused("p", true); e != nil {
		t.Fatal(e)
	}
	if e = s.Record(ev("b", "p", "s", "t", now)); e != nil {
		t.Fatal(e)
	}
	got, _ = s.Events("p", 10)
	if len(got) != 1 {
		t.Fatal("paused capture inserted")
	}
	if e = s.PurgeBefore(now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	got, _ = s.Events("p", 10)
	if len(got) != 0 {
		t.Fatal("retention failed")
	}
}
func TestStoreConcurrentRecord(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var w sync.WaitGroup
	for i := 0; i < 20; i++ {
		w.Add(1)
		go func(i int) {
			defer w.Done()
			if e := s.Record(ev(string(rune('a'+i)), "p", "s", "t", time.Now())); e != nil {
				t.Error(e)
			}
		}(i)
	}
	w.Wait()
	got, e := s.Events("p", 30)
	if e != nil || len(got) != 20 {
		t.Fatalf("%d %v", len(got), e)
	}
}
func TestReportPreservesHistoricalRepeatAfterLaterEdit(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer s.Close()
	n := time.Now()
	a := ev("a", "p", "s", "t", n)
	s.Record(a)
	b := ev("b", "p", "s", "t", n.Add(time.Second))
	s.Record(b)
	r, e := s.Report("p")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Findings) == 0 {
		t.Fatal("expected repeat")
	}
	edit := model.Event{ID: "x", Project: "p", SessionID: "s", TurnID: "t", Kind: "edit", Timestamp: n.Add(2 * time.Second)}
	s.Record(edit)
	r, _ = s.Report("p")
	if len(r.Findings) == 0 {
		t.Fatal("later edit should not erase historical repeat")
	}
}

func TestReportEditBetweenChecksPreventsRepetition(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer s.Close()
	n := time.Now()
	s.Record(ev("a", "p", "s", "t", n))
	s.Record(model.Event{ID: "x", Project: "p", SessionID: "s", TurnID: "t", Kind: "edit", Timestamp: n.Add(time.Second)})
	s.Record(ev("b", "p", "s", "t", n.Add(2*time.Second)))
	r, err := s.Report("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 0 {
		t.Fatal("edit between checks should prevent repetition finding")
	}
}
