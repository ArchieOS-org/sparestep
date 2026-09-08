package focus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Evaluate returns Codex's native wire protocol. Allowed operations are quiet.
func (s *Store) Evaluate(raw []byte, project string) (map[string]any, error) {
	var p struct {
		Event      string         `json:"hook_event_name"`
		Session    string         `json:"session_id"`
		AgentID    string         `json:"agent_id"`
		TurnID     string         `json:"turn_id"`
		Transcript string         `json:"transcript_path"`
		CWD        string         `json:"cwd"`
		Permission string         `json:"permission_mode"`
		Source     string         `json:"source"`
		Tool       string         `json:"tool_name"`
		Input      map[string]any `json:"tool_input"`
		StopActive bool           `json:"stop_hook_active"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("invalid native hook payload")
	}
	quiet := map[string]any{}
	if p.Session == "" || p.CWD == "" || !inside(project, p.CWD) {
		return quiet, nil
	}
	task, err := s.Current(project, p.Session)
	if errors.Is(err, ErrNotFound) {
		return quiet, nil
	}
	if err != nil {
		return nil, err
	}
	// Native permission_mode describes approval policy, not Plan mode.
	// Read typed, turn-matched rollout metadata once per turn, with PreToolUse
	// as a fallback if the prompt hook ran before the rollout was flushed.
	if p.AgentID == "" && (p.Event == "UserPromptSubmit" || p.Event == "PreToolUse") {
		mode := ""
		if p.TurnID != "" {
			if p.Event == "PreToolUse" {
				_ = s.db.QueryRow(`SELECT mode FROM focus_mode_cache WHERE task_id=? AND turn_id=?`, task.ID, p.TurnID).Scan(&mode)
			}
			if mode == "" {
				if observed, ok := readNativeMode(p.Transcript, p.TurnID); ok {
					mode = "implementing"
					if observed == "plan" {
						mode = "planning"
					}
					if _, err = s.db.Exec(`INSERT INTO focus_mode_cache(task_id,turn_id,mode) VALUES(?,?,?) ON CONFLICT(task_id) DO UPDATE SET turn_id=excluded.turn_id,mode=excluded.mode`, task.ID, p.TurnID, mode); err != nil {
						return nil, err
					}
				} else if err = s.recordGap(task.ID, "Native planning mode metadata was unavailable for a turn. Mode handling also relies on the explicit skill instructions."); err != nil {
					return nil, err
				}
			}
		}
		// Some compatible clients explicitly expose plan; default/bypass never
		// prove implementation authorization.
		if p.Permission == "plan" {
			mode = "planning"
		}
		if mode != "" && (task.Mode != mode || task.PermissionMode != p.Permission) {
			_, err = s.db.Exec(`UPDATE focus_tasks SET mode=?,permission_mode=?,revision=revision+1 WHERE id=?`, mode, p.Permission, task.ID)
			if err != nil {
				return nil, err
			}
			task.Mode, task.PermissionMode = mode, p.Permission
		}
	}
	if p.Event == "UserPromptSubmit" && p.AgentID == "" && (task.CompletionRequested || task.StopGate) {
		if _, err = s.db.Exec(`UPDATE focus_tasks SET completion_requested=0,stop_gate=0,revision=revision+1 WHERE id=?`, task.ID); err != nil {
			return nil, err
		}
		task.CompletionRequested, task.StopGate = false, false
	}
	if task.Status == "paused" {
		return quiet, nil
	}
	switch p.Event {
	case "PostToolUse":
		// Persisted notice identity prevents repeated announcements across
		// native hook processes. The GUI retains the complete amendment.
		if len(task.Amendments) > 0 {
			res, err := s.db.Exec(`INSERT OR IGNORE INTO focus_notices(task_id,amendment_count) VALUES(?,?)`, task.ID, len(task.Amendments))
			if err != nil {
				return nil, err
			}
			count, _ := res.RowsAffected()
			if count > 0 {
				return map[string]any{"systemMessage": "Scope expanded — " + boundedText(task.Amendments[len(task.Amendments)-1].Reason, 600)}, nil
			}
		}
		return quiet, nil
	case "Interrupt", "SubagentStop", "PostCompact":
		return quiet, nil
	case "UserPromptSubmit", "SubagentStart":
		return contextOutput(p.Event, brief(*task)), nil
	case "SessionStart":
		if p.Source == "compact" || p.Source == "resume" {
			return contextOutput(p.Event, brief(*task)), nil
		}
		return quiet, nil
	case "Stop":
		// An answer/question is not necessarily a completion claim. The finish
		// command signals intent; never infer completion from assistant prose.
		if p.AgentID != "" || task.Mode == "planning" || !task.CompletionRequested || task.StopGate || p.StopActive {
			return quiet, nil
		}
		missing := []string{}
		for _, c := range task.Criteria {
			if !criterionSatisfied(c) {
				missing = append(missing, c.Description)
			}
		}
		if len(missing) == 0 {
			return quiet, nil
		}
		res, err := s.db.Exec(`UPDATE focus_tasks SET stop_gate=1,revision=revision+1 WHERE id=? AND stop_gate=0`, task.ID)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return quiet, nil
		}
		return map[string]any{"decision": "block", "reason": "The requested task is still incomplete: " + boundedText(strings.Join(missing, "; "), 700) + ". Resolve only these obligations, or record a stopped result and explain the blocker. Do not add side work."}, nil
	case "PreToolUse":
		if !task.HookObserved {
			if _, err = s.db.Exec(`UPDATE focus_tasks SET hook_observed=1,revision=revision+1 WHERE id=? AND hook_observed=0`, task.ID); err != nil {
				return nil, err
			}
		}
	default:
		return quiet, nil
	}
	operation := inspectOperation(p.Tool, p.Input)
	if (task.Mode == "planning" || p.Permission == "plan") && operation.write && !operation.unknown {
		return deny("Plan mode is active. Keep the proposal in the conversation; implementation and issue filing wait until implementation mode."), nil
	}
	if operation.unknown {
		if err = s.recordGap(task.ID, "Some commands or tools have opaque effects. Scope protection covers supported edits, not arbitrary shell or remote behavior."); err != nil {
			return nil, err
		}
		if operation.write {
			if err = s.InvalidateChecks(task.ID); err != nil {
				return nil, err
			}
		}
		return quiet, nil
	}
	if !operation.write {
		return quiet, nil
	}
	for _, path := range operation.paths {
		if !filepath.IsAbs(path) {
			base := p.CWD
			for _, key := range []string{"workdir", "cwd"} {
				if value, ok := p.Input[key].(string); ok && value != "" {
					if filepath.IsAbs(value) {
						base = value
					} else {
						base = filepath.Join(p.CWD, value)
					}
					break
				}
			}
			path = filepath.Join(base, path)
		}
		if ok, _ := taskAllowsPath(*task, project, path); !ok {
			return deny("Outside this task's scope: " + boundedText(path, 240) + ". If required for an existing completion condition, record a necessary expansion with evidence and announce it. Otherwise defer it to Linear and continue the original task."), nil
		}
	}
	if len(operation.paths) > 0 {
		if err = s.InvalidateChecks(task.ID); err != nil {
			return nil, err
		}
	}
	return quiet, nil
}

func deny(reason string) map[string]any {
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}}
}
func contextOutput(event, text string) map[string]any {
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}}
}
func boundedText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func brief(t Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sparestep task %s (%s): %s.\n", t.ID, t.Mode, boundedText(t.Goal, 500))
	for _, c := range t.Criteria {
		fmt.Fprintf(&b, "- %s [%s]: %s\n", c.ID, c.Status, boundedText(c.Description, 180))
	}
	fmt.Fprintf(&b, "Components: %s. Exclusions: %s.\n", strings.Join(t.Paths, ", "), strings.Join(t.ExcludedPaths, ", "))
	b.WriteString("Finish this outcome. Record and visibly explain necessary expansion; defer worthwhile optional work. Keep required verification. In Plan mode, stay read-only and wait for implementation authorization. Do not regenerate the plan or supervise every tool call.")
	return boundedText(b.String(), 1600)
}

func (s *Store) recordGap(id, gap string) error {
	for attempt := 0; attempt < 4; attempt++ {
		s.mu.Lock()
		t, err := s.getLocked(id)
		if err == nil {
			if containsPath(t.CoverageGaps, gap) {
				s.mu.Unlock()
				return nil
			}
			t.CoverageGaps = append(t.CoverageGaps, gap)
			t.UpdatedAt = time.Now().UTC()
			err = s.saveLocked(t)
		}
		s.mu.Unlock()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

// Latest includes completed tasks so finishing never makes the result vanish.
func (s *Store) Latest(project string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := scanTask(s.db.QueryRow(taskSelect+` WHERE project=? ORDER BY updated_at DESC LIMIT 1`, project))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
func (s *Store) AllActive(project string) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(taskSelect+` WHERE project=? AND status IN ('active','paused') ORDER BY updated_at DESC`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			return nil, e
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func inside(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func taskAllowsPath(t Task, project, path string) (bool, string) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(project, path)
	}
	path = filepath.Clean(path)
	if !inside(project, path) {
		return false, "outside worktree"
	}
	rel, _ := filepath.Rel(project, path)
	rel = filepath.ToSlash(rel)
	if symlinkEscapes(project, rel) {
		return false, "symlink leaves worktree"
	}
	for _, excluded := range t.ExcludedPaths {
		if pathMatches(rel, excluded) {
			return false, "explicitly excluded"
		}
	}
	for _, allowed := range t.Paths {
		if pathMatches(rel, allowed) {
			return true, ""
		}
	}
	return false, "outside task components"
}
func pathMatches(path, pattern string) bool {
	pattern = strings.TrimSuffix(filepath.ToSlash(filepath.Clean(pattern)), "/")
	if pattern == "." {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		pattern = strings.TrimSuffix(pattern, "/**")
		return path == pattern || strings.HasPrefix(path, pattern+"/")
	}
	if strings.ContainsAny(pattern, "*?[") {
		matched, _ := filepath.Match(pattern, path)
		return matched
	}
	return path == pattern || strings.HasPrefix(path, pattern+"/")
}
func symlinkEscapes(root, rel string) bool {
	path := filepath.Join(root, filepath.FromSlash(rel))
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return !inside(root, resolved)
		} else if !os.IsNotExist(err) {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path || !inside(root, parent) {
			return true
		}
		path = parent
	}
}
