package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/focus"
	"github.com/ArchieOS-org/sparestep/internal/linear"
)

func repositoryIdentity(project string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if b, err := exec.CommandContext(ctx, "git", "-C", project, "remote", "get-url", "origin").Output(); err == nil {
		raw := strings.TrimSpace(string(b))
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return strings.ToLower(u.Host) + strings.TrimSuffix(u.Path, ".git")
		}
		if colon := strings.Index(raw, ":"); colon > 0 && !strings.Contains(raw[:colon], "/") {
			host := raw[:colon]
			if at := strings.LastIndex(host, "@"); at >= 0 {
				host = host[at+1:]
			}
			return strings.ToLower(host) + "/" + strings.TrimSuffix(raw[colon+1:], ".git")
		}
	}
	common := project
	if b, err := exec.CommandContext(ctx, "git", "-C", project, "rev-parse", "--git-common-dir").Output(); err == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(project, common)
		}
		if p, e := filepath.EvalSymlinks(common); e == nil {
			common = p
		}
	}
	h := sha256.Sum256([]byte(common))
	return "local:" + hex.EncodeToString(h[:])
}

func enqueueDeferred(in io.Reader, out io.Writer, dir string, task *focus.Task) error {
	var input struct {
		Component    string   `json:"component"`
		Problem      string   `json:"problem"`
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		Criteria     []string `json:"criteria"`
		Evidence     []string `json:"evidence"`
		TypeLabel    string   `json:"type_label"`
		AreaLabel    string   `json:"area_label"`
		SurfaceLabel string   `json:"surface_label,omitempty"`
	}
	if err := readJSON(in, &input); err != nil {
		return err
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Description) == "" || len(input.Criteria) == 0 || len(input.Evidence) == 0 {
		return errors.New("deferred work needs a title, concrete problem, evidence, and completion criteria")
	}
	if task.Mode == "planning" || task.Status != "active" {
		return errors.New("defer implementation side work only while its focus task is active")
	}
	o, err := linear.Open(filepath.Join(dir, "linear.db"))
	if err != nil {
		return err
	}
	defer o.Close()
	item := linear.NewItem(linear.ItemInput{TaskID: task.ID, Repository: repositoryIdentity(task.Project), Component: input.Component, Problem: input.Problem, Title: input.Title, Criteria: input.Criteria, Evidence: input.Evidence})
	item.Body = linear.ComposeBody(input.Description, input.Criteria, input.Evidence)
	item.TypeLabel, item.AreaLabel, item.SurfaceLabel = input.TypeLabel, input.AreaLabel, input.SurfaceLabel
	if err = defaultDispatchDestination(dir, task.Project, item.Repository); err != nil {
		return err
	}
	saved, err := o.Enqueue(item)
	if err != nil {
		return err
	}
	kickPublisher(dir)
	return json.NewEncoder(out).Encode(map[string]any{"item": saved, "message": "Side work saved. Continue the original task; Linear delivery runs separately."})
}

func defaultDispatchDestination(dir, project, repository string) error {
	known, err := loadDestinations(dir)
	if err != nil {
		return err
	}
	if _, ok := known[repository]; ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "git", "-C", project, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return nil
	}
	common := strings.TrimSpace(string(b))
	if !filepath.IsAbs(common) {
		common = filepath.Join(project, common)
	}
	if canonical, err := filepath.EvalSymlinks(common); err == nil {
		common = canonical
	}
	if filepath.Base(filepath.Dir(common)) != "dispatch-monorepo" {
		return nil
	}
	// This is a setup default, never proof that the destination is still
	// valid. The publisher validates live team/project/state policy first.
	return saveDestination(dir, repository, linear.Destination{TeamID: "ca612367-ef47-43c8-bda5-14ac9ac9358e", ProjectID: "Dispatch v1 — Duncan feedback", AssigneeID: "me", State: "Backlog"})
}

func deferredForTask(dir, taskID string) ([]linear.Item, error) {
	if _, err := os.Stat(filepath.Join(dir, "linear.db")); os.IsNotExist(err) {
		return []linear.Item{}, nil
	}
	o, err := linear.Open(filepath.Join(dir, "linear.db"))
	if err != nil {
		return nil, err
	}
	defer o.Close()
	items, err := o.Items(taskID)
	if items == nil {
		items = []linear.Item{}
	}
	return items, err
}

func writeFocusStatus(out io.Writer, dir string, task *focus.Task) error {
	items, err := deferredForTask(dir, task.ID)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"focus": task, "deferred": items})
}

func loadDestinations(dir string) (map[string]linear.Destination, error) {
	data, err := os.ReadFile(filepath.Join(dir, "linear-projects.json"))
	if os.IsNotExist(err) {
		return map[string]linear.Destination{}, nil
	}
	if err != nil {
		return nil, err
	}
	var destinations map[string]linear.Destination
	if err = json.Unmarshal(data, &destinations); err != nil {
		return nil, errors.New("Linear destination settings could not be read")
	}
	return destinations, nil
}

func saveDestination(dir, repository string, d linear.Destination) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "linear-projects.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	destinations, err := loadDestinations(dir)
	if err != nil {
		return err
	}
	destinations[repository] = d
	data, err := json.MarshalIndent(destinations, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".linear-projects-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "linear-projects.json"))
}

