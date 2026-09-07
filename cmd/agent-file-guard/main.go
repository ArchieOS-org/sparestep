package main

import (
	"fmt"
	"one-shot-tally/internal/fileguard"
	"one-shot-tally/internal/recovery"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-file-guard:", err)
		os.Exit(2)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent-file-guard hook [codex|claude|cursor|cursor-shell] | remove -- PATH | restore -- RECEIPT | install")
	}
	switch args[0] {
	case "recycle":
		return fileguard.Recycle(args[1:], os.Stdout, os.Stderr)
	case "hook":
		surface := "codex"
		if len(args) > 1 {
			surface = args[1]
		}
		return fileguard.Hook(os.Stdin, os.Stdout, surface)
	case "install":
		return fileguard.Install(os.Stdout)
	case "remove", "restore":
		if len(args) != 3 || args[1] != "--" {
			return fmt.Errorf("use %s -- followed by one explicit path", args[0])
		}
		if args[0] == "restore" {
			return recovery.Restore(args[2])
		}
		receipt, err := recovery.Remove(args[2])
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Retained for recovery:", receipt)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
