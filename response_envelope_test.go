package main

import (
	"encoding/json"
	"testing"
)

func TestHookResponseResult(t *testing.T) {
	valid := `{"chunk_id":"chunk-1","exit_code":0,"output":"ok","wall_time_seconds":0.25}`
	failed := `{"chunk_id":"chunk-2","exit_code":1,"output":"bad","wall_time_seconds":0.25}`
	serialized := `[{"type":"input_text","text":` + quoteJSON(valid) + `}]`
	mixed := `[{"type":"input_text","text":` + quoteJSON(valid) + `},{"type":"input_text","text":` + quoteJSON(failed) + `}]`
	wrapped := `{"output":[{"type":"input_text","text":` + quoteJSON(valid) + `}]}`
	tests := []struct {
		name, tool, raw  string
		known, succeeded bool
	}{
		{"direct native success", "functions.exec_command", valid, true, true},
		{"serialized native success", "functions.exec_command", serialized, true, true},
		{"wrapped native success", "functions.exec_command", wrapped, true, true},
		{"write stdin basename", "/native/write_stdin", serialized, true, true},
		{"namespaced write stdin", "functions.write_stdin", serialized, true, true},
		{"native failure", "bash", failed, true, false},
		{"failure dominates", "exec_command", mixed, true, false},
		{"arbitrary marker", "exec_command", `{"output":"__ONE_SHOT_TALLY_RESULT__:0"}`, false, false},
		{"arbitrary JSON exit", "exec_command", `[{"type":"input_text","text":"{\"exit_code\":0}"}]`, false, false},
		{"incomplete running session", "exec_command", `{"chunk_id":"chunk-3","output":"still running","wall_time_seconds":0.25}`, false, false},
		{"nested stdout ignored", "exec_command", `{"stdout":{"chunk_id":"x","exit_code":0,"output":"ok","wall_time_seconds":1}}`, false, false},
		{"nested content ignored", "exec_command", `{"content":[{"chunk_id":"x","exit_code":1,"output":"bad","wall_time_seconds":1}]}`, false, false},
		{"MCP lookalike ignored", "mcp__server__tool", serialized, false, false},
		{"transport success unknown", "exec_command", `{"success":true}`, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			known, succeeded := hookResponseResult(test.tool, json.RawMessage(test.raw))
			if known != test.known || succeeded != test.succeeded {
				t.Fatalf("hookResponseResult() = (%v, %v), want (%v, %v)", known, succeeded, test.known, test.succeeded)
			}
		})
	}
}

func TestNativeRunningSession(t *testing.T) {
	running := `{"chunk_id":"chunk-1","output":"partial","wall_time_seconds":1.5,"session_id":42}`
	wrapped := `{"output":[{"type":"input_text","text":` + quoteJSON(`{"chunk_id":"chunk-2","output":"partial","wall_time_seconds":2,"session_id":"0042"}`) + `}]}`
	for _, test := range []struct {
		name, raw, want string
	}{
		{"root", running, "42"},
		{"wrapped", wrapped, "42"},
		{"complete", `{"chunk_id":"chunk-3","output":"done","wall_time_seconds":1,"session_id":42,"exit_code":0}`, ""},
		{"nested stdout", `{"stdout":` + running + `}`, ""},
		{"invalid session type", `{"chunk_id":"chunk-4","output":"partial","wall_time_seconds":1,"session_id":true}`, ""},
		{"invalid wall time", `{"chunk_id":"chunk-5","output":"partial","wall_time_seconds":"1","session_id":42}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nativeRunningSession(json.RawMessage(test.raw)); got != test.want {
				t.Fatalf("nativeRunningSession() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInputExecutionSession(t *testing.T) {
	for _, test := range []struct {
		raw, want string
	}{
		{`{"session_id":7}`, "7"},
		{`{"session_id":"0007"}`, "7"},
		{`{"session_id":1.5}`, ""},
		{`{"nested":{"session_id":7}}`, ""},
		{`[{"session_id":7}]`, ""},
	} {
		if got := inputExecutionSession(json.RawMessage(test.raw)); got != test.want {
			t.Fatalf("inputExecutionSession(%s) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
