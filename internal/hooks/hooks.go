// Package hooks adapts Codex project hooks to Sparestep observations.
package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/model"
)

var requiredEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "PostCompact", "SubagentStart", "SubagentStop", "Interrupt"}

// Process converts one Codex hook payload into append-only observations.
func Process(data []byte, configuredProject string, history []model.Event) ([]model.Event, error) {
	var raw map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("hook payload contains trailing JSON")
		}
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("hook payload must be an object")
	}

	now := time.Now().UTC()
	kind := strings.ToLower(firstString(raw, "hook_event_name", "event", "type", "hook"))
	session := firstString(raw, "session_id", "sessionId")
	if session == "" {
		session = nestedString(raw, "session", "id")
	}
	turn := firstString(raw, "turn_id", "turnId")
	cwd := firstString(raw, "cwd", "working_directory", "workingDirectory")
	eventProject := configuredProject
	if cwd == "" {
		// Process the bounded observation for compatibility with hook senders
		// that omit cwd, but leave it unattributed so it cannot be recorded in
		// the configured project.
		eventProject = ""
	} else if !inProject(configuredProject, cwd) {
		// Keep this diagnostic outside the configured project. A missing or
		// foreign cwd must never create a gap attributed to the wrong project.
		return []model.Event{{
			ID:           digest(configuredProject + "\x00" + session + "\x00" + turn + "\x00cwd")[:16],
			Source:       "codex-hook",
			Project:      "",
			SessionID:    session,
			TurnID:       turn,
			Kind:         "gap",
			Timestamp:    now,
			ContextKnown: false,
			Gap:          "event working directory is missing or outside configured project",
		}}, nil
	}

	tool := firstString(raw, "tool_name", "toolName", "tool")
	command := commandString(raw)
	op := operation(raw)
	component, summary := commandSummary(command)
	if component == "" {
		component = bounded(tool)
	}
	base := model.Event{
		Source:       "codex-hook",
		Project:      eventProject,
		SessionID:    session,
		AgentID:      firstString(raw, "agent_id"),
		TurnID:       turn,
		Timestamp:    now,
		ContextKnown: false,
		Tool:         classify(tool, command),
		Command:      summary,
		OperationID:  op,
	}
	if command != "" {
		base.StateKey = digest(eventProject + "\x00" + command)
	}

	switch kind {
	case "sessionstart", "session_start":
		base.Kind = "task_started"
	case "userpromptsubmit", "user_prompt_submit":
		base.Kind = "task_started"
		base.Summary = "user prompt submitted"
	case "pretooluse", "pre_tool_use":
		base.Kind = "tool_started"
		if op == "" {
			base.Gap = "tool operation id unavailable"
		}
	case "posttooluse", "post_tool_use":
		base.Kind = "tool_completed"
		if isApplyPatch(tool, command) || isMutatingCommand(command) {
			base.Kind = "edit"
		}
		setResult(&base, raw, component)
		if op == "" {
			base.Gap = joinGap(base.Gap, "tool operation id unavailable")
		}
		pair(&base, history, hasResponseDuration(raw))
	case "stop":
		base.Kind = "task_completed"
		base.Summary = "task stopped"
	case "postcompact", "post_compact":
		base.Kind = "context_reset"
		base.Summary = "context compacted"
	case "subagentstart":
		base.Kind = "agent_started"
	case "subagentstop":
		base.Kind = "agent_stopped"
	case "interrupt":
		base.Kind = "task_interrupted"
	default:
		base.Kind = "gap"
		base.Gap = "unknown Codex hook event"
	}
	base.ID = eventID(base)
	return []model.Event{base}, nil
}

func eventID(e model.Event) string {
	return digest(strings.Join([]string{e.Project, e.SessionID, e.TurnID, e.Kind, e.OperationID, e.Tool, e.StateKey}, "\x00"))[:16]
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	return ""
}

func nestedString(m map[string]any, a, b string) string {
	if x, ok := m[a].(map[string]any); ok {
		return firstString(x, b)
	}
	return ""
}

func commandString(m map[string]any) string {
	for _, k := range []string{"command", "cmd"} {
		if s := firstString(m, k); s != "" {
			return s
		}
	}
	for _, key := range []string{"tool_input", "toolInput", "input", "arguments"} {
		if x, ok := m[key].(map[string]any); ok {
			if s := commandString(x); s != "" {
				return s
			}
		}
	}
	return ""
}

func operation(m map[string]any) string {
	return firstString(m, "tool_use_id", "toolUseId", "operation_id", "operationId")
}

