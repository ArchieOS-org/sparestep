package hooks

import (
	"encoding/json"
	"github.com/ArchieOS-org/sparestep/internal/model"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func payload(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestMalformed(t *testing.T) {
	if _, e := Process([]byte("{"), "/p", nil); e == nil {
		t.Fatal("expected error")
	}
}
func TestTrustedExitAndSpoof(t *testing.T) {
	b, _ := Process(payload(t, map[string]any{"hook_event_name": "PostToolUse", "session_id": "s", "turn_id": "t", "tool_use_id": "o", "exit_code": 2, "stdout": "{\"exit_code\":0}"}), "/p", nil)
	if b[0].Success == nil || *b[0].Success {
		t.Fatal("untrusted stdout changed status")
	}
}
func TestPairScope(t *testing.T) {
	h := []model.Event{{Kind: "tool_started", OperationID: "o", SessionID: "s", TurnID: "t", Timestamp: now()}}
	b, _ := Process(payload(t, map[string]any{"hook_event_name": "PostToolUse", "session_id": "s", "turn_id": "t", "tool_use_id": "o", "exit_code": 0}), "/p", h)
	if b[0].DurationMS < 0 {
		t.Fatal()
	}
}
func TestRedaction(t *testing.T) {
	b, _ := Process(payload(t, map[string]any{"hook_event_name": "PreToolUse", "session_id": "s", "command": "curl https://u:p@example.com?a=token -H password=abc"}), "/p", nil)
	if contains(b[0].Command, "abc") || contains(b[0].Command, "u:p") {
		t.Fatal(b[0].Command)
	}
}
func TestConnectPreservesAndIdempotent(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, ".codex")
	os.MkdirAll(p, 0755)
	os.WriteFile(filepath.Join(p, "hooks.json"), []byte(`{"other":1,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"keep"}]}]}}`), 0644)
	x, e := Connect(d, "/bin/sparestep", "/state")
	if e != nil {
		t.Fatal(e)
	}
	_, e = Connect(d, "/bin/sparestep", "/state")
	if e != nil {
		t.Fatal(e)
	}
	ok, _ := ConnectionStatus(d)
	if !ok || x == "" {
		t.Fatal()
	}
}
func TestDisconnectPreserves(t *testing.T) {
	d := t.TempDir()
	Connect(d, "/bin/sparestep", "/state")
	if e := Disconnect(d); e != nil {
		t.Fatal(e)
	}
	ok, _ := ConnectionStatus(d)
	if ok {
		t.Fatal("still connected")
	}
}

func TestReconnectReplacesOnlyOurOldCommand(t *testing.T) {
	d := t.TempDir()
	if _, err := Connect(d, "/old/sparestep", "/old/state"); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(d, "/new/sparestep", "/new/state"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(d, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	for _, event := range requiredEvents {
		groups := root.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || contains(groups[0].Hooks[0].Command, "/old/") {
			t.Fatalf("reconnect left duplicate or stale command for %s: %s", event, b)
		}
	}
}
func TestForeignCWD(t *testing.T) {
	b, _ := Process(payload(t, map[string]any{"hook_event_name": "PreToolUse", "session_id": "s", "cwd": "/other", "command": "cat x"}), "/p", nil)
	if b[0].Gap == "" {
		t.Fatal("accepted foreign cwd")
	}
}
func TestEditKind(t *testing.T) {
	b, _ := Process(payload(t, map[string]any{"hook_event_name": "PostToolUse", "session_id": "s", "tool_name": "apply_patch", "exit_code": 0}), "/p", nil)
	if b[0].Kind != "edit" {
		t.Fatal(b[0].Kind)
	}
}

func TestCommandSummaryClassificationAndPrivacy(t *testing.T) {
	b, err := Process(payload(t, map[string]any{
		"hook_event_name": "PreToolUse", "session_id": "s", "tool_name": "Bash",
		"tool_input": map[string]any{"cmd": "go test ./... -run token"},
	}), "/p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Tool != "check" || b[0].Command != "go test" || b[0].StateKey == "" {
		t.Fatalf("unexpected bounded command: %#v", b[0])
	}
	if contains(b[0].Command, "token") {
		t.Fatal("command argument leaked")
	}
}

func TestCommandSummaryRejectsUntrustedFirstTokens(t *testing.T) {
	for _, command := range []string{"TOKEN=secret go test", "https://user:secret@example.test", "echo 'secret'"} {
		b, err := Process(payload(t, map[string]any{
			"hook_event_name": "PreToolUse", "session_id": "s", "cmd": command,
		}), "/p", nil)
		if err != nil {
			t.Fatal(err)
		}
		if b[0].Command != "[command omitted]" || contains(b[0].Command, "secret") {
			t.Fatalf("unsafe command summary %q: %#v", command, b[0])
		}
	}
}

func TestUnknownEventDoesNotEchoKind(t *testing.T) {
	b, err := Process(payload(t, map[string]any{
		"hook_event_name": "unknown-secret-token", "session_id": "s",
	}), "/p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if contains(b[0].Gap, "secret") || contains(b[0].Gap, "unknown-secret-token") {
		t.Fatalf("untrusted event kind leaked: %#v", b[0])
	}
}

func TestExit127HasBoundedSetupFailureKey(t *testing.T) {
	b, err := Process(payload(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "s", "tool_use_id": "o",
		"tool_name": "Bash", "cmd": "setup-tool --version", "exit_code": 127,
	}), "/p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].FailureKey != "setup-tool:command not found" {
		t.Fatalf("unexpected setup failure key: %#v", b[0])
	}
}

func TestMCPErrorAndDurationAreTyped(t *testing.T) {
	h := []model.Event{{Project: "", Kind: "tool_started", OperationID: "o", SessionID: "s", TurnID: "t", Timestamp: now()}}
	b, err := Process(payload(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "s", "turn_id": "t", "tool_use_id": "o",
		"tool_response": map[string]any{"duration_ms": 0, "isError": true, "message": "permission denied"},
	}), "/p", h)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].Success == nil || *b[0].Success || b[0].DurationMS != 0 || contains(b[0].Summary, "elapsed") {
		t.Fatalf("bad typed response: %#v", b[0])
	}
	if b[0].FailureKey != "tool:permission denied" {
		t.Fatalf("bad bounded failure key: %q", b[0].FailureKey)
	}
}

