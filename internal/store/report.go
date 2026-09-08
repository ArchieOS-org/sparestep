package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/model"
)

func findingID(cat, project, key string) string {
	h := sha256.Sum256([]byte(cat + "\x00" + project + "\x00" + key))
	return cat + "-" + hex.EncodeToString(h[:8])
}

// Report retains the complete evidence stream, while task-derived counters
// and status are limited to the latest task. Findings remain historical.
func (s *Store) Report(project string) (model.Report, error) {
	ev, err := s.Events(project, 1000)
	if err != nil {
		return model.Report{}, err
	}
	paused, err := s.Paused(project)
	if err != nil {
		return model.Report{}, err
	}
	r := model.Report{Project: project, Paused: paused, Events: ev, EventsCount: len(ev), Status: "recording", Hostname: hostname(), UsageNote: "This connection does not provide token totals."}
	if len(ev) == 0 {
		r.Status = "no_activity"
		return r, nil
	}
	for _, e := range ev {
		if e.Gap != "" {
			r.RecordingGap = appendUnique(r.RecordingGap, e.Gap)
		}
	}
	scope, taskID := latestTask(ev)
	r.TaskID = taskID
	taskEvents := scopedEvents(ev, scope)
	noTaskBoundary := scope.session == ""
	if len(taskEvents) == 0 && noTaskBoundary {
		taskEvents = ev
	}
	var latestEdit time.Time
	for _, e := range taskEvents {
		if e.Kind == "edit" && after(e.Timestamp, latestEdit) {
			latestEdit = e.Timestamp
		}
		if e.Kind == "tool_completed" && e.Tool == "check" {
			r.Checks++
			ok, known := eventResult(e)
			if !known {
				r.UnknownChecks++
			} else if !ok {
				r.FailedChecks++
			}
		}
	}
	r.TokenUsage = taskUsage(taskEvents, &r.UsageNote)
	if noTaskBoundary {
		// Without a lifecycle marker we cannot know that separate sessions are
		// one task (the demo data intentionally contains several sessions).
		r.ElapsedMS = 0
	} else {
		r.ElapsedMS = elapsed(taskEvents)
	}
	r.Status = taskStatus(taskEvents, latestEdit)
	if noTaskBoundary {
		// A legacy/imported stream can contain useful evidence without a task
		// lifecycle marker. It is observed activity, not an open task.
		r.Status = "observed"
	}
	if paused {
		r.Status = "paused"
	}
	r.Findings = s.findings(project, ev)
	return r, nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

type taskScope struct {
	session, turn string
	start         time.Time
}

func latestTask(ev []model.Event) (taskScope, string) {
	var chosen *model.Event
	for i := range ev {
		e := &ev[i]
		if e.Kind != "task_started" || e.SessionID == "" {
			continue
		}
		if chosen == nil || afterEvent(*e, *chosen) {
			chosen = e
		}
	}
	if chosen == nil {
		return taskScope{}, ""
	}
	s := taskScope{session: chosen.SessionID, turn: chosen.TurnID, start: chosen.Timestamp}
	return s, taskID(s.session, s.turn)
}
func afterEvent(a, b model.Event) bool {
	return a.Timestamp.After(b.Timestamp) || (a.Timestamp.Equal(b.Timestamp) && a.ID > b.ID)
}
func taskID(session, turn string) string {
	if session == "" {
		return ""
	}
	if turn == "" {
		return session
	}
	return session + ":" + turn
}
func scopedEvents(ev []model.Event, s taskScope) []model.Event {
	if s.session == "" {
		return nil
	}
	out := make([]model.Event, 0)
	for _, e := range ev {
		if e.SessionID != s.session || e.Timestamp.Before(s.start) {
			continue
		}
		if s.turn != "" && e.TurnID != s.turn {
			continue
		}
		out = append(out, e)
	}
	return out
}
func after(t, base time.Time) bool { return !t.IsZero() && (base.IsZero() || t.After(base)) }
func elapsed(ev []model.Event) int64 {
	if len(ev) == 0 {
		return 0
	}
	var first, last time.Time
	for _, e := range ev {
		if first.IsZero() || e.Timestamp.Before(first) {
			first = e.Timestamp
		}
		if last.IsZero() || e.Timestamp.After(last) {
			last = e.Timestamp
		}
	}
	if first.IsZero() || last.IsZero() {
		return 0
	}
	return last.Sub(first).Milliseconds()
}
func taskStatus(ev []model.Event, edit time.Time) string {
	if len(ev) == 0 {
		return "recording"
	}
	completed := false
	checks := map[string]struct {
		ok, known bool
	}{}
	tools := map[string]struct {
		ok, known bool
	}{}
	for i := range ev {
		e := &ev[i]
		if e.Kind == "task_completed" {
			completed = true
		}
		if e.Kind != "tool_completed" || !after(e.Timestamp, edit) {
			continue
		}
		ok, known := eventResult(*e)
		if e.Tool == "check" {
			checks[e.StateKey+"\x00"+e.Command] = struct{ ok, known bool }{ok, known}
		} else if e.Tool != "" || e.Command != "" {
			// A later successful invocation of the same command resolves an
			// earlier failure; failures from a different command remain open.
			key := e.StateKey + "\x00" + e.Command
			if e.StateKey == "" && e.Command == "" {
				key = e.Tool
			}
			tools[key] = struct{ ok, known bool }{ok, known}
		}
	}
	for _, result := range tools {
		if result.known && !result.ok {
			return "needs_attention"
		}
	}
	if len(checks) > 0 && completed {
		allPassed := true
		for _, result := range checks {
			if result.known && !result.ok {
				return "needs_attention"
			}
			if !result.known {
				allPassed = false
			}
		}
		if allPassed {
			return "checks_passed"
		}
	}
	if completed {
		return "completed"
	}
	return "recording"
}

type usageSample struct {
	e model.Event
	u model.Usage
}

func taskUsage(ev []model.Event, note *string) *model.Usage {
	bySource := map[string][]usageSample{}
	invalid := false
	for _, e := range ev {
		if e.Usage == nil {
			continue
		}
		if !validUsage(*e.Usage) || strings.TrimSpace(e.Usage.Source) == "" || strings.EqualFold(e.Usage.Source, "unknown") {
			*note = "Token usage was unavailable because a snapshot had an unknown or inconsistent source/counter."
			invalid = true
			continue
		}
		u := *e.Usage
		bySource[u.Source] = append(bySource[u.Source], usageSample{e: e, u: u})
	}
	if invalid || len(bySource) == 0 {
		return nil
	}
	var out model.Usage
	for source, samples := range bySource {
		var one model.Usage
		var latestCum *usageSample
		for i := range samples {
			x := samples[i]
			if x.u.Cumulative && (latestCum == nil || afterEvent(x.e, latestCum.e)) {
				z := x
				latestCum = &z
			}
		}
		if latestCum != nil {
			one = latestCum.u
			for _, x := range samples {
				if !x.u.Cumulative && x.e.Timestamp.After(latestCum.e.Timestamp) {
					addUsage(&one, x.u)
				}
			}
		} else {
			one.Source = source
			for _, x := range samples {
				addUsage(&one, x.u)
			}
		}
		if out.Source == "" {
			out = one
		} else {
			addUsage(&out, one)
		}
	}
	out.Cumulative = false
	if len(bySource) > 1 {
		out.Source = "multiple"
	}
	return &out
}
func validUsage(u model.Usage) bool {
	if u.Input < 0 || u.CachedInput < 0 || u.Output < 0 || u.ReasoningOutput < 0 || u.Total < 0 || u.CachedInput > u.Input || u.ReasoningOutput > u.Output {
		return false
	}
	return u.Total == 0 || u.Total == u.Input+u.Output
}
func addUsage(dst *model.Usage, u model.Usage) {
	dst.Input += u.Input
	dst.CachedInput += u.CachedInput
	dst.Output += u.Output
	dst.ReasoningOutput += u.ReasoningOutput
	dst.Total += u.Total
}

func (s *Store) findings(project string, ev []model.Event) []model.Finding {
	type group struct {
		cat, key, title string
		list            []model.Event
	}
	groups := map[string]*group{}
	failureTasks := map[string]map[string]bool{}
	for _, e := range ev {
		ok, known := eventResult(e)
		if e.Kind != "tool_completed" || e.Tool == "" || e.FailureKey == "" || !known || ok || e.SessionID == "" {
			continue
		}
		key := e.Tool + "\x00" + e.FailureKey
		tk := taskID(e.SessionID, e.TurnID)
		if tk == "" {
			continue
		}
		if groups[key] == nil {
			groups[key] = &group{cat: "recurring_failure", key: key, title: "Repeated setup or tool failure"}
		}
		groups[key].list = append(groups[key].list, e)
		if failureTasks[key] == nil {
			failureTasks[key] = map[string]bool{}
		}
		failureTasks[key][tk] = true
	}
	for key := range groups {
		if len(failureTasks[key]) < 2 {
			delete(groups, key)
		}
	}
	for _, cat := range []string{"check", "read"} {
		for key, list := range repeatGroups(ev, cat) {
			if len(list) >= 2 {
				groups[cat+"\x00"+key] = &group{cat: "repeated_" + cat, key: key, title: map[string]string{"check": "Repeated successful check", "read": "Repeated unchanged read"}[cat], list: list}
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]model.Finding, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		if len(g.list) < 2 {
			continue
		}
		id := findingID(g.cat, project, g.key)
		f := model.Finding{ID: id, Category: g.cat, Title: g.title, Confidence: "medium", Occurrences: len(g.list), Suggestion: "Review whether this repeated work can be avoided.", Disposition: s.disposition(project, id), IssueURL: s.issueURL(project, id)}
		for _, e := range g.list {
			f.EvidenceIDs = append(f.EvidenceIDs, e.ID)
			f.DurationMS += e.DurationMS
		}
		switch g.cat {
		case "recurring_failure":
			f.Explanation = "The same known failure occurred across multiple tasks."
		case "repeated_check":
			f.Explanation = "The same successful check repeated in one task segment with known unchanged context."
		case "repeated_read":
			f.Explanation = "The same read repeated in one task segment with known unchanged context."
		}
		out = append(out, f)
	}
	return out
}

func repeatGroups(ev []model.Event, cat string) map[string][]model.Event {
	type taskState struct{ last map[string]model.Event }
	states := map[string]*taskState{}
	out := map[string][]model.Event{}
	seen := map[string]map[string]bool{}
	for _, e := range ev {
		tk := taskID(e.SessionID, e.TurnID)
		if tk == "" {
			continue
		}
		st := states[tk]
		if st == nil {
			st = &taskState{last: map[string]model.Event{}}
			states[tk] = st
		}
		if e.Kind == "edit" || e.Kind == "context_reset" {
			st.last = map[string]model.Event{}
			continue
		}
		if e.Kind != "tool_completed" || e.Tool != cat || !e.ContextKnown || e.StateKey == "" || e.Command == "" {
			continue
		}
		ok, known := eventResult(e)
		if !known || !ok {
			continue
		}
		key := e.StateKey + "\x00" + e.Command
		if p, exists := st.last[key]; exists {
			if seen[key] == nil {
				seen[key] = map[string]bool{}
			}
			if !seen[key][p.ID] {
				out[key] = append(out[key], p)
				seen[key][p.ID] = true
			}
			if !seen[key][e.ID] {
				out[key] = append(out[key], e)
				seen[key][e.ID] = true
			}
		}
		st.last[key] = e
	}
	return out
}

func eventResult(e model.Event) (bool, bool) {
	if e.Success != nil && e.ExitCode != nil {
		// A disagreement is evidence of failure, never grounds for claiming
		// a successful result.
		return *e.Success && *e.ExitCode == 0, true
	}
	if e.Success != nil {
		return *e.Success, true
	}
	if e.ExitCode != nil {
		return *e.ExitCode == 0, true
	}
	return false, false
}
func appendUnique(existing, value string) string {
	if existing == "" {
		return value
	}
	for _, x := range strings.Split(existing, "; ") {
		if x == value {
			return existing
		}
	}
	return existing + "; " + value
}
func (s *Store) issueURL(project, id string) string {
	var v string
	if s.db.QueryRow(`SELECT issue_url FROM drafts WHERE project=? AND finding_id=?`, project, id).Scan(&v) != nil {
		return ""
	}
	return v
}

func (s *Store) Draft(project, id string) (model.Draft, error) {
	var d model.Draft
	err := s.db.QueryRow(`SELECT finding_id,title,body,linear_url,state,issue_url FROM drafts WHERE project=? AND finding_id=?`, project, id).Scan(&d.FindingID, &d.Title, &d.Body, &d.LinearURL, &d.State, &d.IssueURL)
	if err == nil {
		return d, nil
	}
	if err != sql.ErrNoRows {
		return d, err
	}
	r, err := s.Report(project)
	if err != nil {
		return d, err
	}
	var f *model.Finding
	for i := range r.Findings {
		if r.Findings[i].ID == id {
			f = &r.Findings[i]
			break
		}
	}
	if f == nil {
		return d, fmt.Errorf("finding not found")
	}
	body := draftBody(*f)
	vals := url.Values{}
	vals.Set("title", f.Title)
	vals.Set("description", body)
	d = model.Draft{FindingID: id, Title: f.Title, State: "draft", LinearURL: "https://linear.new?" + vals.Encode(), Body: body}
	if _, err = s.db.Exec(`INSERT OR IGNORE INTO drafts(project,finding_id,title,body,linear_url,state,issue_url) VALUES(?,?,?,?,?,?,?)`, project, id, d.Title, d.Body, d.LinearURL, d.State, ""); err != nil {
		return d, err
	}
	return s.Draft(project, id)
}
func draftBody(f model.Finding) string {
	return fmt.Sprintf("What we observed\n\n%s\n\nOccurrences: %d\nObserved duration: %d ms\nEvidence IDs: %s\n\nHypothesis\n\nThis repeated work may be avoidable.\n\nSuggested action\n\n%s\n\nAcceptance criteria\n\n- Confirm whether the repeated work is expected for this task.\n- If it is avoidable, record a durable instruction or check that prevents the repetition.\n- Verify the instruction against a future task before adopting it.", f.Explanation, f.Occurrences, f.DurationMS, strings.Join(f.EvidenceIDs, ", "), f.Suggestion)
}
