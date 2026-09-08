package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/codexsetup"
	"github.com/ArchieOS-org/sparestep/internal/focus"
	"github.com/ArchieOS-org/sparestep/internal/hooks"
	"github.com/ArchieOS-org/sparestep/internal/web"
)

func readJSON(in io.Reader, value any) error {
	data, err := io.ReadAll(io.LimitReader(in, 65537))
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return errors.New("input JSON exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("read input JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("provide exactly one JSON object")
	}
	return nil
}

func runFocus(args []string, state string, in io.Reader, out, errOut io.Writer) error {
	action := "status"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("sparestep focus "+action, flag.ContinueOnError)
	fs.SetOutput(errOut)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	project := fs.String("project", cwd, "Active project/worktree")
	dir := fs.String("state-dir", state, "Private state folder")
	id := fs.String("id", "", "Focus task id")
	session := fs.String("session-id", os.Getenv("CODEX_SESSION_ID"), "Native Codex session id")
	thread := fs.String("thread-id", os.Getenv("CODEX_THREAD_ID"), "Native Codex thread id")
	criterion := fs.String("criterion", "", "Completion criterion id")
	result := fs.String("result", "completed", "completed, stopped, or cancelled")
	_ = fs.Bool("json", false, "Print JSON")
	if err = fs.Parse(args); err != nil {
		return err
	}
	root, err := projectRoot(*project)
	if err != nil {
		return err
	}
	*project = root
	*dir, err = filepath.Abs(*dir)
	if err != nil {
		return err
	}
	dbPath := filepath.Join(*dir, "focus.db")
	var beginInput focus.BeginInput
	if action == "begin" {
		if *session == "" || *thread == "" {
			return errors.New("start focus from Codex so it can be bound to this task")
		}
		if err = readJSON(in, &beginInput); err != nil {
			return err
		}
		if beginInput.Mode == "planning" {
			return errors.New("in Plan mode, keep the brief and deferred ideas in the conversation; begin focus after switching to implementation")
		}
		if strings.TrimSpace(beginInput.Goal) == "" || len(beginInput.Criteria) == 0 || len(beginInput.Paths) == 0 {
			return errors.New("provide a task goal, at least one completion condition, and the components in scope")
		}
		for _, c := range beginInput.Criteria {
			if strings.TrimSpace(c.Description) == "" {
				return errors.New("each completion condition needs a description")
			}
		}
		if connected, _ := hooks.ConnectionStatus(*project); !connected {
			binary, e := os.Executable()
			if e != nil {
				return e
			}
			if _, e = hooks.Connect(*project, binary, *dir); e != nil {
				return e
			}
			setup, e := codexsetup.Setup(*project, binary, *dir)
			if e != nil {
				return e
			}
			return json.NewEncoder(out).Encode(map[string]any{"focus": nil, "status": setup.Status, "message": setup.Message, "next_action": setup.NextAction, "setup_only": true})
		}
	}
	if action == "status" {
		if _, e := os.Stat(dbPath); os.IsNotExist(e) {
			return json.NewEncoder(out).Encode(map[string]any{"focus": nil, "deferred": []any{}, "message": "No focused task. Use /sparestep followed by what you want done."})
		}
	}
	s, err := focus.Open(dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if action == "begin" {
		input := beginInput
		input.Project, input.SessionID, input.ThreadID, input.OwnerRootThread = *project, *session, *thread, *thread
		input.Mode = "implementing"
		task, err := s.Begin(input)
		if err != nil {
			return err
		}
		kickPublisher(*dir)
		return json.NewEncoder(out).Encode(map[string]any{"focus": task, "message": "Task brief saved. Supported native guards take effect when this task's trusted hooks run."})
	}
	var task *focus.Task
	if *id != "" {
		task, err = s.Get(*id)
	} else if *session != "" {
		if action == "status" {
			task, err = s.LatestForSession(*project, *session)
		} else {
			task, err = s.Current(*project, *session)
		}
	} else if action == "status" {
		task, err = s.Latest(*project)
	} else {
		return errors.New("run this action from the owning Codex task, or provide its session and thread ids")
	}
	if errors.Is(err, focus.ErrNotFound) && action == "status" {
		return json.NewEncoder(out).Encode(map[string]any{"focus": nil, "deferred": []any{}, "message": "No focused task."})
	}
	if err != nil {
		return err
	}
	if task.Project != *project {
		return errors.New("focus belongs to another worktree")
	}
	if action == "status" {
		return writeFocusStatus(out, *dir, task)
	}
	if *session == "" || *thread == "" || task.SessionID != *session {
		return errors.New("focus belongs to another Codex session")
	}
	if action != "defer" && action != "check" && action != "review" && *thread != task.OwnerRootThread {
		return errors.New("only the owning Codex task can change or finish its focus brief")
	}
	if task.Mode == "planning" && action != "pause" && action != "cancel" {
		return errors.New("Plan mode is active; resume implementation before changing focus or publishing issues")
	}
	switch action {
	case "amend":
		var amendment focus.Amendment
		if err = readJSON(in, &amendment); err != nil {
			return err
		}
		known := false
		for _, c := range task.Criteria {
			if c.ID == amendment.CriterionID {
				known = true
			}
		}
		if !known {
			return errors.New("a necessary expansion must name an existing completion condition")
		}
		updated, err := s.Amend(task.ID, amendment)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"focus": updated, "message": "Scope expanded — " + amendment.Reason, "announce": true})
	case "check":
		if *criterion == "" || fs.NArg() == 0 {
			return errors.New("usage: sparestep focus check --criterion ID -- command [arguments]")
		}
		known := false
		for _, c := range task.Criteria {
			if c.ID == *criterion && c.Kind == "check" {
				known = true
			}
		}
		if !known {
			return errors.New("choose an existing command check criterion")
		}
		command := exec.Command(fs.Arg(0), fs.Args()[1:]...)
		command.Dir, command.Stdin, command.Stdout, command.Stderr = *project, in, out, errOut
		started := time.Now()
		runErr := command.Run()
		status, code := "pass", 0
		if runErr != nil {
			status, code = "fail", -1
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				code = exitErr.ExitCode()
			}
		}
		evidence := fmt.Sprintf("%s exited %d (%s)", filepath.Base(fs.Arg(0)), code, time.Since(started).Round(time.Millisecond))
		if err = s.RecordCheck(task.ID, focus.Check{CriterionID: *criterion, Kind: "check", Status: status, Evidence: evidence, Source: "sparestep-exec"}); err != nil {
			return err
		}
		if runErr != nil {
			return fmt.Errorf("required check failed: %w", runErr)
		}
		return nil
	case "review":
		var review struct {
			CriterionID string `json:"criterion_id"`
			Evidence    string `json:"evidence"`
		}
		if err = readJSON(in, &review); err != nil {
			return err
		}
		known := false
		for _, c := range task.Criteria {
			if c.ID == review.CriterionID && c.Kind == "review" {
				known = true
			}
		}
		if !known || strings.TrimSpace(review.Evidence) == "" {
			return errors.New("review requires an existing review criterion and a concrete evidence reference")
		}
		if err = s.RecordCheck(task.ID, focus.Check{CriterionID: review.CriterionID, Kind: "review", Status: "pass", Evidence: review.Evidence, Source: "agent-review", CoverageGap: "Review evidence is an agent assessment, not a machine-verified result."}); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"message": "Review evidence recorded as an agent assessment."})
	case "defer":
		return enqueueDeferred(in, out, *dir, task)
	case "finish", "cancel":
		if action == "cancel" {
			*result = "cancelled"
		}
		if *result == "completed" {
			if err = s.RequestCompletion(task.ID); err != nil {
				return err
			}
		}
		updated, err := s.Finish(task.ID, *result)
		if err != nil {
			return err
		}
		kickPublisher(*dir)
		return json.NewEncoder(out).Encode(map[string]any{"focus": updated, "message": "Task " + updated.Status + "."})
	case "pause", "resume":
		var updated focus.Task
		if action == "pause" {
			updated, err = s.Pause(task.ID)
		} else {
			updated, err = s.Resume(task.ID)
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"focus": updated})
	default:
		return fmt.Errorf("unknown focus action %q", action)
	}
}