func TestMCPContentErrorIsClassifiedWithoutRetention(t *testing.T) {
	b, err := Process(payload(t, map[string]any{
		"hook_event_name": "PostToolUse", "session_id": "s", "tool_use_id": "o",
		"tool_name": "mcp__fs__read", "tool_response": map[string]any{
			"isError": true, "content": []any{map[string]any{"type": "text", "text": "no such file or directory: private-secret"}},
		},
	}), "/p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[0].FailureKey != "mcp__fs__read:no such file or directory" || contains(b[0].FailureKey, "private-secret") {
		t.Fatalf("bad bounded MCP failure key: %#v", b[0])
	}
}

func TestPairRequiresProjectSessionTurn(t *testing.T) {
	h := []model.Event{{Project: "/elsewhere", Kind: "tool_started", OperationID: "o", SessionID: "s", TurnID: "t", Timestamp: now()}}
	b, err := Process(payload(t, map[string]any{
		"hook_event_name": "PostToolUse", "cwd": "/p", "session_id": "s", "turn_id": "t", "tool_use_id": "o", "exit_code": 0,
	}), "/p", h)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(b[0].Gap, "start event unavailable") {
		t.Fatalf("paired across projects: %#v", b[0])
	}
}

func TestDisconnectKeepsNeighborAndBackup(t *testing.T) {
	d := t.TempDir()
	if _, err := Connect(d, "/bin/spare step", "/state with 'quote'"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, ".codex", "hooks.json")
	original, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(original, &root); err != nil {
		t.Fatal(err)
	}
	hooks := root["hooks"].(map[string]any)
	stop := hooks["Stop"].([]any)[0].(map[string]any)
	stop["hooks"] = append(stop["hooks"].([]any), map[string]any{"type": "command", "command": "neighbor"})
	encoded, _ := json.Marshal(root)
	if err := os.WriteFile(p, encoded, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Disconnect(d); err != nil {
		t.Fatal(err)
	}
	result, _ := os.ReadFile(p)
	if !contains(string(result), "neighbor") || contains(string(result), "Sparestep recording") {
		t.Fatal("disconnect did not preserve neighbor or remove own hook")
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Fatal("missing preserved backup")
	}
}
func contains(s, x string) bool {
	for i := 0; i+len(x) <= len(s); i++ {
		if s[i:i+len(x)] == x {
			return true
		}
	}
	return false
}
func now() time.Time { return time.Now().Add(-time.Second) }
