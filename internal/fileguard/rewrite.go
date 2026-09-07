package fileguard

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// literalWord deliberately excludes substitutions and expansions.
func literalWord(word *syntax.Word) string {
	var text strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			text.WriteString(p.Value)
		case *syntax.SglQuoted:
			text.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, child := range p.Parts {
				lit, ok := child.(*syntax.Lit)
				if !ok {
					return ""
				}
				text.WriteString(lit.Value)
			}
		default:
			return ""
		}
	}
	return text.String()
}

// RewriteRemovals edits only command words. Shell quoting, glob expansion,
// control flow, redirects, and argument spelling remain the shell's job.
func RewriteRemovals(command, helper string) (string, bool, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return command, false, err
	}
	type edit struct {
		start, end  int
		replacement string
	}
	var edits []edit
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		index := 0
		if literalWord(call.Args[0]) == "command" && len(call.Args) > 1 {
			index = 1
			if literalWord(call.Args[index]) == "--" && len(call.Args) > 2 {
				index++
			}
		}
		word := call.Args[index]
		name := literalWord(word)
		if name != "" && filepath.Base(name) == "rm" {
			edits = append(edits, edit{int(word.Pos().Offset()), int(word.End().Offset()), shellQuote(helper) + " recycle"})
		}
		return true
	})
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, change := range edits {
		command = command[:change.start] + change.replacement + command[change.end:]
	}
	return command, len(edits) != 0, nil
}

// Check actual shell calls, not words inside search patterns or documentation.
func destructiveCommand(command string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return destructive.MatchString(command)
	}
	unsafe := false
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name := filepath.Base(literalWord(call.Args[0]))
		text := command[int(call.Pos().Offset()):int(call.End().Offset())]
		switch name {
		case "rm", "rmdir", "unlink", "srm", "mkfs", "newfs":
			unsafe = true
		case "find", "gfind", "rsync", "git", "sudo", "doas", "env", "command", "xargs", "eval", "bash", "sh", "zsh", "python", "python3", "node", "osascript", "diskutil":
			unsafe = unsafe || destructive.MatchString(text)
		}
		return true
	})
	return unsafe
}

func rewrittenInput(input any, helper string) (any, bool, error) {
	fields, ok := input.(map[string]any)
	if !ok {
		return input, false, nil
	}
	copy := make(map[string]any, len(fields))
	for key, value := range fields {
		copy[key] = value
	}
	changed := false
	for _, key := range []string{"cmd", "command"} {
		command, ok := fields[key].(string)
		if !ok {
			continue
		}
		updated, didChange, err := RewriteRemovals(command, helper)
		if err != nil {
			if destructive.MatchString(command) {
				return input, false, fmt.Errorf("cannot safely route this shell syntax to Trash: %w", err)
			}
			continue
		}
		copy[key] = updated
		changed = changed || didChange
	}
	return copy, changed, nil
}