func inProject(project, cwd string) bool {
	if project == "" || cwd == "" {
		return false
	}
	p, err := filepath.Abs(filepath.Clean(project))
	if err != nil {
		return false
	}
	c, err := filepath.Abs(filepath.Clean(cwd))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(p, c)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func classify(tool, command string) string {
	words := safeWords(command)
	if len(words) > 0 {
		exe := baseExecutable(words[0])
		sub := words[1:]
		switch {
		case exe == "go" && len(sub) > 0 && oneOf(sub[0], "test", "vet", "build"):
			return "check"
		case exe == "npm" && len(sub) > 0 && (sub[0] == "test" || (sub[0] == "run" && len(sub) > 1 && sub[1] == "test")):
			return "check"
		case exe == "make" && len(sub) > 0 && sub[0] == "test":
			return "check"
		case exe == "cargo" && len(sub) > 0 && sub[0] == "test":
			return "check"
		case exe == "pytest" || (exe == "python" && len(sub) > 1 && sub[0] == "-m" && sub[1] == "pytest"):
			return "check"
		case exe == "git" && len(sub) > 0 && oneOf(sub[0], "diff", "status", "show", "log"):
			return "read"
		case oneOf(exe, "cat", "ls", "pwd", "find", "rg"):
			return "read"
		}
	}
	if tool != "" {
		return bounded(tool)
	}
	return ""
}

func safeWords(s string) []string {
	if s == "" || strings.ContainsAny(s, "\r\n;|&><$`'\"") {
		return nil
	}
	return strings.Fields(s)
}

func baseExecutable(s string) string { return strings.ToLower(filepath.Base(s)) }

func safeExecutable(s string) (string, bool) {
	if s == "" || strings.ContainsAny(s, `=:@?'"`) || strings.HasPrefix(s, "-") {
		return "", false
	}
	exe := baseExecutable(s)
	if exe == "" || len(exe) > 64 {
		return "", false
	}
	for _, r := range exe {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '+' && r != '-' {
			return "", false
		}
	}
	return exe, true
}

func commandSummary(command string) (string, string) {
	if strings.ContainsAny(command, "'\"") {
		return "", "[command omitted]"
	}
	words := safeWords(command)
	if len(words) == 0 {
		// Quoting, shell operators, and multiline input are intentionally not
		// parsed. Preserve only a conservative executable-looking first token.
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return "", ""
		}
		exe, ok := safeExecutable(fields[0])
		if !ok {
			return "", "[command omitted]"
		}
		return exe, exe + " [arguments omitted]"
	}
	exe, ok := safeExecutable(words[0])
	if !ok {
		return "", "[command omitted]"
	}
	if len(words) > 1 {
		sub := words[1]
		switch {
		case exe == "go" && oneOf(sub, "test", "vet", "build"),
			exe == "npm" && (sub == "test" || (sub == "run" && len(words) > 2 && words[2] == "test")),
			exe == "make" && sub == "test",
			exe == "cargo" && sub == "test",
			exe == "git" && oneOf(sub, "diff", "status", "show", "log"):
			if exe == "npm" && sub == "run" && len(words) > 2 && words[2] == "test" {
				return "npm run test", "npm run test"
			}
			return exe + " " + sub, exe + " " + sub
		}
	}
	if len(words) == 1 {
		return exe, exe
	}
	return exe, exe + " [arguments omitted]"
}

func isApplyPatch(tool, command string) bool {
	if strings.EqualFold(strings.TrimSpace(tool), "apply_patch") {
		return true
	}
	words := safeWords(command)
	return len(words) > 0 && baseExecutable(words[0]) == "apply_patch"
}

func isMutatingCommand(command string) bool {
	words := safeWords(command)
	if len(words) == 0 {
		return false
	}
	exe := baseExecutable(words[0])
	if oneOf(exe, "rm", "mv", "cp", "touch", "mkdir", "rmdir", "chmod", "chown") {
		return true
	}
	if exe == "git" && len(words) > 1 && oneOf(words[1], "add", "commit", "checkout", "reset", "restore", "clean", "rebase", "merge") {
		return true
	}
	return (exe == "sed" || exe == "perl") && strings.Contains(strings.Join(words[1:], " "), "-i")
}

