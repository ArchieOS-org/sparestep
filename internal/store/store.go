package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.init(); err != nil {
		db.Close()
		return nil, err
	}
	// Only tighten permissions on the database file itself.  In particular, do
	// not chmod a directory (or a URI such as :memory:) by accident.
	if st, statErr := os.Lstat(path); statErr == nil && st.Mode().IsRegular() {
		_ = os.Chmod(path, 0600)
	}
	return s, nil
}
func (s *Store) init() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events (id TEXT PRIMARY KEY, source TEXT, project TEXT, session_id TEXT, turn_id TEXT, agent_id TEXT, kind TEXT, tool TEXT, operation_id TEXT, command TEXT, timestamp INTEGER, duration_ms INTEGER, exit_code INTEGER, success INTEGER, state_key TEXT, context_known INTEGER, failure_key TEXT, summary TEXT, usage_json TEXT, gap TEXT);
CREATE INDEX IF NOT EXISTS events_project_time ON events(project,timestamp);
CREATE INDEX IF NOT EXISTS events_timestamp ON events(timestamp);
CREATE TABLE IF NOT EXISTS dispositions (project TEXT NOT NULL, finding_id TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(project,finding_id));
CREATE TABLE IF NOT EXISTS drafts (project TEXT NOT NULL, finding_id TEXT NOT NULL, title TEXT, body TEXT, linear_url TEXT, state TEXT, issue_url TEXT, PRIMARY KEY(project,finding_id));
INSERT OR IGNORE INTO meta(key,value) VALUES ('schema_version','1');`)
	if err != nil {
		return err
	}
	var version string
	if err = s.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return err
	}
	v, err := strconv.Atoi(version)
	if err != nil || v > 1 || v < 1 {
		return fmt.Errorf("unsupported schema version %q", version)
	}
	return nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Record(e model.Event) error {
	paused, err := s.Paused(e.Project)
	if err != nil {
		return err
	}
	if paused {
		return nil
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.ID == "" {
		b, _ := json.Marshal(e)
		h := sha256.Sum256(b)
		e.ID = hex.EncodeToString(h[:])
	}
	var uj string
	if e.Usage != nil {
		b, _ := json.Marshal(e.Usage)
		uj = string(b)
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO events(id,source,project,session_id,turn_id,agent_id,kind,tool,operation_id,command,timestamp,duration_ms,exit_code,success,state_key,context_known,failure_key,summary,usage_json,gap) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.ID, e.Source, e.Project, e.SessionID, e.TurnID, e.AgentID, e.Kind, e.Tool, e.OperationID, e.Command, e.Timestamp.UnixNano(), e.DurationMS, e.ExitCode, boolInt(e.Success), e.StateKey, boolVal(e.ContextKnown), e.FailureKey, e.Summary, uj, e.Gap)
	return err
}
func boolInt(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return 1
	}
	return 0
}
func boolVal(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Store) Events(project string, limit int) ([]model.Event, error) {
	if limit <= 0 {
		return []model.Event{}, nil
	}
	rows, err := s.db.Query(`SELECT id,source,project,session_id,turn_id,agent_id,kind,tool,operation_id,command,timestamp,duration_ms,exit_code,success,state_key,context_known,failure_key,summary,usage_json,gap FROM events WHERE project=? ORDER BY timestamp DESC,id DESC LIMIT ?`, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var ts int64
		var ec, suc, known sql.NullInt64
		var uj string
		if err := rows.Scan(&e.ID, &e.Source, &e.Project, &e.SessionID, &e.TurnID, &e.AgentID, &e.Kind, &e.Tool, &e.OperationID, &e.Command, &ts, &e.DurationMS, &ec, &suc, &e.StateKey, &known, &e.FailureKey, &e.Summary, &uj, &e.Gap); err != nil {
			return nil, err
		}
		e.Timestamp = time.Unix(0, ts)
		if ec.Valid {
			x := int(ec.Int64)
			e.ExitCode = &x
		}
		if suc.Valid {
			x := suc.Int64 != 0
			e.Success = &x
		}
		e.ContextKnown = known.Valid && known.Int64 != 0
		if uj != "" {
			if err = json.Unmarshal([]byte(uj), &e.Usage); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
func (s *Store) SetDisposition(project, id, value string) error {
	if value != "open" && value != "necessary" && value != "dismissed" {
		return fmt.Errorf("invalid disposition")
	}
	if !s.findingExists(project, id) {
		return fmt.Errorf("finding not found")
	}
	_, err := s.db.Exec(`INSERT INTO dispositions(project,finding_id,value) VALUES(?,?,?) ON CONFLICT(project,finding_id) DO UPDATE SET value=excluded.value`, project, id, value)
	return err
}
func (s *Store) SetPaused(project string, v bool) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "paused:"+project, fmt.Sprint(v))
	return err
}
func (s *Store) Paused(project string) (bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, `paused:`+project).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v == "true", err
}
func (s *Store) SetIssueURL(project, id, u string) error {
	if !validLinear(u) {
		return fmt.Errorf("issue URL must be a Linear issue URL")
	}
	if !s.findingExists(project, id) {
		return fmt.Errorf("finding not found")
	}
	// Linking is allowed directly from the report.  Create the durable draft
	// record first when the user did not open the draft command beforehand.
	if _, err := s.Draft(project, id); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE drafts SET issue_url=?, state='linked' WHERE project=? AND finding_id=?`, u, project, id)
	if err == nil {
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("draft not found")
		}
	}
	return err
}
func (s *Store) PurgeBefore(t time.Time) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE timestamp<?`, t.UnixNano())
	return err
}
func (s *Store) disposition(project, id string) string {
	var v string
	if s.db.QueryRow(`SELECT value FROM dispositions WHERE project=? AND finding_id=?`, project, id).Scan(&v) != nil {
		return "open"
	}
	return v
}
func validLinear(u string) bool {
	p, e := url.Parse(u)
	if e != nil || p.Scheme != "https" || p.Hostname() != "linear.app" || p.User != nil || p.Path == "" {
		return false
	}
	parts := strings.Split(strings.Trim(p.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "issue" && parts[i+1] != "" {
			return true
		}
	}
	return false
}

func (s *Store) findingExists(project, id string) bool {
	r, err := s.Report(project)
	if err != nil {
		return false
	}
	for _, f := range r.Findings {
		if f.ID == id {
			return true
		}
	}
	return false
}
