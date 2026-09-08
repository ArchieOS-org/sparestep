package model

import "time"

// Event contains bounded observations, never a complete conversation.
type Event struct {
	ID           string    `json:"id"`
	Source       string    `json:"source"`
	Project      string    `json:"project"`
	SessionID    string    `json:"session_id"`
	TurnID       string    `json:"turn_id,omitempty"`
	AgentID      string    `json:"agent_id,omitempty"`
	Kind         string    `json:"kind"`
	Tool         string    `json:"tool,omitempty"`
	OperationID  string    `json:"operation_id,omitempty"`
	Command      string    `json:"command,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Success      *bool     `json:"success,omitempty"`
	StateKey     string    `json:"state_key,omitempty"`
	ContextKnown bool      `json:"context_known"`
	FailureKey   string    `json:"failure_key,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
	Gap          string    `json:"gap,omitempty"`
}

type Usage struct {
	Input           int64  `json:"input"`
	CachedInput     int64  `json:"cached_input"`
	Output          int64  `json:"output"`
	ReasoningOutput int64  `json:"reasoning_output"`
	Total           int64  `json:"total"`
	Cumulative      bool   `json:"cumulative"`
	Source          string `json:"source"`
}

type Finding struct {
	ID          string   `json:"id"`
	Category    string   `json:"category"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Confidence  string   `json:"confidence"`
	Occurrences int      `json:"occurrences"`
	DurationMS  int64    `json:"duration_ms"`
	EvidenceIDs []string `json:"evidence_ids"`
	Disposition string   `json:"disposition"`
	Suggestion  string   `json:"suggestion"`
	IssueURL    string   `json:"issue_url,omitempty"`
}

type Report struct {
	Project       string    `json:"project"`
	TaskID        string    `json:"task_id"`
	Status        string    `json:"status"`
	Paused        bool      `json:"paused"`
	EventsCount   int       `json:"events_count"`
	Checks        int       `json:"checks"`
	FailedChecks  int       `json:"failed_checks"`
	UnknownChecks int       `json:"unknown_checks"`
	ElapsedMS     int64     `json:"elapsed_ms"`
	TokenUsage    *Usage    `json:"token_usage,omitempty"`
	UsageNote     string    `json:"usage_note"`
	Findings      []Finding `json:"findings"`
	Events        []Event   `json:"events"`
	RecordingGap  string    `json:"recording_gap,omitempty"`
	Hostname      string    `json:"hostname"`
}

type Draft struct {
	FindingID string `json:"finding_id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	LinearURL string `json:"linear_url"`
	State     string `json:"state"`
	IssueURL  string `json:"issue_url,omitempty"`
}