func runHook(in io.Reader, out io.Writer, dir, project string) error {
	data, err := io.ReadAll(io.LimitReader(in, 2*1024*1024+1))
	decision := map[string]any{}
	if err != nil || len(data) > 2*1024*1024 {
		diagnostic(dir, errors.New("hook input unavailable or too large"))
		return json.NewEncoder(out).Encode(decision)
	}
	if _, err := os.Stat(filepath.Join(dir, "focus.db")); err == nil {
		s, e := focus.Open(filepath.Join(dir, "focus.db"))
		if e == nil {
			decision, e = s.Evaluate(data, project)
			_ = s.Close()
		}
		if e != nil {
			diagnostic(dir, e)
			decision = map[string]any{"systemMessage": "Sparestep focus checks are unavailable. This action was not checked."}
		}
	}
	// Observation errors never erase an enforcement decision already made.
	if err := recordHook(bytes.NewReader(data), dir, project); err != nil {
		diagnostic(dir, err)
	}
	if decision == nil {
		decision = map[string]any{}
	}
	return json.NewEncoder(out).Encode(decision)
}

func focusProvider(dir, project string) web.FocusProvider {
	return web.FocusProvider{
		Version: Version,
		Snapshot: func() (any, any, error) {
			if _, err := os.Stat(filepath.Join(dir, "focus.db")); os.IsNotExist(err) {
				return nil, []any{}, nil
			}
			s, err := focus.Open(filepath.Join(dir, "focus.db"))
			if err != nil {
				return nil, nil, err
			}
			defer s.Close()
			task, err := s.Latest(project)
			if errors.Is(err, focus.ErrNotFound) {
				return nil, []any{}, nil
			}
			if err != nil {
				return nil, nil, err
			}
			items, err := deferredForTask(dir, task.ID)
			return task, items, err
		},
		Pause: func(paused bool) error {
			if _, err := os.Stat(filepath.Join(dir, "focus.db")); os.IsNotExist(err) {
				return nil
			}
			s, err := focus.Open(filepath.Join(dir, "focus.db"))
			if err != nil {
				return err
			}
			defer s.Close()
			return s.SetProjectPaused(project, paused)
		},
	}
}

