package linear

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const leaseDuration = 2 * time.Minute

type Outbox struct {
	db          *sql.DB
	mu          sync.RWMutex
	client      ToolClient
	destination Destination
	endpoint    string
	tools       []Tool
	toolsLoaded bool
	closed      bool
}

// Open creates or opens a self-contained SQLite outbox. Database and parent
// directory permissions are tightened because OAuth state and private finding
// evidence may live alongside this file.
func Open(path string) (*Outbox, error) {
	if path == "" {
		return nil, errors.New("linear: database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + url.PathEscape(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	o := &Outbox{db: db, endpoint: "https://mcp.linear.app/mcp"}
	if err = o.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0600)
	}
	return o, nil
}

func (o *Outbox) init() error {
	_, err := o.db.Exec(`
CREATE TABLE IF NOT EXISTS linear_outbox (
 id TEXT PRIMARY KEY,
 fingerprint TEXT NOT NULL UNIQUE,
 task_id TEXT NOT NULL DEFAULT '',
 repository TEXT NOT NULL,
 component TEXT NOT NULL,
	problem TEXT NOT NULL,
	problem_key TEXT NOT NULL DEFAULT '',
	type_label_id TEXT NOT NULL DEFAULT '',
	area_label_id TEXT NOT NULL DEFAULT '',
	surface_label_id TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL,
 body TEXT NOT NULL,
 status TEXT NOT NULL,
 issue_url TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 lease_until INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS linear_outbox_task ON linear_outbox(task_id,created_at);
CREATE INDEX IF NOT EXISTS linear_outbox_status ON linear_outbox(status,lease_until,created_at);
CREATE TABLE IF NOT EXISTS linear_outbox_tasks (
 item_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(item_id,task_id),
 FOREIGN KEY(item_id) REFERENCES linear_outbox(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS linear_outbox_tasks_task ON linear_outbox_tasks(task_id,created_at);
`)
	for _, stmt := range []string{`ALTER TABLE linear_outbox ADD COLUMN problem_key TEXT NOT NULL DEFAULT ''`, `ALTER TABLE linear_outbox ADD COLUMN type_label_id TEXT NOT NULL DEFAULT ''`, `ALTER TABLE linear_outbox ADD COLUMN area_label_id TEXT NOT NULL DEFAULT ''`, `ALTER TABLE linear_outbox ADD COLUMN surface_label_id TEXT NOT NULL DEFAULT ''`} {
		_, _ = o.db.Exec(stmt)
	}
	return err
}

func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	if o.client != nil {
		_ = o.client.Close()
		o.client = nil
	}
	return o.db.Close()
}

func (o *Outbox) Configure(d Destination) error {
	if err := validateDestination(d); err != nil {
		return err
	}
	if d.State == "" {
		d.State = "backlog"
	}
	o.mu.Lock()
	o.destination = d
	o.mu.Unlock()
	// Configuration and policy failures are repairable. A corrected
	// destination should make those records eligible on the next flush; an
	// ambiguous remote create remains protected until reconciliation.
	_, _ = o.db.Exec(`UPDATE linear_outbox SET status=?,last_error='',lease_until=0,updated_at=? WHERE status IN (?,?)`, string(StatusQueued), time.Now().UTC().UnixNano(), string(StatusNeedsAttention), string(StatusAuthRequired))
	return nil
}

