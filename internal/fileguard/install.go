package fileguard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Install adds independent guards without replacing tally or lifecycle hooks.
func Install(out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if override := os.Getenv("ONE_SHOT_INSTALL_HOME"); override != "" {
		home = override
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	bin := filepath.Join(home, ".local", "bin", "agent-file-guard")
	data, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	if err := atomicWrite(bin, data, 0755); err != nil {
		return err
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" || os.Getenv("ONE_SHOT_INSTALL_HOME") != "" {
		codexHome = filepath.Join(home, ".codex")
	}
	paths := []string{filepath.Join(home, ".codex", "hooks.json"), filepath.Join(codexHome, "hooks.json")}
	accounts, _ := filepath.Glob(filepath.Join(home, ".codex-accounts", "*", "hooks.json"))
	paths = append(paths, accounts...)
	seen := map[string]bool{}
	for _, path := range paths {
		if resolved, e := filepath.EvalSymlinks(path); e == nil {
			path = resolved
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := installHooks(path, bin, "codex"); err != nil {
			return err
		}
	}
	if err := installHooks(filepath.Join(home, ".claude", "settings.json"), bin, "claude"); err != nil {
		return err
	}
	if err := installHooks(filepath.Join(home, ".cursor", "hooks.json"), bin, "cursor"); err != nil {
		return err
	}
	fmt.Fprintln(out, "Installed independent deletion guards for Codex accounts, Claude, and Cursor.")
	return nil
}

func installHooks(path, bin, surface string) error {
	doc := map[string]any{}
	old, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(old, &doc); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		if doc["hooks"] != nil {
			return fmt.Errorf("invalid hooks object in %s", path)
		}
		hooks = map[string]any{}
		doc["hooks"] = hooks
	}
	events := []string{"PreToolUse"}
	if surface == "cursor" {
		doc["version"] = 1
		events = []string{"preToolUse", "beforeShellExecution", "beforeMCPExecution", "beforeReadFile", "beforeTabFileRead"}
	}
	for _, event := range events {
		entries, _ := hooks[event].([]any)
		// Preserve all foreign entries and their order (Codex trust IDs use indexes).
		command := shellQuote(bin) + " hook " + surface
		if surface != "cursor" {
			fallback := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"File guard unavailable; tool denied."}}`
			command += " || printf '%s\\n' " + shellQuote(fallback)
		}
		guard := map[string]any{"command": command, "timeout": 5, "type": "command"}
		var entry any = map[string]any{"matcher": "*", "hooks": []any{guard}}
		if surface == "cursor" {
			guard["failClosed"] = true
			entry = guard
		}
		replaced := false
		for i, raw := range entries {
			encoded, _ := json.Marshal(raw)
			if strings.Contains(string(encoded), "agent-file-guard' hook") || strings.Contains(string(encoded), "agent-file-guard hook") {
				entries[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			entries = append(entries, entry)
		}
		hooks[event] = entries
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(old) > 0 {
		if _, e := os.Stat(path + ".before-agent-file-guard"); os.IsNotExist(e) {
			if err := atomicWrite(path+".before-agent-file-guard", old, 0600); err != nil {
				return err
			}
		}
	}
	return atomicWrite(path, data, 0600)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".guard-install-")
	if err != nil {
		return err
	}
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
