// Package linear provides a durable, OAuth-capable publisher for Sparestep
// findings.  The package deliberately speaks to Linear through its MCP tools;
// it never stores a Linear API key and never performs updates or deletes.
package linear

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	StatusQueued         Status = "queued"
	StatusSending        Status = "sending"
	StatusCreated        Status = "created"
	StatusAmbiguous      Status = "ambiguous"
	StatusAuthRequired   Status = "auth_required"
	StatusNeedsAttention Status = "needs_attention"
)

var (
	ErrNotConnected = errors.New("linear: MCP client is not connected")
	ErrAuthRequired = errors.New("linear: authentication is required")
	ErrAmbiguous    = errors.New("linear: create outcome is ambiguous; reconcile before retrying")
)

// Item is the JSON-compatible durable record kept in the local outbox.  The
// fingerprint is intentionally not exported: it is derived only from
// repository, component, and problem, so wording/task/worktree changes do not
// create a second issue.
type Item struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Repository     string `json:"repository"`
	Component      string `json:"component"`
	Problem        string `json:"problem"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Status         Status `json:"status"`
	IssueURL       string `json:"issue_url,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	TypeLabelID    string `json:"-"`
	AreaLabelID    string `json:"-"`
	SurfaceLabelID string `json:"-"`
	TypeLabel      string `json:"-"`
	AreaLabel      string `json:"-"`
	SurfaceLabel   string `json:"-"`
	ProblemKey     string `json:"-"`

	createdAt  time.Time
	attempts   int
	leaseUntil time.Time
}

type Destination struct {
	TeamID         string `json:"team_id"`
	ProjectID      string `json:"project_id"`
	AssigneeID     string `json:"assignee_id"`
	TypeLabelID    string `json:"type_label_id,omitempty"`
	AreaLabelID    string `json:"area_label_id,omitempty"`
	SurfaceLabelID string `json:"surface_label_id,omitempty"`
	MilestoneID    string `json:"milestone_id,omitempty"`
	State          string `json:"state,omitempty"`

	// The aliases make configuration from human-readable MCP policy output
	// convenient while preserving IDs as the canonical create arguments.
	Team, Project, Assignee                       string `json:"-"`
	TypeLabel, AreaLabel, SurfaceLabel, Milestone string `json:"-"`
}

func (d Destination) team() string {
	if d.TeamID != "" {
		return d.TeamID
	}
	return d.Team
}
func (d Destination) project() string {
	if d.ProjectID != "" {
		return d.ProjectID
	}
	return d.Project
}
func (d Destination) assignee() string {
	if d.AssigneeID != "" {
		return d.AssigneeID
	}
	return d.Assignee
}
func (d Destination) typeLabel() string {
	if d.TypeLabelID != "" {
		return d.TypeLabelID
	}
	return d.TypeLabel
}
func (d Destination) areaLabel() string {
	if d.AreaLabelID != "" {
		return d.AreaLabelID
	}
	return d.AreaLabel
}
func (d Destination) surfaceLabel() string {
	if d.SurfaceLabelID != "" {
		return d.SurfaceLabelID
	}
	return d.SurfaceLabel
}
func (d Destination) milestone() string {
	if d.MilestoneID != "" {
		return d.MilestoneID
	}
	return d.Milestone
}

// Tool describes one discovered MCP tool. InputSchema is the server-supplied
// JSON Schema and is intentionally kept as an interface for fake MCP clients.
type Tool struct {
	Name        string
	Description string
	InputSchema any
}

type ToolResult struct {
	Content           []Content
	StructuredContent any
	IsError           bool
}

type Content struct {
	Type string
	Text string
}

// ToolClient is the narrow MCP surface used by the publisher. Production code
// gets an implementation from the official Go MCP SDK; tests can provide a
// deterministic fake without a network or browser.
type ToolClient interface {
	ListTools(context.Context) ([]Tool, error)
	CallTool(context.Context, string, map[string]any) (ToolResult, error)
	Close() error
}