// MarkAttention makes configuration and policy failures visible without
// losing the durable item. It is used by the CLI when it cannot even begin a
// publish attempt (for example, a project mapping is missing).
func (o *Outbox) MarkAttention(taskID, message string) error {
	_, err := o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE task_id=? AND status IN (?,?)`, string(StatusNeedsAttention), boundedRedacted(message, 2000), time.Now().UTC().UnixNano(), taskID, string(StatusQueued), string(StatusAuthRequired))
	return err
}

func (o *Outbox) Destination() Destination { o.mu.RLock(); defer o.mu.RUnlock(); return o.destination }

// SetClient is primarily useful for tests and for callers that already own an
// MCP session. Connect is the production OAuth path.
func (o *Outbox) SetClient(c ToolClient) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.client = c
	o.tools = nil
	o.toolsLoaded = false
}

func (o *Outbox) Enqueue(i Item) (Item, error) {
	if err := validateItem(i); err != nil {
		return Item{}, err
	}
	i.TaskID = bounded(i.TaskID, 256)
	i.Repository = boundedRedacted(i.Repository, 512)
	i.Component = boundedRedacted(i.Component, 512)
	i.Problem = boundedRedacted(i.Problem, 2000)
	i.ProblemKey = boundedRedacted(i.ProblemKey, 2000)
	if i.ProblemKey == "" {
		i.ProblemKey = i.Problem
	}
	if i.TypeLabelID == "" {
		i.TypeLabelID = i.TypeLabel
	}
	if i.AreaLabelID == "" {
		i.AreaLabelID = i.AreaLabel
	}
	if i.SurfaceLabelID == "" {
		i.SurfaceLabelID = i.SurfaceLabel
	}
	i.Title = boundedRedacted(i.Title, 300)
	if i.Title == "" {
		i.Title = boundedRedacted(i.Problem, 300)
	}
	i.Body = boundedRedacted(i.Body, 12000)
	if i.Body == "" {
		i.Body = ComposeBody(i.Problem, nil, nil)
	}
	i.Status = StatusQueued
	fp := fingerprint(i.Repository, i.Component, i.ProblemKey)
	i.ID = fp
	now := time.Now().UTC().UnixNano()
	_, err := o.db.Exec(`INSERT INTO linear_outbox(id,fingerprint,task_id,repository,component,problem,problem_key,type_label_id,area_label_id,surface_label_id,title,body,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(fingerprint) DO NOTHING`, i.ID, fp, i.TaskID, i.Repository, i.Component, i.Problem, i.ProblemKey, i.TypeLabelID, i.AreaLabelID, i.SurfaceLabelID, i.Title, i.Body, string(StatusQueued), now, now)
	if err != nil {
		return Item{}, err
	}
	if i.TaskID != "" {
		_, err = o.db.Exec(`INSERT OR IGNORE INTO linear_outbox_tasks(item_id,task_id,created_at) VALUES(?,?,?)`, i.ID, i.TaskID, now)
		if err != nil {
			return Item{}, err
		}
	}
	saved, err := o.itemByFingerprint(fp)
	if err == nil && i.TaskID != "" {
		saved.TaskID = i.TaskID
	}
	return saved, err
}

func (o *Outbox) Items(taskID string) ([]Item, error) {
	rows, err := o.db.Query(`SELECT o.id,?,o.repository,o.component,o.problem,o.problem_key,o.type_label_id,o.area_label_id,o.surface_label_id,o.title,o.body,o.status,o.issue_url,o.last_error,o.attempts,o.lease_until,o.created_at,o.updated_at FROM linear_outbox o JOIN linear_outbox_tasks r ON r.item_id=o.id WHERE r.task_id=? ORDER BY o.created_at,o.id`, taskID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// Pending returns all non-terminal records so an operator can see ambiguous,
// authentication-blocked, and policy-blocked work as well as queued work.
func (o *Outbox) Pending() ([]Item, error) {
	rows, err := o.db.Query(`SELECT o.id,COALESCE(r.task_id,o.task_id),o.repository,o.component,o.problem,o.problem_key,o.type_label_id,o.area_label_id,o.surface_label_id,o.title,o.body,o.status,o.issue_url,o.last_error,o.attempts,o.lease_until,o.created_at,o.updated_at FROM linear_outbox o LEFT JOIN (SELECT item_id,MIN(task_id) AS task_id FROM linear_outbox_tasks GROUP BY item_id) r ON r.item_id=o.id WHERE o.status<>? ORDER BY o.created_at,o.id`, string(StatusCreated))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

func scanItems(rows *sql.Rows) ([]Item, error) {
	out := make([]Item, 0)
	for rows.Next() {
		var i Item
		var status string
		var lease, created, updated int64
		if err := rows.Scan(&i.ID, &i.TaskID, &i.Repository, &i.Component, &i.Problem, &i.ProblemKey, &i.TypeLabelID, &i.AreaLabelID, &i.SurfaceLabelID, &i.Title, &i.Body, &status, &i.IssueURL, &i.LastError, &i.attempts, &lease, &created, &updated); err != nil {
			return nil, err
		}
		i.Status = Status(status)
		i.leaseUntil = time.Unix(0, lease)
		i.createdAt = time.Unix(0, created)
		out = append(out, i)
	}
	return out, rows.Err()
}

func (o *Outbox) itemByFingerprint(fp string) (Item, error) {
	row := o.db.QueryRow(`SELECT id,task_id,repository,component,problem,problem_key,type_label_id,area_label_id,surface_label_id,title,body,status,issue_url,last_error,attempts,lease_until,created_at,updated_at FROM linear_outbox WHERE fingerprint=?`, fp)
	var i Item
	var status string
	var lease, created, updated int64
	if err := row.Scan(&i.ID, &i.TaskID, &i.Repository, &i.Component, &i.Problem, &i.ProblemKey, &i.TypeLabelID, &i.AreaLabelID, &i.SurfaceLabelID, &i.Title, &i.Body, &status, &i.IssueURL, &i.LastError, &i.attempts, &lease, &created, &updated); err != nil {
		return Item{}, err
	}
	i.Status = Status(status)
	i.leaseUntil = time.Unix(0, lease)
	i.createdAt = time.Unix(0, created)
	return i, nil
}

func (o *Outbox) claim(taskID ...string) (Item, bool, error) {
	tx, err := o.db.Begin()
	if err != nil {
		return Item{}, false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().UnixNano()
	// A crashed publisher may have completed the remote create before dying.
	// Preserve the no-duplicate guarantee by making expired sends ambiguous;
	// only reconciliation can settle them.
	if _, err = tx.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status=? AND lease_until>0 AND lease_until<?`, string(StatusAmbiguous), "publisher lease expired before remote result was recorded", now, string(StatusSending), now); err != nil {
		return Item{}, false, err
	}
	var row *sql.Row
	if len(taskID) > 0 && taskID[0] != "" {
		row = tx.QueryRow(`SELECT o.id,o.task_id,o.repository,o.component,o.problem,o.problem_key,o.type_label_id,o.area_label_id,o.surface_label_id,o.title,o.body,o.status,o.issue_url,o.last_error,o.attempts,o.lease_until,o.created_at,o.updated_at FROM linear_outbox o WHERE o.status=? AND EXISTS (SELECT 1 FROM linear_outbox_tasks r WHERE r.item_id=o.id AND r.task_id=?) ORDER BY o.created_at,o.id LIMIT 1`, string(StatusQueued), taskID[0])
	} else {
		row = tx.QueryRow(`SELECT id,task_id,repository,component,problem,problem_key,type_label_id,area_label_id,surface_label_id,title,body,status,issue_url,last_error,attempts,lease_until,created_at,updated_at FROM linear_outbox WHERE status=? ORDER BY created_at,id LIMIT 1`, string(StatusQueued))
	}
	var i Item
	var status string
	var lease, created, updated int64
	if err = row.Scan(&i.ID, &i.TaskID, &i.Repository, &i.Component, &i.Problem, &i.ProblemKey, &i.TypeLabelID, &i.AreaLabelID, &i.SurfaceLabelID, &i.Title, &i.Body, &status, &i.IssueURL, &i.LastError, &i.attempts, &lease, &created, &updated); err == sql.ErrNoRows {
		return Item{}, false, tx.Commit()
	} else if err != nil {
		return Item{}, false, err
	}
	leaseUntil := time.Now().UTC().Add(leaseDuration).UnixNano()
	res, err := tx.Exec(`UPDATE linear_outbox SET status=?,attempts=attempts+1,lease_until=?,updated_at=? WHERE id=? AND status=?`, string(StatusSending), leaseUntil, now, i.ID, string(StatusQueued))
	if err != nil {
		return Item{}, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Item{}, false, tx.Commit()
	}
	if err = tx.Commit(); err != nil {
		return Item{}, false, err
	}
	i.Status = StatusSending
	i.attempts++
	i.leaseUntil = time.Unix(0, leaseUntil)
	i.createdAt = time.Unix(0, created)
	return i, true, nil
}

