package focus

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type operation struct {
	write, unknown bool
	paths          []string
}

func inspectOperation(tool string, input map[string]any) operation {
	switch strings.ToLower(tool) {
	case "apply_patch", "edit", "write":
		if strings.EqualFold(tool, "apply_patch") {
			text, _ := input["command"].(string)
			if text == "" {
				text, _ = input["patch"].(string)
			}
			paths := []string{}
			for _, line := range strings.Split(text, "\n") {
				for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
					if strings.HasPrefix(line, prefix) {
						paths = append(paths, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
					}
				}
			}
			return operation{write: true, unknown: len(paths) == 0, paths: paths}
		}
		for _, key := range []string{"file_path", "path", "file"} {
			if path, ok := input[key].(string); ok && path != "" {
				return operation{write: true, paths: []string{path}}
			}
		}
		return operation{write: true, unknown: true}
	case "bash", "exec_command", "shell_command":
		command, _ := input["command"].(string)
		if command == "" {
			command, _ = input["cmd"].(string)
		}
		return inspectShell(command)
	case "read", "read_file", "view_image", "update_plan", "spawn_agent", "agent", "wait_agent", "send_message", "wait":
		return operation{}
	default:
		return operation{unknown: true}
	}
}

// Parse shell syntax without evaluating it. Substitutions and compound
// programs remain opaque; quoted JSON heredocs are only literal input.
func inspectShell(command string) operation {
	unknown := operation{write: true, unknown: true}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return unknown
	}
	statement := file.Stmts[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return unknown
	}
	args := []string{}
	for _, arg := range call.Args {
		text, ok := literalWord(arg)
		if !ok {
			return unknown
		}
		args = append(args, text)
	}
	for _, redirect := range statement.Redirs {
		if redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc {
			return unknown
		}
		if redirect.Hdoc != nil {
			if _, ok := literalWord(redirect.Hdoc); !ok {
				return unknown
			}
		}
	}
	name := filepath.Base(args[0])
	if strings.HasPrefix(name, "sparestep") && len(args) >= 3 && args[1] == "focus" {
		switch args[2] {
		case "status":
			return operation{}
		case "check":
			// The wrapper records this check's real exit status. Treating it
			// as an edit would erase every other required check in a suite.
			return operation{}
		case "begin", "amend", "defer", "review", "finish", "pause", "resume", "cancel":
			return operation{write: true}
		}
	}
	if len(statement.Redirs) > 0 {
		return unknown
	}
	switch name {
	case "pwd", "cat", "rg", "grep", "ls", "head", "tail", "wc", "stat", "readlink":
		return operation{}
	case "git":
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "--output") || arg == "--ext-diff" || arg == "--textconv" {
				return unknown
			}
		}
		if len(args) > 1 {
			switch args[1] {
			case "status", "diff", "show", "log", "rev-parse", "ls-files", "ls-tree":
				return operation{}
			}
		}
	case "go":
		for _, arg := range args[1:] {
			if arg == "-w" || arg == "-u" {
				return unknown
			}
		}
		if len(args) > 1 {
			switch args[1] {
			case "test", "vet", "version", "env":
				return operation{}
			}
		}
	case "gofmt":
		write := false
		paths := []string{}
		for _, arg := range args[1:] {
			if arg == "-w" {
				write = true
			} else if !strings.HasPrefix(arg, "-") {
				paths = append(paths, arg)
			}
		}
		if !write {
			return operation{}
		}
		if len(paths) > 0 {
			return operation{write: true, paths: paths}
		}
	case "npm", "pnpm", "yarn":
		if len(args) > 2 && args[1] == "run" && (args[2] == "format" || args[2] == "format:write") {
			return operation{write: true, paths: []string{"."}}
		}
	}
	return unknown
}
func literalWord(word *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range word.Parts {
		switch value := part.(type) {
		case *syntax.Lit:
			b.WriteString(value.Value)
		case *syntax.SglQuoted:
			b.WriteString(value.Value)
		case *syntax.DblQuoted:
			text, ok := literalWord(&syntax.Word{Parts: value.Parts})
			if !ok {
				return "", false
			}
			b.WriteString(text)
		default:
			return "", false
		}
	}
	return b.String(), true
}
