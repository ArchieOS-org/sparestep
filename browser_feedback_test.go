package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const chromeRegistrationAbort = "Chrome exited with signal SIGABRT\n_RegisterApplication failed\nTransformProcessType failed"
const playwrightChromeStartupFailure = "Traceback (most recent call last):\n  File \"playwright/_impl/_connection.py\", line 1\nplaywright._impl._errors.TargetClosedError: BrowserType.launch: Target page, context or browser has been closed\nBrowser logs:\n<launching> /Applications/Google Chrome.app/Contents/MacOS/Google Chrome --headless\n<launched> pid=29746"

func TestPlaywrightBrowserCommandRequiresActualLaunch(t *testing.T) {
	for _, test := range []struct {
		command string
		want    bool
	}{
		{"node run-playwright.js", true},
		{"cat > /tmp/boost-work-order-check.py <<'PY'\nfrom playwright.sync_api import sync_playwright\nPY\n/Library/Developer/CommandLineTools/usr/bin/python3 /tmp/boost-work-order-check.py", true},
		{"env CI=1 pnpm exec playwright test", true},
		{"echo 'node run-playwright.js'", false},
		{"printf '%s' playwright", false},
		{"cat <<'PY'\npython3 /tmp/example.py\nPY", false},
		{"google-chrome --headless", false},
		{"node -e \"require('playwright').chromium.launch()\"; true", true},
	} {
		t.Run(test.command, func(t *testing.T) {
			if got := playwrightBrowserCommand(test.command); got != test.want {
				t.Fatalf("playwrightBrowserCommand(%q) = %v, want %v", test.command, got, test.want)
			}
		})
	}
}

func TestChromeStartupCrashFeedbackRequiresNativeOutputAndFullSignature(t *testing.T) {
	native := `{"chunk_id":"browser","exit_code":1,"output":` + quoteJSON(chromeRegistrationAbort) + `,"wall_time_seconds":1}`
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{"native registration abort", native, true},
		{"native observed Playwright startup failure", `{"chunk_id":"browser","exit_code":1,"output":` + quoteJSON(playwrightChromeStartupFailure) + `,"wall_time_seconds":1}`, true},
		{"raw local-hook stdout", quoteJSON(playwrightChromeStartupFailure), true},
		{"serialized native crash", `[{"type":"input_text","text":` + quoteJSON(native) + `}]`, true},
		{"missing registration marker", `{"chunk_id":"browser","exit_code":1,"output":"SIGABRT TransformProcessType","wall_time_seconds":1}`, false},
		{"nested stdout is not evidence", `{"stdout":` + native + `}`, false},
		{"generic page failure", `{"chunk_id":"browser","exit_code":1,"output":"page.goto: net::ERR_CONNECTION_REFUSED","wall_time_seconds":1}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := chromeStartupCrashFeedback(json.RawMessage(test.raw))
			if (got != "") != test.want {
				t.Fatalf("chromeStartupCrashFeedback() = %q, want feedback=%v", got, test.want)
			}
			if got != "" && (!strings.Contains(got, "Do not rerun") || strings.Contains(strings.ToLower(got), "bypass")) {
				t.Fatalf("unsafe feedback: %q", got)
			}
		})
	}
}

func TestPostToolUseSteersConfirmedChromeStartupCrashWithoutBlocking(t *testing.T) {
	dir := retainedTestDir(t)
	command := "cat > /tmp/boost-work-order-check.py <<'PY'\nfrom playwright.sync_api import sync_playwright\nPY\n/Library/Developer/CommandLineTools/usr/bin/python3 /tmp/boost-work-order-check.py"
	hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "crash", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "launch", "tool_input": map[string]any{"command": command}})
	out := hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "crash", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": "launch", "tool_response": map[string]any{"chunk_id": "browser", "exit_code": 1, "output": playwrightChromeStartupFailure, "wall_time_seconds": 1}})
	specific, _ := out["hookSpecificOutput"].(map[string]any)
	context, _ := specific["additionalContext"].(string)
	if len(out) != 1 || specific["hookEventName"] != "PostToolUse" || !strings.Contains(context, "Native browser startup failed") {
		t.Fatalf("crash feedback = %#v", out)
	}

	hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "success", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "completed", "tool_input": map[string]any{"command": command}})
	success := hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "success", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": "completed", "tool_response": map[string]any{"chunk_id": "completed", "exit_code": 0, "output": playwrightChromeStartupFailure, "wall_time_seconds": 1}})
	if len(success) != 0 {
		t.Fatalf("successful launch caused feedback: %#v", success)
	}

	hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "raw", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "raw-launch", "tool_input": map[string]any{"command": command}})
	raw := hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "raw", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": "raw-launch", "tool_response": playwrightChromeStartupFailure})
	rawSpecific, _ := raw["hookSpecificOutput"].(map[string]any)
	if rawSpecific["hookEventName"] != "PostToolUse" {
		t.Fatalf("raw local stdout did not steer: %#v", raw)
	}

	hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "page", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "page-error", "tool_input": map[string]any{"command": command}})
	pageFailure := hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "page", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": "page-error", "tool_response": "page.goto: net::ERR_CONNECTION_REFUSED"})
	if len(pageFailure) != 0 {
		t.Fatalf("generic page failure caused feedback: %#v", pageFailure)
	}

	hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "quote", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "example", "tool_input": map[string]any{"command": "echo 'node -e require(playwright)'"}})
	quoted := hook(t, dir, map[string]any{"session_id": "browser", "turn_id": "quote", "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": "example", "tool_response": map[string]any{"chunk_id": "example", "exit_code": 1, "output": playwrightChromeStartupFailure, "wall_time_seconds": 1}})
	if len(quoted) != 0 {
		t.Fatalf("quoted example caused feedback: %#v", quoted)
	}
}