func setResult(e *model.Event, m map[string]any, component string) {
	var code *int
	for _, k := range []string{"exit_code", "exitCode"} {
		if v, ok := m[k]; ok {
			code = integer(v)
			break
		}
	}
	response, responseOK := responseObject(m)
	if code == nil && responseOK {
		for _, k := range []string{"exit_code", "exitCode"} {
			if v, ok := response[k]; ok {
				code = integer(v)
				break
			}
		}
	}
	e.ExitCode = code
	if code != nil {
		success := *code == 0
		e.Success = &success
		e.Summary = fmt.Sprintf("exit code %d", *code)
		if !success {
			e.FailureKey = failureKey(component, responseText(response))
			if e.FailureKey == "" && *code == 127 {
				e.FailureKey = failureKey(component, "command not found")
			}
		}
	} else if responseOK && responseBool(response, "isError") {
		success := false
		e.Success = &success
		e.Summary = "tool response reported an error"
		e.FailureKey = failureKey(component, responseText(response))
	} else {
		e.Summary = "result status unknown"
		e.Gap = joinGap(e.Gap, "completion status unavailable")
	}
	if responseOK {
		if duration := integer(response["duration_ms"]); duration != nil && *duration >= 0 {
			e.DurationMS = int64(*duration)
		}
	}
}

func responseObject(m map[string]any) (map[string]any, bool) {
	for _, key := range []string{"tool_response", "toolResponse"} {
		if x, ok := m[key].(map[string]any); ok {
			return x, true
		}
	}
	return nil, false
}

func responseBool(m map[string]any, key string) bool { b, _ := m[key].(bool); return b }

