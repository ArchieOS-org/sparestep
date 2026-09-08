// Package focus stores the bounded task state used by Sparestep's native hooks.
// It deliberately contains no command execution, network, model, or external
// issue-tracker integration. Callers can use Evaluate to make a local policy
// decision before handing an operation to the native host.
package focus

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound      = errors.New("focus task not found")
	ErrInactive      = errors.New("focus task is inactive")
	ErrInvalid       = errors.New("invalid focus input")
	ErrUnmetCriteria = errors.New("required criteria are unmet")
	ErrCrossSession  = errors.New("focus task belongs to another session")
	ErrBoundary      = errors.New("operation is outside the focus boundary")
	ErrConflict      = errors.New("focus task changed concurrently")
)

// Criterion describes one bounded completion requirement.
type Criterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Kind        string `json:"kind"` // check or review
	Status      string `json:"status"`
	Evidence    string `json:"evidence,omitempty"`
}

// Amendment records an explicit, evidenced expansion or change in scope.
type Amendment struct {
	CriterionID string    `json:"criterion_id,omitempty"`
	Reason      string    `json:"reason"`
	Evidence    string    `json:"evidence"`
	Paths       []string  `json:"paths,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Check is a bounded observation. Invalidated checks cannot satisfy a
// criterion after a subsequent edit or amendment.
type Check struct {
	ID          string    `json:"id,omitempty"`
	CriterionID string    `json:"criterion_id"`
	Name        string    `json:"name,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Status      string    `json:"status"`
	Evidence    string    `json:"evidence,omitempty"`
	Paths       []string  `json:"paths,omitempty"`
	CoverageGap string    `json:"coverage_gap,omitempty"`
	Source      string    `json:"source,omitempty"`
	Command     []string  `json:"command,omitempty"`
	ExitCode    int       `json:"exit_code,omitempty"`
	OperationID string    `json:"operation_id,omitempty"`
	Timestamp   time.Time `json:"timestamp,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Task is the persisted focus object. Paths are component-relative paths or
// globs, interpreted from Project when Project is an absolute worktree path.
type Task struct {
	ID                  string      `json:"id"`
	Project             string      `json:"project"`
	SessionID           string      `json:"session_id"`
	ThreadID            string      `json:"thread_id,omitempty"`
	Goal                string      `json:"goal"`
	Status              string      `json:"status"`
	Mode                string      `json:"mode"`
	Criteria            []Criterion `json:"criteria"`
	Paths               []string    `json:"paths"`
	ExcludedPaths       []string    `json:"excluded_paths"`
	Amendments          []Amendment `json:"amendments"`
	Checks              []Check     `json:"checks"`
	CoverageGaps        []string    `json:"coverage_gaps"`
	OwnerRootThread     string      `json:"owner_root_thread,omitempty"`
	StopGate            bool        `json:"stop_gate,omitempty"`
	PermissionMode      string      `json:"permission_mode,omitempty"`
	Revision            int64       `json:"revision,omitempty"`
	HookObserved        bool        `json:"hook_observed,omitempty"`
	CompletionRequested bool        `json:"completion_requested,omitempty"`
	CreatedAt           time.Time   `json:"created_at"`
	UpdatedAt           time.Time   `json:"updated_at"`
}

// BeginInput starts one task session.
type BeginInput struct {
	Project         string      `json:"project"`
	SessionID       string      `json:"session_id"`
	ThreadID        string      `json:"thread_id,omitempty"`
	Goal            string      `json:"goal"`
	Mode            string      `json:"mode"`
	Criteria        []Criterion `json:"criteria,omitempty"`
	Paths           []string    `json:"paths,omitempty"`
	ExcludedPaths   []string    `json:"excluded_paths,omitempty"`
	OwnerRootThread string      `json:"owner_root_thread,omitempty"`
	PermissionMode  string      `json:"permission_mode,omitempty"`
}

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Keep reads explicit so databases created before a migration appended a
// column still scan correctly.
const taskSelect = `SELECT id,project,session_id,thread_id,goal,status,mode,criteria_json,paths_json,excluded_paths_json,amendments_json,checks_json,gaps_json,owner_root_thread,stop_gate,permission_mode,revision,hook_observed,completion_requested,created_at,updated_at FROM focus_tasks`

// Open opens (and migrates) a durable focus database.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	dsn := path
	if path != ":memory:" {
		dsn = (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String() + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0600)
	}
	return s, nil
}

func (s *Store) init() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS focus_tasks (
 id TEXT PRIMARY KEY, project TEXT NOT NULL, session_id TEXT NOT NULL,
 thread_id TEXT, goal TEXT NOT NULL, status TEXT NOT NULL, mode TEXT NOT NULL,
 criteria_json TEXT NOT NULL, paths_json TEXT NOT NULL, excluded_paths_json TEXT NOT NULL,
 amendments_json TEXT NOT NULL, checks_json TEXT NOT NULL, gaps_json TEXT NOT NULL,
 owner_root_thread TEXT, stop_gate INTEGER NOT NULL DEFAULT 0,
	permission_mode TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 0, hook_observed INTEGER NOT NULL DEFAULT 0, completion_requested INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
); CREATE INDEX IF NOT EXISTS focus_current ON focus_tasks(project,session_id,status);
CREATE TABLE IF NOT EXISTS focus_notices (task_id TEXT NOT NULL, amendment_count INTEGER NOT NULL, PRIMARY KEY(task_id,amendment_count));
CREATE TABLE IF NOT EXISTS focus_mode_cache (task_id TEXT PRIMARY KEY, turn_id TEXT NOT NULL, mode TEXT NOT NULL);`)
	if err != nil {
		return err
	}
	// Databases created by an early development build may lack these columns.
	// SQLite reports a duplicate-column error, which is safe to ignore here.
	_, _ = s.db.Exec(`ALTER TABLE focus_tasks ADD COLUMN permission_mode TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE focus_tasks ADD COLUMN revision INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE focus_tasks ADD COLUMN hook_observed INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE focus_tasks ADD COLUMN completion_requested INTEGER NOT NULL DEFAULT 0`)
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Begin(in BeginInput) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.Project = strings.TrimSpace(in.Project)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.Goal = strings.TrimSpace(in.Goal)
	if in.Project == "" || in.SessionID == "" || in.Goal == "" {
		return Task{}, fmt.Errorf("%w: project, session_id, and goal are required", ErrInvalid)
	}
	if in.Mode == "" {
		in.Mode = "implementing"
	}
	if in.Mode != "planning" && in.Mode != "implementing" {
		return Task{}, fmt.Errorf("%w: mode must be planning or implementing", ErrInvalid)
	}
	criteria := cloneCriteria(in.Criteria)
	seen := map[string]bool{}
	for i := range criteria {
		criteria[i].ID = strings.TrimSpace(criteria[i].ID)
		if criteria[i].ID == "" {
			return Task{}, fmt.Errorf("%w: criterion id is required", ErrInvalid)
		}
		if seen[criteria[i].ID] {
			return Task{}, fmt.Errorf("%w: duplicate criterion id %q", ErrInvalid, criteria[i].ID)
		}
		seen[criteria[i].ID] = true
		if criteria[i].Kind == "" {
			criteria[i].Kind = "check"
		}
		if criteria[i].Kind != "check" && criteria[i].Kind != "review" {
			return Task{}, fmt.Errorf("%w: criterion kind %q", ErrInvalid, criteria[i].Kind)
		}
		// A caller cannot smuggle a passed model assertion into the durable
		// focus. Only RecordCheck from a trusted wrapper can satisfy it.
		criteria[i].Status = "pending"
	}
	now := time.Now().UTC()
	// Starting a second focus in one session supersedes the previous one. This
	// keeps Current deterministic and makes retries idempotent for the same
	// explicit task request.
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, err
	}
	var existing Task
	if prior, e := scanTask(tx.QueryRow(taskSelect+` WHERE project=? AND session_id=? AND status IN ('active','paused') ORDER BY updated_at DESC LIMIT 1`, in.Project, in.SessionID)); e == nil {
		existing = prior
		if existing.Goal == in.Goal && existing.Mode == in.Mode {
			_ = tx.Rollback()
			return existing, nil
		}
		existing.Status = "superseded"
		existing.UpdatedAt = now
		if err = updateTaskExec(tx, existing); err != nil {
			_ = tx.Rollback()
			return Task{}, err
		}
	}
	t := Task{ID: newID(), Project: in.Project, SessionID: in.SessionID, ThreadID: in.ThreadID, Goal: in.Goal, Status: "active", Mode: in.Mode, Criteria: criteria, Paths: normalizePaths(in.Paths), ExcludedPaths: normalizePaths(in.ExcludedPaths), Amendments: []Amendment{}, Checks: []Check{}, CoverageGaps: []string{}, OwnerRootThread: in.OwnerRootThread, PermissionMode: in.PermissionMode, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.Exec(`INSERT INTO focus_tasks(id,project,session_id,thread_id,goal,status,mode,criteria_json,paths_json,excluded_paths_json,amendments_json,checks_json,gaps_json,owner_root_thread,stop_gate,permission_mode,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.Project, t.SessionID, t.ThreadID, t.Goal, t.Status, t.Mode, mustJSON(t.Criteria), mustJSON(t.Paths), mustJSON(t.ExcludedPaths), mustJSON(t.Amendments), mustJSON(t.Checks), mustJSON(t.CoverageGaps), t.OwnerRootThread, 0, t.PermissionMode, t.Revision, t.CreatedAt.UnixNano(), t.UpdatedAt.UnixNano()); err != nil {
		_ = tx.Rollback()
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) Current(project, session string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(taskSelect+` WHERE project=? AND session_id=? AND status IN ('active','paused') ORDER BY updated_at DESC LIMIT 1`, project, session)
	t, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) Get(id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(id)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) getLocked(id string) (Task, error) {
	return scanTask(s.db.QueryRow(taskSelect+` WHERE id=?`, id))
}

func (s *Store) Amend(taskID string, a Amendment) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(taskID)
	if err == sql.ErrNoRows {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if t.Status != "active" {
		return Task{}, ErrInactive
	}
	a.Reason = strings.TrimSpace(a.Reason)
	a.Evidence = strings.TrimSpace(a.Evidence)
	if a.Reason == "" || a.Evidence == "" {
		return Task{}, fmt.Errorf("%w: amendment reason and evidence are required", ErrInvalid)
	}
	a.Paths = normalizePaths(a.Paths)
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	t.Amendments = append(t.Amendments, a)
	for i := range t.Checks {
		t.Checks[i].Status = "invalidated"
	}
	for i := range t.Criteria {
		t.Criteria[i].Status = "pending"
		t.Criteria[i].Evidence = ""
	}
	for _, p := range a.Paths {
		if !containsPath(t.Paths, p) {
			t.Paths = append(t.Paths, p)
		}
	}
	t.UpdatedAt = time.Now().UTC()
	if err = s.saveLocked(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) RecordCheck(taskID string, c Check) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(taskID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if t.Status != "active" {
		return ErrInactive
	}
	c.CriterionID = strings.TrimSpace(c.CriterionID)
	if c.CriterionID == "" {
		return fmt.Errorf("%w: criterion_id is required", ErrInvalid)
	}
	idx := -1
	for i := range t.Criteria {
		if t.Criteria[i].ID == c.CriterionID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: unknown criterion %q", ErrInvalid, c.CriterionID)
	}
	if !validCheckStatus(c.Status) {
		return fmt.Errorf("%w: invalid check status %q", ErrInvalid, c.Status)
	}
	if canonicalCheckStatus(c.Status) == "pass" && t.Criteria[idx].Kind == "check" && !trustedCheckSource(c.Source) {
		return fmt.Errorf("%w: passed checks require a trusted wrapper source", ErrInvalid)
	}
	if canonicalCheckStatus(c.Status) == "pass" && t.Criteria[idx].Kind == "review" && !trustedReviewSource(c.Source) {
		return fmt.Errorf("%w: passed reviews require an agent-review source", ErrInvalid)
	}
	if c.ID == "" {
		c.ID = newID()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.Paths = normalizePaths(c.Paths)
	t.Checks = append(t.Checks, c)
	t.Criteria[idx].Status = canonicalCheckStatus(c.Status)
	t.Criteria[idx].Evidence = c.Evidence
	if c.CoverageGap != "" {
		t.CoverageGaps = appendUnique(t.CoverageGaps, c.CoverageGap)
	}
	t.UpdatedAt = time.Now().UTC()
	return s.saveLocked(t)
}

// InvalidateChecks marks prior evidence stale after an accepted edit. The
// operation is idempotent and performs no write when there is no evidence.
func (s *Store) InvalidateChecks(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(taskID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	changed := false
	for i := range t.Checks {
		if t.Checks[i].Status != "invalidated" {
			t.Checks[i].Status = "invalidated"
			changed = true
		}
	}
	if !changed {
		return nil
	}
	for i := range t.Criteria {
		t.Criteria[i].Status = "pending"
		t.Criteria[i].Evidence = ""
	}
	t.UpdatedAt = time.Now().UTC()
	return s.saveLocked(t)
}

func (s *Store) Pause(taskID string) (Task, error)  { return s.transition(taskID, "paused", "active") }
func (s *Store) Resume(taskID string) (Task, error) { return s.transition(taskID, "active", "paused") }
func (s *Store) transition(id, to, from string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(id)
	if err == sql.ErrNoRows {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if t.Status != from {
		return Task{}, ErrInactive
	}
	t.Status = to
	t.UpdatedAt = time.Now().UTC()
	return t, s.saveLocked(t)
}

// SetProjectPaused pauses or resumes every active focus in a project. This is
// the small project-level control used by the local UI; it does not infer a
// session or thread.
func (s *Store) SetProjectPaused(project string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(taskSelect+` WHERE project=? AND status=?`, project, map[bool]string{true: "active", false: "paused"}[paused])
	if err != nil {
		return err
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			return e
		}
		tasks = append(tasks, t)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, t := range tasks {
		if paused {
			t.Status = "paused"
		} else {
			t.Status = "active"
		}
		t.UpdatedAt = time.Now().UTC()
		if err = s.saveLocked(t); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Finish(taskID, status string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(taskID)
	if err == sql.ErrNoRows {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	if t.Status != "active" {
		return Task{}, ErrInactive
	}
	status = canonicalFinishStatus(status)
	if status == "" {
		return Task{}, fmt.Errorf("%w: unsupported finish status", ErrInvalid)
	}
	if status == "completed" {
		for _, c := range t.Criteria {
			if !criterionSatisfied(c) {
				return Task{}, fmt.Errorf("%w: %s", ErrUnmetCriteria, c.ID)
			}
		}
	}
	t.Status = status
	t.UpdatedAt = time.Now().UTC()
	if err = s.saveLocked(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// RequestCompletion records an explicit user/CLI request to complete the
// task. Native stop handling uses this bit to avoid turning an informational
// stop into an implied completion attempt.
func (s *Store) RequestCompletion(taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getLocked(taskID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if t.Status != "active" {
		return ErrInactive
	}
	if t.CompletionRequested {
		return nil
	}
	t.CompletionRequested = true
	t.UpdatedAt = time.Now().UTC()
	return s.saveLocked(t)
}

// LatestForSession returns the most recently changed task for a project and
// native session, including finished tasks for status/history views.
func (s *Store) LatestForSession(project, session string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := scanTask(s.db.QueryRow(taskSelect+` WHERE project=? AND session_id=? ORDER BY updated_at DESC LIMIT 1`, project, session))
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) saveLocked(t Task) error {
	return updateTaskExec(s.db, t)
}

type taskExecer interface {
	Exec(string, ...any) (sql.Result, error)
}

func updateTaskExec(exec taskExecer, t Task) error {
	res, err := exec.Exec(`UPDATE focus_tasks SET project=?,session_id=?,thread_id=?,goal=?,status=?,mode=?,criteria_json=?,paths_json=?,excluded_paths_json=?,amendments_json=?,checks_json=?,gaps_json=?,owner_root_thread=?,stop_gate=?,permission_mode=?,revision=revision+1,hook_observed=?,completion_requested=?,updated_at=? WHERE id=? AND revision=?`, t.Project, t.SessionID, t.ThreadID, t.Goal, t.Status, t.Mode, mustJSON(t.Criteria), mustJSON(t.Paths), mustJSON(t.ExcludedPaths), mustJSON(t.Amendments), mustJSON(t.Checks), mustJSON(t.CoverageGaps), t.OwnerRootThread, boolInt(t.StopGate), t.PermissionMode, boolInt(t.HookObserved), boolInt(t.CompletionRequested), t.UpdatedAt.UnixNano(), t.ID, t.Revision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	t.Revision++
	return nil
}

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var criteria, paths, excluded, amendments, checks, gaps string
	var created, updated int64
	var stop, observed, completionRequested int
	err := row.Scan(&t.ID, &t.Project, &t.SessionID, &t.ThreadID, &t.Goal, &t.Status, &t.Mode, &criteria, &paths, &excluded, &amendments, &checks, &gaps, &t.OwnerRootThread, &stop, &t.PermissionMode, &t.Revision, &observed, &completionRequested, &created, &updated)
	if err != nil {
		return t, err
	}
	for _, v := range []struct {
		s string
		p any
	}{{criteria, &t.Criteria}, {paths, &t.Paths}, {excluded, &t.ExcludedPaths}, {amendments, &t.Amendments}, {checks, &t.Checks}, {gaps, &t.CoverageGaps}} {
		if v.s != "" {
			if e := json.Unmarshal([]byte(v.s), v.p); e != nil {
				return t, e
			}
		}
	}
	t.StopGate = stop != 0
	t.HookObserved = observed != 0
	t.CompletionRequested = completionRequested != 0
	t.CreatedAt = time.Unix(0, created).UTC()
	t.UpdatedAt = time.Unix(0, updated).UTC()
	return t, nil
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "focus-" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("focus-%d", time.Now().UnixNano())
}
func cloneCriteria(in []Criterion) []Criterion { out := append([]Criterion(nil), in...); return out }
func normalizePaths(in []string) []string {
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(filepath.ToSlash(p))
		p = strings.TrimPrefix(p, "./")
		if p != "" && !containsPath(out, p) {
			out = append(out, p)
		}
	}
	return out
}
func containsPath(xs []string, p string) bool {
	for _, x := range xs {
		if x == p {
			return true
		}
	}
	return false
}
func appendUnique(xs []string, p string) []string {
	if !containsPath(xs, p) {
		return append(xs, p)
	}
	return xs
}
func validCheckStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pass", "passed", "fail", "failed", "pending", "unknown", "skipped", "invalidated":
		return true
	}
	return false
}
func canonicalCheckStatus(s string) string {
	if strings.EqualFold(s, "passed") {
		return "pass"
	}
	if strings.EqualFold(s, "failed") {
		return "fail"
	}
	return strings.ToLower(strings.TrimSpace(s))
}
func criterionSatisfied(c Criterion) bool {
	return strings.EqualFold(c.Status, "pass") || strings.EqualFold(c.Status, "passed") || strings.EqualFold(c.Status, "complete")
}
func trustedCheckSource(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "wrapper", "trusted_wrapper", "root", "root_cli", "cli", "codex_wrapper", "sparestep-exec", "sparestep_exec":
		return true
	}
	return false
}
func trustedReviewSource(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "agent-review") || strings.EqualFold(strings.TrimSpace(s), "trusted_review")
}
func canonicalFinishStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "complete", "completed", "done", "success":
		return "completed"
	case "failed", "failure":
		return "failed"
	case "cancelled", "canceled", "cancel":
		return "cancelled"
	case "stopped", "stop":
		return "stopped"
	}
	return ""
}
