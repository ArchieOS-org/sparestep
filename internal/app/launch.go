package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ArchieOS-org/sparestep/internal/codexsetup"
	"github.com/ArchieOS-org/sparestep/internal/hooks"
	"github.com/ArchieOS-org/sparestep/internal/store"
)

type startResult struct {
	Project     string `json:"project"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	URL         string `json:"url,omitempty"`
	NextAction  string `json:"next_action,omitempty"`
	EventsCount int    `json:"events_count"`
}

func projectRoot(folder string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "git", "-C", folder, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.New("choose the project or worktree in Codex, then use /sparestep again")
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(b)))
}

func connectionResult(s *store.Store, project string) (startResult, error) {
	r, err := s.Report(project)
	if err != nil {
		return startResult{}, err
	}
	result := startResult{Project: project, EventsCount: r.EventsCount}
	connected, _ := hooks.ConnectionStatus(project)
	switch {
	case !connected:
		result.Status, result.Message, result.NextAction = "not_connected", "Sparestep is not connected to this project yet.", "/sparestep"
	case r.Paused:
		result.Status, result.Message = "paused", "Recording is paused. Your saved report is available."
	case r.EventsCount > 0:
		result.Status, result.Message = "observed", fmt.Sprintf("%d events recorded for %s.", r.EventsCount, filepath.Base(project))
	default:
		result.Status, result.Message = "waiting", "The connection is installed. Waiting for the first recorded event."
	}
	return result, nil
}

func start(s *store.Store, project, state string, port int, connect, asJSON bool, out io.Writer) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	var setup *codexsetup.Result
	if connect {
		if _, err = hooks.Connect(project, binary, state); err != nil {
			return fmt.Errorf("could not connect this project: %w", err)
		}
		result, err := codexsetup.Setup(project, binary, state)
		if err != nil {
			return err
		}
		setup = &result
	}
	result, err := connectionResult(s, project)
	if err != nil {
		return err
	}
	if setup != nil {
		if setup.Status != "ready" {
			result.Status, result.Message, result.NextAction = setup.Status, setup.Message, setup.NextAction
		} else if result.Status != "paused" {
			if result.EventsCount == 0 {
				result.Status, result.Message = "ready", "Sparestep is connected. Waiting for activity from Codex."
			}
			result.NextAction = setup.NextAction
		}
	}
	result.URL, err = openReport(binary, project, state, port)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintln(out, result.Message)
	fmt.Fprintln(out, "Report:", result.URL)
	if result.NextAction != "" {
		fmt.Fprintln(out, result.NextAction)
	}
	return nil
}

type reportEndpoint struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

func writeEndpoint(path string, endpoint reportEndpoint) error {
	data, err := json.Marshal(endpoint)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".report-ready-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func removeEndpoint(path string, pid int) {
	data, err := os.ReadFile(path)
	var endpoint reportEndpoint
	if err == nil && json.Unmarshal(data, &endpoint) == nil && endpoint.PID == pid {
		_ = os.Remove(path)
	}
}

func availableReport(path, project string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var endpoint reportEndpoint
	if json.Unmarshal(data, &endpoint) != nil {
		return "", false
	}
	u, err := url.Parse(endpoint.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil {
		return "", false
	}
	fragment, err := url.ParseQuery(u.Fragment)
	token := fragment.Get("token")
	if err != nil || len(token) != 64 {
		return "", false
	}
	u.Path, u.Fragment, u.RawQuery = "/api/health", "", ""
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 300 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	var report struct {
		Project string `json:"project"`
		Version string `json:"version"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&report) != nil || report.Project != project || report.Version != Version {
		return "", false
	}
	return endpoint.URL, true
}

// openReport starts at most one report per worktree and leaves recording independent.
func openReport(binary, project, state string, port int) (string, error) {
	if port < 0 || port > 65535 {
		return "", errors.New("port must be between 0 and 65535")
	}
	folder := filepath.Join(state, "reports")
	if err := os.MkdirAll(folder, 0700); err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(project))
	path := filepath.Join(folder, hex.EncodeToString(id[:12])+".json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			return "", errors.New("report is already opening; use /sparestep again in a moment")
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if address, ok := availableReport(path, project); ok {
		return address, nil
	}
	log, err := os.OpenFile(path+".log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer log.Close()
	command := exec.Command(binary, "serve", "--project", project, "--state-dir", state, "--port", fmt.Sprint(port), "--ready-file", path)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		return "", err
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if address, ok := availableReport(path, project); ok {
			_ = command.Process.Release()
			return address, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_ = command.Wait()
	return "", fmt.Errorf("the report could not start; details are in %s", path+".log")
}