func runLinear(args []string, state string, in io.Reader, out, errOut io.Writer) error {
	action := "status"
	if len(args) > 0 {
		action, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("sparestep linear "+action, flag.ContinueOnError)
	fs.SetOutput(errOut)
	dir := fs.String("state-dir", state, "Private state folder")
	project := fs.String("project", "", "Project/worktree for destination setup")
	callbackURL := fs.String("callback-url", "", "Public HTTPS callback URL for a remote browser")
	callbackPort := fs.Int("callback-port", 0, "Loopback port forwarded by the public callback")
	_ = fs.Bool("json", false, "Print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absolute, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	*dir = absolute
	if action == "worker" || action == "flush" {
		return flushLinear(*dir, out)
	}
	if action == "status" {
		connected := false
		if _, err := os.Stat(filepath.Join(*dir, "linear-oauth.json")); err == nil {
			connected = true
		}
		items := []linear.Item{}
		if _, err := os.Stat(filepath.Join(*dir, "linear.db")); err == nil {
			o, e := linear.Open(filepath.Join(*dir, "linear.db"))
			if e != nil {
				return e
			}
			items, e = o.Pending()
			o.Close()
			if e != nil {
				return e
			}
		}
		return json.NewEncoder(out).Encode(map[string]any{"credentials_saved": connected, "pending": items})
	}
	o, err := linear.Open(filepath.Join(*dir, "linear.db"))
	if err != nil {
		return err
	}
	defer o.Close()
	switch action {
	case "configure":
		if *project == "" {
			return errors.New("choose the project/worktree for this Linear destination")
		}
		root, err := projectRoot(*project)
		if err != nil {
			return err
		}
		var destination linear.Destination
		if err = readJSON(in, &destination); err != nil {
			return err
		}
		if err = o.Configure(destination); err != nil {
			return err
		}
		if err = saveDestination(*dir, repositoryIdentity(root), destination); err != nil {
			return err
		}
		kickPublisher(*dir)
		return json.NewEncoder(out).Encode(map[string]string{"message": "Destination saved. Live issue policy is checked before anything is filed."})
	case "connect":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err = o.Connect(ctx, *dir, linear.ConnectOptions{CallbackURL: *callbackURL, CallbackPort: *callbackPort, DisplayURL: func(address string) {
			fmt.Fprintln(out, "Connect Linear:", address)
			if u, e := url.Parse(address); e == nil {
				if callback, e := url.Parse(u.Query().Get("redirect_uri")); e == nil && callback.Hostname() == "127.0.0.1" && callback.Port() != "" {
					fmt.Fprintf(out, "Using an SSH VM? On your computer, forward the sign-in callback before opening that link:\nssh -N -L 127.0.0.1:%s:127.0.0.1:%s USER@VM\n", callback.Port(), callback.Port())
				}
			}
		}})
		if err != nil {
			return errors.New("Linear sign-in did not finish; queued work is still saved. Run /sparestep linear connect to retry")
		}
		if *project != "" {
			root, e := projectRoot(*project)
			if e != nil {
				return e
			}
			if e = defaultDispatchDestination(*dir, root, repositoryIdentity(root)); e != nil {
				return e
			}
		}
		kickPublisher(*dir)
		return json.NewEncoder(out).Encode(map[string]string{"message": "Linear connected. Saved side work can now be filed."})
	default:
		return fmt.Errorf("unknown Linear action %q", action)
	}
}

func kickPublisher(dir string) {
	if _, err := os.Stat(filepath.Join(dir, "linear.db")); err != nil {
		return
	}
	binary, err := os.Executable()
	if err != nil {
		return
	}
	// A detached, bounded worker survives its invoking shell. It has no model
	// and does not depend on the report server. A process lock prevents overlap.
	log, err := os.OpenFile(filepath.Join(dir, "linear-worker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer log.Close()
	command := exec.Command(binary, "linear", "worker", "--state-dir", dir)
	command.Stdin = nil
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err == nil {
		_ = command.Process.Release()
	}
}

func flushLinear(dir string, out io.Writer) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "linear-worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	o, err := linear.Open(filepath.Join(dir, "linear.db"))
	if err != nil {
		return err
	}
	defer o.Close()
	items, err := o.Pending()
	if err != nil || len(items) == 0 {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err = o.Connect(ctx, dir, linear.ConnectOptions{NoBrowser: true}); err != nil {
		return errors.New("Linear needs sign-in or is unavailable; side work remains queued")
	}
	destinations, err := loadDestinations(dir)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.TaskID] {
			continue
		}
		seen[item.TaskID] = true
		destination, ok := destinations[item.Repository]
		if !ok {
			if err = o.MarkAttention(item.TaskID, "Choose a Linear destination for this repository with /sparestep linear configure."); err != nil {
				return err
			}
			continue
		}
		if err = o.Configure(destination); err != nil {
			return err
		}
		result, err := o.Flush(ctx, linear.FlushOptions{TaskID: item.TaskID, Limit: 10, Reconcile: true})
		if err != nil {
			return err
		}
		if err = json.NewEncoder(out).Encode(result); err != nil {
			return err
		}
	}
	return nil
}
