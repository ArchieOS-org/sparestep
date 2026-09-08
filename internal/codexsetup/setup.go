// Package codexsetup records the exact hashes of Sparestep's existing project
// hooks through Codex's public app-server API.
package codexsetup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const setupTimeout = 10 * time.Second

var expectedEvents = map[string]struct{}{
	"sessionStart":     {},
	"userPromptSubmit": {},
	"preToolUse":       {},
	"postToolUse":      {},
	"stop":             {},
	"postCompact":      {},
	"subagentStart":    {},
	"subagentStop":     {},
	"interrupt":        {},
}

// Result describes the setup state without exposing app-server diagnostics,
// which may contain paths or other user data.
type Result struct {
	Status     string `json:"status"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
	Changed    bool   `json:"changed"`
}

type hookInfo struct {
	Key         string          `json:"key"`
	EventName   string          `json:"eventName"`
	HandlerType string          `json:"handlerType"`
	Command     string          `json:"command"`
	Async       *bool           `json:"async"`
	Matcher     json.RawMessage `json:"matcher"`
	TimeoutSec  *int            `json:"timeoutSec"`
	SourcePath  string          `json:"sourcePath"`
	Source      string          `json:"source"`
	Enabled     bool            `json:"enabled"`
	IsManaged   *bool           `json:"isManaged"`
	CurrentHash string          `json:"currentHash"`
	TrustStatus string          `json:"trustStatus"`
}

type hookList struct {
	Hooks    []hookInfo `json:"hooks"`
	Errors   []any      `json:"errors"`
	Warnings []any      `json:"warnings"`
}

type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// Setup trusts only the existing generated Sparestep project handlers by
// their current hashes. Hook installation remains the caller's responsibility.
// It does not edit Codex's private trust store or use a trust bypass.
func Setup(project, binary, stateDir string) (Result, error) {
	project, binary, stateDir, err := absoluteInputs(project, binary, stateDir)
	if err != nil {
		return Result{}, err
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		return Result{
			Status:     "codex_missing",
			Message:    "Codex CLI was not found, so Sparestep could not trust its project hooks.",
			NextAction: "Install Codex CLI or add codex to PATH, then use /sparestep again.",
		}, nil
	}
	return setupWith(context.Background(), codex, project, binary, stateDir)
}

func absoluteInputs(project, binary, stateDir string) (string, string, string, error) {
	if project == "" || binary == "" || stateDir == "" {
		return "", "", "", errors.New("project, binary, and state directory are required")
	}
	var err error
	project, err = filepath.Abs(project)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve project: %w", err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve binary: %w", err)
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve state directory: %w", err)
	}
	return filepath.Clean(project), filepath.Clean(binary), filepath.Clean(stateDir), nil
}

func setupWith(parent context.Context, codex, project, binary, stateDir string) (Result, error) {
	ctx, cancel := context.WithTimeout(parent, setupTimeout)
	defer cancel()
	server, err := startServer(ctx, codex, project)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{Status: "unsupported", Message: "Codex did not respond to its public app-server API before setup timed out.", NextAction: "Upgrade Codex CLI and use /sparestep again."}, nil
		}
		return Result{Status: "unsupported", Message: "This Codex CLI does not expose the public hook setup API Sparestep needs.", NextAction: "Upgrade Codex CLI and use /sparestep again."}, nil
	}
	defer server.close()

	first, err := server.listHooks(ctx, project)
	if err != nil {
		return unsupportedResult(ctx), nil
	}
	expectedCommand := hookCommand(binary, project, stateDir)
	selected, err := selectHooks(first, project, expectedCommand)
	if err != nil {
		if hasDisabledExactHook(first, project, expectedCommand) {
			return disabledResult(), nil
		}
		return notLoadedResult(), nil
	}

	changed := false
	trustState := make(map[string]any, len(selected))
	for _, item := range selected {
		if item.TrustStatus != "trusted" {
			changed = true
		}
		trustState[item.Key] = map[string]string{"trusted_hash": item.CurrentHash}
	}
	if changed {
		if err := server.batchWrite(ctx, trustState); err != nil {
			return needsReviewResult(), nil
		}
	} else {
		// The catalog already proves every selected handler is trusted and
		// enabled. There was no mutation to verify with another round trip.
		return Result{Status: "ready", Message: "Sparestep is connected to this project."}, nil
	}

	final, err := server.listHooks(ctx, project)
	if err != nil {
		return unsupportedResult(ctx), nil
	}
	if _, err := selectTrustedHooks(final, project, expectedCommand); err != nil {
		if hasDisabledExactHook(final, project, expectedCommand) {
			return disabledResult(), nil
		}
		return needsReviewResult(), nil
	}
	result := Result{Status: "ready", Message: "Sparestep is connected to this project.", Changed: changed}
	if changed {
		result.NextAction = "Open a new Codex task in this project once to load the connection."
	}
	return result, nil
}

func startServer(ctx context.Context, codex, project string) (*server, error) {
	cmd := exec.CommandContext(ctx, codex, "app-server", "--stdio")
	// Codex may be launched through a wrapper process. Bound the lifetime of
	// the entire setup process group, including a wrapper's native child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return stopProcessGroup(cmd) }
	cmd.Dir = project
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &server{cmd: cmd, in: stdin, decoder: bufio.NewReader(stdout), encoder: json.NewEncoder(stdin)}, nil
}

type server struct {
	cmd         *exec.Cmd
	in          io.WriteCloser
	decoder     *bufio.Reader
	encoder     *json.Encoder
	initialized bool
}

func (s *server) close() {
	_ = s.in.Close()
	_ = stopProcessGroup(s.cmd)
	_ = s.cmd.Wait()
}

func stopProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (s *server) call(method string, id int, params any) (json.RawMessage, error) {
	request := map[string]any{"id": id, "method": method, "params": params}
	if err := s.encoder.Encode(request); err != nil {
		return nil, err
	}
	for {
		line, err := s.decoder.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var response rpcResponse
		if json.Unmarshal(line, &response) != nil || response.ID == nil || *response.ID != id {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return nil, errors.New("app-server request failed")
		}
		if len(response.Result) == 0 {
			return nil, errors.New("app-server returned no result")
		}
		return response.Result, nil
	}
}

func (s *server) initialize() error {
	if _, err := s.call("initialize", 1, map[string]any{
		"clientInfo":   map[string]string{"name": "sparestep-setup", "version": "0.2.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}); err != nil {
		return err
	}
	return s.encoder.Encode(map[string]any{"method": "initialized"})
}

func (s *server) listHooks(_ context.Context, project string) (hookList, error) {
	if err := s.initializeIfNeeded(); err != nil {
		return hookList{}, err
	}
	result, err := s.call("hooks/list", 2, map[string]any{"cwds": []string{project}})
	if err != nil {
		return hookList{}, err
	}
	var data struct {
		Data []hookList `json:"data"`
	}
	if json.Unmarshal(result, &data) != nil || len(data.Data) != 1 {
		return hookList{}, errors.New("invalid hooks/list response")
	}
	if len(data.Data[0].Errors) != 0 || len(data.Data[0].Warnings) != 0 {
		return hookList{}, errors.New("hooks/list reported configuration diagnostics")
	}
	return data.Data[0], nil
}

func (s *server) initializeIfNeeded() error {
	if s.initialized {
		return nil
	}
	if err := s.initialize(); err != nil {
		return err
	}
	s.initialized = true
	return nil
}

func (s *server) batchWrite(_ context.Context, trustState map[string]any) error {
	_, err := s.call("config/batchWrite", 3, map[string]any{
		"edits": []map[string]any{{
			"keyPath":       "hooks.state",
			"value":         trustState,
			"mergeStrategy": "upsert",
		}},
		"filePath":         nil,
		"expectedVersion":  nil,
		"reloadUserConfig": true,
	})
	return err
}

func hookCommand(binary, project, stateDir string) string {
	return shellQuote(binary) + " hook --project " + shellQuote(project) + " --state-dir " + shellQuote(stateDir) + " # Sparestep recording"
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func selectHooks(list hookList, project, command string) (map[string]hookInfo, error) {
	selected := make(map[string]hookInfo, len(expectedEvents))
	sourcePath := filepath.Join(project, ".codex", "hooks.json")
	for _, item := range list.Hooks {
		if _, wanted := expectedEvents[item.EventName]; !wanted || !matches(item, project, sourcePath, command) {
			continue
		}
		if _, duplicate := selected[item.EventName]; duplicate {
			return nil, errors.New("duplicate Sparestep handler")
		}
		selected[item.EventName] = item
	}
	if len(selected) != len(expectedEvents) {
		return nil, errors.New("not all Sparestep handlers were loaded")
	}
	return selected, nil
}

func selectTrustedHooks(list hookList, project, command string) (map[string]hookInfo, error) {
	selected, err := selectHooks(list, project, command)
	if err != nil {
		return nil, err
	}
	for _, item := range selected {
		if !item.Enabled || item.TrustStatus != "trusted" {
			return nil, errors.New("Sparestep handler is not trusted and enabled")
		}
	}
	return selected, nil
}

func hasDisabledExactHook(list hookList, project, command string) bool {
	sourcePath := filepath.Join(project, ".codex", "hooks.json")
	for _, item := range list.Hooks {
		if _, wanted := expectedEvents[item.EventName]; wanted && matchesDefinition(item, project, sourcePath, command) && !item.Enabled {
			return true
		}
	}
	return false
}

func matches(item hookInfo, project, sourcePath, command string) bool {
	if !matchesDefinition(item, project, sourcePath, command) || !item.Enabled {
		return false
	}
	return emptyMatcher(item.Matcher)
}

func matchesDefinition(item hookInfo, project, sourcePath, command string) bool {
	return item.Key != "" && item.CurrentHash != "" && item.HandlerType == "command" && item.Command == command && item.Source == "project" && filepath.Clean(item.SourcePath) == filepath.Clean(sourcePath) && item.IsManaged != nil && !*item.IsManaged && item.Async != nil && !*item.Async && item.TimeoutSec != nil && *item.TimeoutSec == 2 && emptyMatcher(item.Matcher)
}

func emptyMatcher(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == ""
}

func notLoadedResult() Result {
	return Result{Status: "not_loaded", Message: "Codex did not load all required Sparestep project handlers.", NextAction: "Open a new Codex task in this project, then use /sparestep again."}
}

func disabledResult() Result {
	return Result{Status: "disabled", Message: "One or more Sparestep project handlers are disabled in Codex.", NextAction: "Open /hooks, enable the disabled Sparestep entries, then use /sparestep again."}
}

func needsReviewResult() Result {
	return Result{Status: "needs_review", Message: "The connection is installed; Codex could not finish its hook review automatically.", NextAction: "Open /hooks and trust the Sparestep entries, then use /sparestep again."}
}

func unsupportedResult(ctx context.Context) Result {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{Status: "unsupported", Message: "Codex did not respond to its public app-server API before setup timed out.", NextAction: "Upgrade Codex CLI and use /sparestep again."}
	}
	return Result{Status: "unsupported", Message: "This Codex CLI does not expose the public hook setup API Sparestep needs.", NextAction: "Upgrade Codex CLI and use /sparestep again."}
}
