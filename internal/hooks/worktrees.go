package hooks

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func metadataLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SharedHookProject mirrors Codex's root-checkout hook discovery for linked
// worktrees. Ordinary repositories and submodules keep their own definitions.
// Reading Git metadata avoids spawning Git on every tool hook.
func SharedHookProject(project string) string {
	project = canonicalPath(project)
	line := metadataLine(filepath.Join(project, ".git"))
	if !strings.HasPrefix(line, "gitdir: ") {
		return project
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(project, gitDir)
	}
	common := metadataLine(filepath.Join(gitDir, "commondir"))
	if common == "" {
		return project
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	common = canonicalPath(common)
	if filepath.Base(common) != ".git" {
		return project
	}
	if info, err := os.Stat(common); err != nil || !info.IsDir() {
		return project
	}
	// Git records the reciprocal worktree path; do not route arbitrary gitfiles.
	backlink := metadataLine(filepath.Join(gitDir, "gitdir"))
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(gitDir, backlink)
	}
	if canonicalPath(backlink) != filepath.Join(project, ".git") {
		return project
	}
	return filepath.Dir(common)
}

// ResolveHookProject routes shared hooks only to an explicitly connected
// worktree of that checkout. The task's own project remains its scope boundary.
func ResolveHookProject(configured, cwd string) (string, bool) {
	if configured == "" || cwd == "" || !filepath.IsAbs(cwd) {
		return "", false
	}
	configured, cwd = canonicalPath(configured), canonicalPath(cwd)
	for candidate := cwd; ; candidate = filepath.Dir(candidate) {
		if _, err := os.Lstat(filepath.Join(candidate, ".git")); err == nil {
			if candidate == configured {
				return configured, true
			}
			if SharedHookProject(candidate) == configured {
				connected, _ := ConnectionStatus(candidate)
				return candidate, connected
			}
			return "", false
		}
		if candidate == configured {
			return configured, true
		}
		if filepath.Dir(candidate) == candidate {
			return "", false
		}
	}
}
