package codexsetup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnresponsiveWrapperIsBounded(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := setupWith(ctx, fake, dir, "/bin/sparestep", dir)
	if err != nil || result.Status != "unsupported" || time.Since(started) > 2*time.Second {
		t.Fatalf("setup did not terminate its wrapper and child: %v %+v", err, result)
	}
}

func boolPtr(value bool) *bool { return &value }
func intPtr(value int) *int    { return &value }

func testHook(event, project, command string) hookInfo {
	return hookInfo{
		Key:         filepath.Join(project, ".codex/hooks.json") + ":" + event,
		EventName:   event,
		HandlerType: "command",
		Command:     command,
		Async:       boolPtr(false),
		Matcher:     json.RawMessage("null"),
		TimeoutSec:  intPtr(2),
		SourcePath:  filepath.Join(project, ".codex/hooks.json"),
		Source:      "project",
		Enabled:     true,
		IsManaged:   boolPtr(false),
		CurrentHash: "sha256:" + event,
		TrustStatus: "untrusted",
	}
}

func TestSelectHooksTrustsOnlyExactSparestepDefinitions(t *testing.T) {
	project := "/tmp/project"
	command := hookCommand("/opt/spare step", project, "/tmp/private state")
	list := hookList{}
	for event := range expectedEvents {
		list.Hooks = append(list.Hooks, testHook(event, project, command))
	}
	// A neighbor with a different command must never enter the trust map.
	neighbor := testHook("stop", project, command+" --neighbor")
	neighbor.Key = "neighbor"
	list.Hooks = append(list.Hooks, neighbor)

	selected, err := selectHooks(list, project, command)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != len(expectedEvents) {
		t.Fatalf("selected %d handlers, want %d", len(selected), len(expectedEvents))
	}
	for event, item := range selected {
		if item.Key == "neighbor" || item.EventName != event {
			t.Fatalf("selected unexpected handler: %#v", item)
		}
	}
}

func TestSelectHooksRejectsAlteredOrDisabledDefinitions(t *testing.T) {
	project := "/tmp/project"
	command := hookCommand("/opt/sparestep", project, "/tmp/state")
	list := hookList{}
	for event := range expectedEvents {
		item := testHook(event, project, command)
		if event == "stop" {
			item.TimeoutSec = intPtr(4)
		}
		if event == "postCompact" {
			item.Enabled = false
		}
		list.Hooks = append(list.Hooks, item)
	}
	if _, err := selectHooks(list, project, command); err == nil {
		t.Fatal("accepted altered or disabled hook definitions")
	}
}

func TestSelectTrustedHooksRequiresEnabledTrust(t *testing.T) {
	project := "/tmp/project"
	command := hookCommand("/opt/sparestep", project, "/tmp/state")
	list := hookList{}
	for event := range expectedEvents {
		item := testHook(event, project, command)
		item.TrustStatus = "trusted"
		list.Hooks = append(list.Hooks, item)
	}
	list.Hooks[0].TrustStatus = "modified"
	if _, err := selectTrustedHooks(list, project, command); err == nil {
		t.Fatal("accepted modified hook")
	}
}