func printFocusSummary(dir, project string, asJSON bool, out io.Writer) (bool, error) {
	value, deferred, err := focusProvider(dir, project).Snapshot()
	if err != nil {
		return false, err
	}
	task, ok := value.(*focus.Task)
	if !ok || task == nil {
		return false, nil
	}
	if asJSON {
		return true, json.NewEncoder(out).Encode(map[string]any{"focus": task, "deferred": deferred})
	}
	fmt.Fprintln(out, task.Goal)
	state := task.Status
	if state == "active" && !task.HookObserved {
		state = "waiting for native guards"
	}
	fmt.Fprintf(out, "Status: %s\n", state)
	for _, criterion := range task.Criteria {
		fmt.Fprintf(out, "  %s: %s\n", criterion.Status, criterion.Description)
	}
	if len(task.Amendments) > 0 {
		fmt.Fprintln(out, "Scope expanded —", task.Amendments[len(task.Amendments)-1].Reason)
	}
	items, err := deferredForTask(dir, task.ID)
	if err != nil {
		return true, err
	}
	for _, item := range items {
		if item.IssueURL != "" {
			fmt.Fprintf(out, "Saved for later: %s — %s\n", item.Title, item.IssueURL)
		} else {
			fmt.Fprintf(out, "Saved for later (%s): %s\n", item.Status, item.Title)
		}
	}
	return true, nil
}
