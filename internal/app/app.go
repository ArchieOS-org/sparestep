package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/hooks"
	"github.com/ArchieOS-org/sparestep/internal/model"
	"github.com/ArchieOS-org/sparestep/internal/store"
	"github.com/ArchieOS-org/sparestep/internal/web"
	"github.com/ArchieOS-org/sparestep/skills"
	"github.com/mattn/go-isatty"
)

var Version = "0.3.2"

func defaultState() (string, error) {
	if p := os.Getenv("SPARESTEP_STATE_DIR"); p != "" {
		return filepath.Abs(p)
	}
	if p := os.Getenv("XDG_STATE_HOME"); p != "" {
		return filepath.Abs(filepath.Join(p, "sparestep"))
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "state", "sparestep"), nil
}

func Run(args []string, in io.Reader, out, errOut io.Writer) error {
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		help(out)
		return nil
	}
	if cmd == "version" || cmd == "--version" {
		fmt.Fprintln(out, "Sparestep", Version)
		return nil
	}
	stateDir, e := defaultState()
	if e != nil {
		return e
	}
	if cmd == "focus" {
		return runFocus(args, stateDir, in, out, errOut)
	}
	if cmd == "linear" {
		return runLinear(args, stateDir, in, out, errOut)
	}
	cwd, e := os.Getwd()
	if e != nil {
		return e
	}
	fs := flag.NewFlagSet("sparestep "+cmd, flag.ContinueOnError)
	fs.SetOutput(errOut)
	project := fs.String("project", cwd, "Project folder on this computer")
	state := fs.String("state-dir", stateDir, "Private local state folder")
	jsonOutput := fs.Bool("json", false, "Print machine-readable output")
	port := fs.Int("port", 7357, "Local browser port (0 chooses a free port)")
	demo := fs.Bool("demo", false, "Use isolated example data")
	days := fs.Int("days", 30, "Days of history to keep")
	skillDir := fs.String("skill-dir", "", "Install the Codex skill in this folder")
	readyFile := fs.String("ready-file", "", "Report process readiness file")
	if e = fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if cmd == "install-skill" {
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		if *skillDir == "" {
			base := os.Getenv("CODEX_HOME")
			if base == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				base = filepath.Join(home, ".agents")
			}
			*skillDir = filepath.Join(base, "skills", "sparestep")
		}
		path, err := skills.Install(*skillDir, binary)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Sparestep is ready in Codex. Type / and choose Sparestep, or use $sparestep.")
		fmt.Fprintln(out, "Skill:", path)
		return nil
	}
	*project, e = filepath.Abs(*project)
	if e != nil {
		return e
	}
	if p, err := filepath.EvalSymlinks(*project); err == nil {
		*project = p
	}
	if cmd != "hook" {
		if (cmd == "start" || cmd == "open") && *demo {
			return errors.New("use sparestep serve --demo for example data")
		}
		if root, err := projectRoot(*project); err == nil {
			*project = root
		} else if cmd == "start" || cmd == "open" {
			return err
		}
	}
	*state, e = filepath.Abs(*state)
	if e != nil {
		return e
	}
	if cmd == "hook" {
		return runHook(in, out, *state, *project)
	}
	switch cmd {
	case "", "status", "review", "feedback", "draft", "link", "pause", "resume", "connect", "disconnect", "doctor", "serve", "demo", "prune", "start", "open":
	default:
		return fmt.Errorf("unknown command %q; run sparestep help", cmd)
	}
	if *demo || cmd == "demo" {
		tmp, err := os.MkdirTemp("", "sparestep-demo-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		*state = tmp
		*project = "/example/login-app"
	}
	if e = os.MkdirAll(*state, 0700); e != nil {
		return fmt.Errorf("cannot open local recording folder: %w", e)
	}
	s, e := store.Open(filepath.Join(*state, "sparestep.db"))
	if e != nil {
		return e
	}
	defer s.Close()
	if *demo || cmd == "demo" {
		if e = seedDemo(s, *project); e != nil {
			return e
		}
	}
	switch cmd {
	case "start", "open":
		return start(s, *project, *state, *port, cmd == "start", *jsonOutput, out)
	case "", "status", "demo":
		if cmd == "status" {
			shown, err := printFocusSummary(*state, *project, *jsonOutput, out)
			if err != nil || shown {
				return err
			}
		}
		if cmd != "status" && terminal(in) {
			return menu(s, *project, *state, in, out, errOut)
		}
		return printReport(s, *project, *jsonOutput, out)
	case "serve":
		return serveReady(s, *project, *state, *port, *readyFile, out)
	case "connect":
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		path, err := hooks.Connect(*project, binary, *state)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Codex connection installed for", *project)
		fmt.Fprintln(out, "Configuration:", path)
		fmt.Fprintln(out, "In Codex, open /hooks and review/trust the Sparestep entries for this project.")
		fmt.Fprintln(out, "Start a new Codex task in this project, then run sparestep doctor.")
		fmt.Fprintln(out, "Waiting for the first real event; recording has not been verified yet.")
		return nil
	case "disconnect":
		if e = hooks.Disconnect(*project); e != nil {
			return e
		}
		if e = focusProvider(*state, *project).Pause(true); e != nil {
			return e
		}
		if *jsonOutput {
			return json.NewEncoder(out).Encode(startResult{Project: *project, Status: "disconnected", Message: "Recording is disconnected. Your saved reports remain available."})
		}
		fmt.Fprintln(out, "Sparestep connection removed. Saved reports remain available.")
		return nil
	case "doctor":
		if *jsonOutput {
			result, err := connectionResult(s, *project)
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(result)
		}
		return doctor(s, *project, *state, out)
	case "pause", "resume":
		if e = focusProvider(*state, *project).Pause(cmd == "pause"); e != nil {
			return e
		}
		if e = s.SetPaused(*project, cmd == "pause"); e != nil {
			return e
		}
		if *jsonOutput {
			result, err := connectionResult(s, *project)
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(result)
		}
		if cmd == "pause" {
			fmt.Fprintln(out, "Recording paused. It will stay paused until you resume.")
		} else {
			fmt.Fprintln(out, "Recording resumed.")
		}
		return nil
	case "review":
		if fs.NArg() != 1 {
			return errors.New("usage: sparestep review [--project FOLDER] FINDING_ID")
		}
		return review(s, *project, fs.Arg(0), out)
	case "feedback":
		if fs.NArg() != 2 {
			return errors.New("usage: sparestep feedback [--project FOLDER] FINDING_ID necessary|dismissed|open")
		}
		if e = s.SetDisposition(*project, fs.Arg(0), fs.Arg(1)); e != nil {
			return e
		}
		fmt.Fprintln(out, "Feedback saved. Use 'open' to undo.")
		return nil
	case "draft":
		if fs.NArg() != 1 {
			return errors.New("usage: sparestep draft [--project FOLDER] FINDING_ID")
		}
		d, err := s.Draft(*project, fs.Arg(0))
		if err != nil {
			return err
		}
		if *jsonOutput {
			return json.NewEncoder(out).Encode(d)
		}
		fmt.Fprintf(out, "# %s\n\n%s\n", d.Title, d.Body)
		return nil
	case "link":
		if fs.NArg() != 2 {
			return errors.New("usage: sparestep link [--project FOLDER] FINDING_ID https://linear.app/...")
		}
		if e = s.SetIssueURL(*project, fs.Arg(0), fs.Arg(1)); e != nil {
			return e
		}
		fmt.Fprintln(out, "Issue link saved.")
		return nil
	case "prune":
		if *days < 1 {
			return errors.New("days must be at least 1")
		}
		if e = s.PurgeBefore(time.Now().AddDate(0, 0, -*days)); e != nil {
			return e
		}
		fmt.Fprintf(out, "Removed observations older than %d days.\n", *days)
		return nil
	}
	return nil
}

func terminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

func diagnostic(dir string, err error) {
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	// Only operational errors, never the hook input or tool output.
	_ = os.WriteFile(filepath.Join(dir, "recording-error.txt"), []byte(time.Now().UTC().Format(time.RFC3339)+" Recording could not be saved: "+err.Error()+"\n"), 0600)
}

func recordHook(in io.Reader, dir, project string) error {
	const limit = 2 * 1024 * 1024
	data, e := io.ReadAll(io.LimitReader(in, limit+1))
	if e != nil {
		return e
	}
	if len(data) > limit {
		return errors.New("event exceeded the 2 MB capture limit")
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	s, e := store.Open(filepath.Join(dir, "sparestep.db"))
	if e != nil {
		return e
	}
	defer s.Close()
	paused, e := s.Paused(project)
	if e != nil {
		return e
	}
	if paused {
		return nil
	}
	history, e := s.Events(project, 300)
	if e != nil {
		return e
	}
	events, e := hooks.Process(data, project, history)
	if e != nil {
		return e
	}
	for _, ev := range events {
		if ev.Kind == "task_started" {
			if e = s.PurgeBefore(time.Now().AddDate(0, 0, -30)); e != nil {
				return e
			}
		}
		if e = s.Record(ev); e != nil {
			return e
		}
	}
	return nil
}

func statusText(r model.Report) string {
	if r.Paused {
		return "Recording is paused."
	}
	switch r.Status {
	case "checks_passed":
		return "Checks passed after the latest edit."
	case "needs_attention":
		return "Some results need attention."
	case "recording", "working":
		return "Codex is working."
	case "no_activity":
		return "Waiting for the first recorded task."
	default:
		if r.EventsCount == 0 {
			return "Waiting for the first recorded task."
		}
		return "Activity recorded. Open the evidence to see what was checked."
	}
}

func printReport(s *store.Store, project string, asJSON bool, out io.Writer) error {
	r, e := s.Report(project)
	if e != nil {
		return e
	}
	if asJSON {
		return json.NewEncoder(out).Encode(r)
	}
	fmt.Fprintln(out, "Sparestep · Help Codex do less busywork.")
	if strings.HasPrefix(project, "/example/") {
		fmt.Fprintln(out, "EXAMPLE DATA — this is not a measurement of your work.")
	}
	fmt.Fprintf(out, "%s on %s\n\n%s\n", project, r.Hostname, statusText(r))
	fmt.Fprintf(out, "Checks: %d · failed: %d · unknown: %d\n", r.Checks, r.FailedChecks, r.UnknownChecks)
	if r.TokenUsage == nil {
		fmt.Fprintln(out, "Token usage unavailable in this connection.")
	} else {
		fmt.Fprintf(out, "Recorded token usage: %d (%s)\n", r.TokenUsage.Total, r.TokenUsage.Source)
	}
	if r.RecordingGap != "" {
		fmt.Fprintln(out, "Recording gap:", r.RecordingGap)
	}
	fmt.Fprintln(out, "Coverage: supported local tools; hosted tools and some remote activity may be unavailable.")
	n := 0
	for _, f := range r.Findings {
		if f.Disposition != "" && f.Disposition != "open" {
			continue
		}
		if n == 3 {
			break
		}
		n++
		fmt.Fprintf(out, "\n%d. %s\n   %s\n   %s\n   Review: sparestep review --project %s %s\n", n, f.Title, f.Explanation, f.Suggestion, shellQuote(project), f.ID)
	}
	if n == 0 {
		fmt.Fprintln(out, "\nNo clear opportunities found in the activity we recorded.")
	}
	fmt.Fprintln(out, "\nUse sparestep to review findings and feedback, or sparestep serve for the browser view.")
	return nil
}

func review(s *store.Store, project, id string, out io.Writer) error {
	r, e := s.Report(project)
	if e != nil {
		return e
	}
	for _, f := range r.Findings {
		if f.ID != id {
			continue
		}
		fmt.Fprintf(out, "%s\n\n%s\n\n%s\nStatus: %s\nObserved attempt time: %.1f seconds (not guaranteed savings)\n", f.Title, f.Explanation, f.Suggestion, f.Disposition, float64(f.DurationMS)/1000)
		for _, eid := range f.EvidenceIDs {
			for _, ev := range r.Events {
				if ev.ID == eid {
					fmt.Fprintf(out, "- %s · %s · %s [%s]\n", ev.Timestamp.Format(time.RFC3339), ev.Tool, ev.Summary, eid)
				}
			}
		}
		fmt.Fprintf(out, "\nGive feedback: sparestep feedback --project %s %s necessary|dismissed|open\nSave a draft: sparestep draft --project %s %s > issue.md\n", shellQuote(project), id, shellQuote(project), id)
		return nil
	}
	return errors.New("finding not found in this project")
}

func doctor(s *store.Store, project, dir string, out io.Writer) error {
	ok, detail := hooks.ConnectionStatus(project)
	fmt.Fprintln(out, "Project:", project)
	if !ok {
		fmt.Fprintln(out, "Connection: not installed. Use /sparestep in Codex to connect this project.")
	} else {
		fmt.Fprintln(out, "Connection:", detail)
	}
	if ok {
		fmt.Fprintln(out, "If capture is missing, open /hooks in Codex to review/trust the entries. The project must be trusted and hooks enabled.")
	}
	r, e := s.Report(project)
	if e != nil {
		return e
	}
	if ok && r.EventsCount > 0 {
		fmt.Fprintf(out, "Capture: %d events recorded.\n", r.EventsCount)
	} else {
		fmt.Fprintln(out, "Capture: not yet verified. Run a Codex task in this project, then check again.")
	}
	if r.Paused {
		fmt.Fprintln(out, "Recording is paused; run sparestep resume to resume.")
	}
	fmt.Fprintln(out, "Usage: hook capture does not supply actual token totals.")
	if b, err := os.ReadFile(filepath.Join(dir, "recording-error.txt")); err == nil {
		fmt.Fprintln(out, "Last recording error (may be historical):", string(b))
	}
	fmt.Fprintln(out, "State:", dir)
	return nil
}

func menu(s *store.Store, project, dir string, in io.Reader, out, errOut io.Writer) error {
	scanner := bufio.NewScanner(in)
	for {
		if e := printReport(s, project, false, out); e != nil {
			return e
		}
		fmt.Fprintln(out, "\n1 Review findings   2 Connect Codex   3 Browser view   4 Pause/resume")
		fmt.Fprintln(out, "5 Check connection  6 Remove connection  7 See an example  q Quit")
		fmt.Fprint(out, "Choose: ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		choice := strings.TrimSpace(scanner.Text())
		switch choice {
		case "q", "quit", "":
			return nil
		case "1":
			r, e := s.Report(project)
			if e != nil {
				return e
			}
			if len(r.Findings) == 0 {
				fmt.Fprintln(out, "No findings yet.")
				continue
			}
			for i, f := range r.Findings {
				fmt.Fprintf(out, "%d. %s [%s]\n", i+1, f.Title, f.Disposition)
			}
			fmt.Fprint(out, "Finding number (Enter to return): ")
			if !scanner.Scan() {
				return scanner.Err()
			}
			n, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
			if err != nil || n < 1 || n > len(r.Findings) {
				continue
			}
			f := r.Findings[n-1]
			if e = review(s, project, f.ID, out); e != nil {
				return e
			}
			fmt.Fprint(out, "n Necessary · d Dismiss · u Undo · s Save draft · l Link issue · Enter Back: ")
			if !scanner.Scan() {
				return scanner.Err()
			}
			switch strings.TrimSpace(scanner.Text()) {
			case "n":
				e = s.SetDisposition(project, f.ID, "necessary")
			case "d":
				e = s.SetDisposition(project, f.ID, "dismissed")
			case "u":
				e = s.SetDisposition(project, f.ID, "open")
			case "s":
				var d model.Draft
				d, e = s.Draft(project, f.ID)
				if e == nil {
					fmt.Fprintf(out, "\n# %s\n\n%s\n", d.Title, d.Body)
					fmt.Fprint(out, "Save path on this computer (Enter to cancel): ")
					if !scanner.Scan() {
						return scanner.Err()
					}
					p := strings.TrimSpace(scanner.Text())
					if p != "" {
						var file *os.File
						file, e = os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
						if e == nil {
							_, e = fmt.Fprintf(file, "# %s\n\n%s\n", d.Title, d.Body)
							ce := file.Close()
							if e == nil {
								e = ce
							}
							if e == nil {
								fmt.Fprintln(out, "Draft saved on this computer:", p)
							}
						}
					}
				}
			case "l":
				fmt.Fprint(out, "Existing Linear issue URL: ")
				if !scanner.Scan() {
					return scanner.Err()
				}
				e = s.SetIssueURL(project, f.ID, strings.TrimSpace(scanner.Text()))
			}
			if e != nil {
				fmt.Fprintln(errOut, "Could not save:", e)
			}
		case "2":
			binary, e := os.Executable()
			if e != nil {
				return e
			}
			p, e := hooks.Connect(project, binary, dir)
			if e != nil {
				fmt.Fprintln(errOut, e)
			} else {
				fmt.Fprintln(out, "Connection installed:", p, "\nIn Codex, open /hooks and review/trust the Sparestep entries.\nStart a new Codex task, then check the connection.")
			}
		case "3":
			return serve(s, project, dir, 7357, out)
		case "4":
			p, e := s.Paused(project)
			if e != nil {
				return e
			}
			if e = s.SetPaused(project, !p); e != nil {
				return e
			}
		case "5":
			if e := doctor(s, project, dir, out); e != nil {
				return e
			}
		case "6":
			if e := hooks.Disconnect(project); e != nil {
				fmt.Fprintln(errOut, e)
			} else {
				fmt.Fprintln(out, "Connection removed. History kept.")
			}
		case "7":
			tmp, e := os.MkdirTemp("", "sparestep-example-")
			if e != nil {
				return e
			}
			ds, e := store.Open(filepath.Join(tmp, "sparestep.db"))
			if e != nil {
				os.RemoveAll(tmp)
				return e
			}
			e = seedDemo(ds, "/example/login-app")
			if e == nil {
				e = printReport(ds, "/example/login-app", false, out)
			}
			ds.Close()
			os.RemoveAll(tmp)
			if e != nil {
				return e
			}
		default:
			fmt.Fprintln(out, "Choose a number or q to quit.")
		}
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func serve(s *store.Store, project, dir string, port int, out io.Writer) error {
	return serveReady(s, project, dir, port, "", out)
}

func serveReady(s *store.Store, project, dir string, port int, readyFile string, out io.Writer) error {
	if port < 0 || port > 65535 {
		return errors.New("port must be between 0 and 65535")
	}
	listener, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if e != nil && port != 0 {
		listener, e = net.Listen("tcp", "127.0.0.1:0")
	}
	if e != nil {
		return e
	}
	defer listener.Close()
	secret := make([]byte, 32)
	if _, e = rand.Read(secret); e != nil {
		return e
	}
	token := hex.EncodeToString(secret)
	actual := listener.Addr().(*net.TCPAddr).Port
	if readyFile != "" {
		endpoint := reportEndpoint{URL: fmt.Sprintf("http://127.0.0.1:%d/#token=%s", actual, token), PID: os.Getpid()}
		if e := writeEndpoint(readyFile, endpoint); e != nil {
			return e
		}
		defer removeEndpoint(readyFile, os.Getpid())
	}
	fmt.Fprintf(out, "Sparestep · %s\nBrowser address: http://127.0.0.1:%d/#token=%s\n", project, actual, token)
	if strings.HasPrefix(project, "/example/") {
		fmt.Fprintln(out, "EXAMPLE DATA. Nothing here describes your real work.")
	}
	fmt.Fprintf(out, "\nUsing a VM? On YOUR computer, run (replace USER@VM with your SSH host):\n  ssh -N -L 127.0.0.1:%d:127.0.0.1:%d USER@VM\nThen open the browser address above on YOUR computer.\n", actual, actual)
	fmt.Fprintln(out, "The address grants access to this local report. Keep this terminal open while viewing.")
	fmt.Fprintln(out, "Ctrl+C closes the report server. Codex hook recording continues independently.")
	srv := &http.Server{Handler: web.NewHandler(s, project, token, focusProvider(dir, project)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	e = srv.Serve(listener)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

func seedDemo(s *store.Store, project string) error {
	now := time.Now().UTC().Add(-10 * time.Minute)
	code := 127
	for i := 0; i < 3; i++ {
		ev := model.Event{ID: fmt.Sprintf("demo-setup-%d", i), Source: "demo", Project: project, SessionID: fmt.Sprintf("example-task-%d", i), TurnID: "turn-1", Kind: "tool_completed", Tool: "shell", OperationID: fmt.Sprintf("setup-%d", i), Command: "project setup", Timestamp: now.Add(time.Duration(i) * time.Minute), DurationMS: 40000, ExitCode: &code, FailureKey: "missing-runtime", Summary: "Required runtime was not found. Example evidence."}
		if e := s.Record(ev); e != nil {
			return e
		}
	}
	return nil
}

func help(w io.Writer) {
	fmt.Fprintln(w, `Sparestep — Help Codex do less busywork.

Start here:
  sparestep install-skill   Add /sparestep to Codex
  sparestep start           Connect this worktree and open its report
  sparestep open            Open or reuse this worktree's report
  sparestep                 Open the terminal guide and report
  sparestep demo            See an isolated example
  sparestep connect         Connect this project to Codex
  sparestep doctor          Check installation and observed activity
  sparestep serve           Open a local browser report (SSH instructions included)
  sparestep serve --demo    Explore the browser with example data

Focus (normally managed by /sparestep <task> in Codex):
  sparestep focus status [--json]
  sparestep focus begin | amend | defer | check | review | finish
  sparestep linear connect  Sign in once to file saved side work
  sparestep linear status   See pending delivery
  sparestep linear flush    Retry pending delivery

Activity reports:
  sparestep status [--json]
  sparestep review FINDING_ID
  sparestep feedback FINDING_ID necessary|dismissed|open
  sparestep draft FINDING_ID > issue.md
  sparestep link FINDING_ID https://linear.app/...

Control:
  sparestep pause | resume | disconnect
  sparestep prune --days 30

Options go before finding IDs: --project FOLDER, --state-dir FOLDER.
Records stay local. Connected Linear receives deferred issue details.
Sparestep makes no AI calls.
Actual token totals are unavailable from the initial hook connection.`)
}