func responseText(m map[string]any) string {
	for _, key := range []string{"error", "message", "detail", "reason", "stderr"} {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	// MCP error envelopes commonly carry diagnostics in content[].text.
	// This text is used only to select a bounded failure signature; it is
	// never copied into an event.
	if content, ok := m["content"].([]any); ok {
		for _, item := range content {
			if obj, ok := item.(map[string]any); ok {
				if s, ok := obj["text"].(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

func integer(v any) *int {
	var n int64
	switch x := v.(type) {
	case json.Number:
		var err error
		n, err = x.Int64()
		if err != nil {
			return nil
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || x != math.Trunc(x) || x < float64(math.MinInt) || x > float64(math.MaxInt) {
			return nil
		}
		n = int64(x)
	case int:
		return &x
	default:
		return nil
	}
	if int64(int(n)) != n {
		return nil
	}
	i := int(n)
	return &i
}

func hasResponseDuration(m map[string]any) bool {
	response, ok := responseObject(m)
	if !ok {
		return false
	}
	d := integer(response["duration_ms"])
	return d != nil && *d >= 0
}

func pair(e *model.Event, history []model.Event, responseDuration bool) {
	if e.OperationID == "" {
		return
	}
	for i := len(history) - 1; i >= 0; i-- {
		h := history[i]
		if h.Kind != "tool_started" || h.Project != e.Project || h.SessionID != e.SessionID || h.TurnID != e.TurnID || h.OperationID != e.OperationID {
			continue
		}
		if !responseDuration && e.DurationMS == 0 {
			d := e.Timestamp.Sub(h.Timestamp).Milliseconds()
			if d >= 0 {
				e.DurationMS = d
				e.Summary += "; elapsed proxy (hook timestamps)"
			}
		}
		return
	}
	e.Gap = joinGap(e.Gap, "start event unavailable")
}

func failureKey(component, output string) string {
	s := strings.ToLower(output)
	known := ""
	for _, x := range []string{"command not found", "no such file or directory", "permission denied", "module not found", "cannot find", "timed out", "timeout"} {
		if strings.Contains(s, x) {
			known = x
			break
		}
	}
	if known == "" {
		return ""
	}
	if component == "" {
		component = "tool"
	}
	return bounded(component) + ":" + known
}

func joinGap(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || strings.Contains(a, b) {
		return a
	}
	return a + "; " + b
}

func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}

func bounded(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 128 {
		return s[:128] + "…"
	}
	return s
}

func digest(s string) string {
	x := sha256.Sum256([]byte(s))
	return hex.EncodeToString(x[:])
}

func configPath(project string) string { return filepath.Join(project, ".codex", "hooks.json") }

func Connect(project, binary, stateDir string) (string, error) {
	return connect(project, binary, stateDir, false)
}

// ConnectShared must not redirect other worktrees to a different state store.
func ConnectShared(project, binary, stateDir string) (string, error) {
	return connect(project, binary, stateDir, true)
}

var ErrSharedConflict = errors.New("shared hooks use a different Sparestep executable or state folder")

func connect(project, binary, stateDir string, preserve bool) (string, error) {
	p := configPath(project)
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	root := map[string]any{}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &root); err != nil {
			return "", err
		}
	}
	hooksValue, exists := root["hooks"]
	if exists {
		if _, ok := hooksValue.(map[string]any); !ok {
			return "", errors.New("hooks config has malformed hooks object")
		}
	}
	hookMap, _ := hooksValue.(map[string]any)
	if hookMap == nil {
		hookMap = map[string]any{}
		root["hooks"] = hookMap
	}
	cmd := shellQuote(binary) + " hook --project " + shellQuote(project) + " --state-dir " + shellQuote(stateDir) + " # Sparestep recording"
	changed := false
	for _, ev := range requiredEvents {
		v, ok := hookMap[ev]
		if ok {
			if err := validateEventHooks(v); err != nil {
				return "", fmt.Errorf("hooks.%s: %w", ev, err)
			}
			if preserve {
				for _, group := range v.([]any) {
					for _, raw := range group.(map[string]any)["hooks"].([]any) {
						handler, _ := raw.(map[string]any)
						existing, _ := handler["command"].(string)
						if ownedCommand(existing, project) && existing != cmd {
							return "", ErrSharedConflict
						}
					}
				}
			}
			if containsOwned(v, cmd) {
				continue
			}
			// Updating the executable or state folder replaces our old handler.
			// Neighboring hooks keep their original configuration.
			cleaned, _ := removeOur(v, project)
			hookMap[ev] = append(cleaned.([]any), hookGroup(cmd))
			changed = true
			continue
		}
		hookMap[ev] = []any{hookGroup(cmd)}
		changed = true
	}
	if !changed {
		return p, nil
	}
	if err := writeAtomic(p, root, old); err != nil {
		return "", err
	}
	return p, nil
}

func hookGroup(cmd string) map[string]any {
	return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 2}}}
}

func validateEventHooks(v any) error {
	a, ok := v.([]any)
	if !ok {
		return errors.New("event must be an array")
	}
	for _, item := range a {
		group, ok := item.(map[string]any)
		if !ok {
			return errors.New("hook group must be an object")
		}
		if hs, exists := group["hooks"]; exists {
			if _, ok := hs.([]any); !ok {
				return errors.New("hook group hooks must be an array")
			}
		} else {
			return errors.New("hook group hooks are missing")
		}
	}
	return nil
}

func containsOwned(v any, cmd string) bool {
	a, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range a {
		group, _ := item.(map[string]any)
		hs, _ := group["hooks"].([]any)
		for _, h := range hs {
			if handler, ok := h.(map[string]any); ok && handler["type"] == "command" && handler["command"] == cmd && strings.HasSuffix(cmd, " # Sparestep recording") {
				return true
			}
		}
	}
	return false
}

func writeAtomic(p string, v any, old []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if len(old) > 0 {
		bak := p + ".bak"
		if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(bak, old, 0600); err != nil {
				return err
			}
		}
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	mode := os.FileMode(0644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".hooks.json.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		return err
	}
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	return nil
}

func Disconnect(project string) error {
	p := configPath(project)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return err
	}
	hooksValue, exists := root["hooks"]
	if !exists {
		return nil
	}
	hooks, ok := hooksValue.(map[string]any)
	if !ok {
		return errors.New("hooks config has malformed hooks object")
	}
	changed := false
	for _, ev := range requiredEvents {
		v, ok := hooks[ev]
		if !ok {
			continue
		}
		if err := validateEventHooks(v); err != nil {
			return fmt.Errorf("hooks.%s: %w", ev, err)
		}
		newValue, removed := removeOur(v, project)
		if removed {
			hooks[ev] = newValue
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeAtomic(p, root, b)
}

func removeOur(v any, project string) (any, bool) {
	a := v.([]any)
	out := make([]any, 0, len(a))
	removed := false
	for _, item := range a {
		group := item.(map[string]any)
		hs := group["hooks"].([]any)
		kept := make([]any, 0, len(hs))
		for _, h := range hs {
			handler, _ := h.(map[string]any)
			cmd, _ := handler["command"].(string)
			if handler["type"] == "command" && ownedCommand(cmd, project) {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) > 0 || len(hs) == 0 {
			group["hooks"] = kept
			out = append(out, group)
		}
	}
	return out, removed
}

func ownedCommand(cmd, project string) bool {
	return strings.HasSuffix(cmd, " # Sparestep recording") && strings.Contains(cmd, " hook --project "+shellQuote(project)+" --state-dir ")
}

func ConnectionStatus(project string) (bool, string) {
	p := configPath(project)
	b, err := os.ReadFile(p)
	if err != nil {
		return false, p
	}
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return false, p
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return false, p
	}
	for _, ev := range requiredEvents {
		v, ok := hooks[ev]
		if !ok || !hasOwnedForProject(v, project) {
			return false, p
		}
	}
	return true, p
}

func hasOwnedForProject(v any, project string) bool {
	a, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range a {
		group, _ := item.(map[string]any)
		hs, _ := group["hooks"].([]any)
		for _, h := range hs {
			handler, _ := h.(map[string]any)
			cmd, _ := handler["command"].(string)
			if handler["type"] == "command" && ownedCommand(cmd, project) {
				return true
			}
		}
	}
	return false
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