// expireSending records the uncertainty of a publisher that stopped holding
// its lease. The remote MCP call may have completed before the process died,
// so these rows must be reconciled before they become eligible for create.
func (o *Outbox) expireSending() error {
	now := time.Now().UTC().UnixNano()
	_, err := o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status=? AND lease_until>0 AND lease_until<?`, string(StatusAmbiguous), "publisher lease expired before remote result was recorded", now, string(StatusSending), now)
	return err
}

func (o *Outbox) setState(id string, status Status, issueURL, lastError string) error {
	if len(lastError) > 2000 {
		lastError = lastError[:2000]
	}
	_, err := o.db.Exec(`UPDATE linear_outbox SET status=?,issue_url=?,last_error=?,lease_until=0,updated_at=? WHERE id=?`, string(status), issueURL, boundedRedacted(lastError, 2000), time.Now().UTC().UnixNano(), id)
	return err
}

func (o *Outbox) markAuthRequired(msg string) error {
	_, err := o.db.Exec(`UPDATE linear_outbox SET status=?,last_error=?,lease_until=0,updated_at=? WHERE status=?`, string(StatusAuthRequired), boundedRedacted(msg, 2000), time.Now().UTC().UnixNano(), string(StatusSending))
	return err
}

func (o *Outbox) marshalJSON(i Item) ([]byte, error) { return json.Marshal(i) }

func normalizeToolName(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "-", "_"), " ", "_"))
}