// ItemInput is useful when callers have separate acceptance criteria and
// evidence. Body is still what is persisted and sent to Linear.
type ItemInput struct {
	TaskID, Repository, Component, Problem, Title        string
	ProblemKey, TypeLabelID, AreaLabelID, SurfaceLabelID string
	TypeLabel, AreaLabel, SurfaceLabel                   string
	Criteria                                             []string
	Evidence                                             []string
}

func NewItem(in ItemInput) Item {
	body := ComposeBody(in.Problem, in.Criteria, in.Evidence)
	return Item{TaskID: bounded(in.TaskID, 256), Repository: bounded(in.Repository, 512), Component: bounded(in.Component, 512), Problem: boundedRedacted(in.Problem, 2000), ProblemKey: boundedRedacted(in.ProblemKey, 2000), TypeLabelID: in.TypeLabelID, AreaLabelID: in.AreaLabelID, SurfaceLabelID: in.SurfaceLabelID, TypeLabel: in.TypeLabel, AreaLabel: in.AreaLabel, SurfaceLabel: in.SurfaceLabel, Title: boundedRedacted(in.Title, 300), Body: boundedRedacted(body, 12000), Status: StatusQueued}
}

func ComposeBody(problem string, criteria, evidence []string) string {
	var b strings.Builder
	b.WriteString("Problem\n")
	b.WriteString(boundedRedacted(problem, 2000))
	b.WriteString("\n\nVerified done\n")
	if len(criteria) == 0 {
		b.WriteString("- Acceptance criteria to be verified in the owning task\n")
	} else {
		for _, value := range criteria {
			b.WriteString("- ")
			b.WriteString(boundedRedacted(value, 1200))
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nObservable scenarios\n")
	if len(evidence) == 0 {
		b.WriteString("- No observed scenario supplied\n")
	} else {
		for _, value := range evidence {
			b.WriteString("- ")
			b.WriteString(boundedRedacted(value, 1600))
			b.WriteByte('\n')
		}
	}
	return bounded(b.String(), 12000)
}

func fingerprint(repository, component, problem string) string {
	canonical := strings.Join([]string{normalize(repository), normalize(component), normalize(problem)}, "\x00")
	h := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(h[:])
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}
func bounded(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

var secretPattern = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+|bearer\s+|(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|secret)\s*[:=]\s*)[^\s,;]+`)
var privatePathPattern = regexp.MustCompile(`/home/[^\s)]+`)

func boundedRedacted(s string, n int) string {
	s = secretPattern.ReplaceAllString(s, "$1[REDACTED]")
	s = privatePathPattern.ReplaceAllString(s, "[REDACTED_PATH]")
	return bounded(s, n)
}

func validateItem(i Item) error {
	if strings.TrimSpace(i.Repository) == "" {
		return errors.New("linear: repository is required")
	}
	if strings.TrimSpace(i.Component) == "" {
		return errors.New("linear: component is required")
	}
	if strings.TrimSpace(i.Problem) == "" {
		return errors.New("linear: problem is required")
	}
	return nil
}

func validateDestination(d Destination) error {
	if d.team() == "" {
		return errors.New("linear: destination team is required")
	}
	if d.project() == "" {
		return errors.New("linear: destination project is required")
	}
	if d.assignee() == "" {
		return errors.New("linear: destination assignee is required")
	}
	return nil
}

func statusError(s Status, msg string) error { return fmt.Errorf("linear: %s: %s", s, msg) }

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") || strings.Contains(s, "403") || strings.Contains(s, "unauthorized") || strings.Contains(s, "forbidden") || strings.Contains(s, "authorization") || strings.Contains(s, "auth")
}

func isAmbiguousError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") || strings.Contains(s, "timed out") || strings.Contains(s, "eof") || strings.Contains(s, "connection reset") || strings.Contains(s, "broken pipe")
}

func sortedTools(tools []Tool) []Tool {
	out := append([]Tool(nil), tools...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
