// Package skills installs Sparestep's explicit Codex skill.
package skills

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const managedMarker = "sparestep-managed-skill: v1"

//go:embed sparestep/SKILL.md sparestep/agents/openai.yaml
var files embed.FS

// Install installs the managed "sparestep" skill in destination and
// returns its directory. Existing unowned files are never replaced. A changed
// owned file gets one .bak copy before it is updated.
func Install(destination, binary string) (string, error) {
	if destination == "" {
		return "", errors.New("skill destination is empty")
	}
	if binary == "" {
		return "", errors.New("Sparestep binary is empty")
	}

	destination, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve skill destination: %w", err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return "", fmt.Errorf("resolve Sparestep binary: %w", err)
	}

	skillDir := destination
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return "", fmt.Errorf("create skill directory: %w", err)
	}

	skill, err := files.ReadFile("sparestep/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("read embedded skill: %w", err)
	}
	skill = []byte(strings.ReplaceAll(string(skill), "{{BINARY}}", shellQuote(binary)))
	yaml, err := files.ReadFile("sparestep/agents/openai.yaml")
	if err != nil {
		return "", fmt.Errorf("read embedded skill metadata: %w", err)
	}

	skillPath := filepath.Join(skillDir, "SKILL.md")
	agentsDir := filepath.Join(skillDir, "agents")
	metadataPath := filepath.Join(agentsDir, "openai.yaml")
	// Inspect every existing managed file before changing either one. This
	// prevents an owned skill from being updated when metadata is unowned.
	if err := preflightFile(skillPath, skill); err != nil {
		return "", err
	}
	if info, err := os.Lstat(agentsDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to use symlink skill metadata directory %s", agentsDir)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("skill metadata path is not a directory: %s", agentsDir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect skill metadata directory %s: %w", agentsDir, err)
	}
	if err := preflightFile(metadataPath, yaml); err != nil {
		return "", err
	}

	if err := installFile(skillPath, skill); err != nil {
		return "", err
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return "", fmt.Errorf("create skill metadata directory: %w", err)
	}
	if err := installFile(metadataPath, yaml); err != nil {
		return "", err
	}
	return skillDir, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func preflightFile(path string, content []byte) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed skill file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink %s", path)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read existing managed file %s: %w", path, err)
	}
	if string(old) != string(content) && !strings.Contains(string(old), managedMarker) {
		return fmt.Errorf("refusing to replace unowned skill file %s", path)
	}
	return nil
}

func installFile(path string, content []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to replace symlink %s", path)
		}
		old, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read existing managed file %s: %w", path, err)
		}
		if string(old) == string(content) {
			return nil
		}
		if !strings.Contains(string(old), managedMarker) {
			return fmt.Errorf("refusing to replace unowned skill file %s", path)
		}
		backup := path + ".bak"
		if _, backupErr := os.Lstat(backup); errors.Is(backupErr, os.ErrNotExist) {
			if err := writeFile(backup, old, info.Mode().Perm()); err != nil {
				return fmt.Errorf("backup managed skill file %s: %w", path, err)
			}
		} else if backupErr != nil {
			return fmt.Errorf("inspect managed skill backup %s: %w", backup, backupErr)
		}
		return writeFile(path, content, info.Mode().Perm())
	}
	if err := writeFile(path, content, 0644); err != nil {
		return fmt.Errorf("write managed skill file %s: %w", path, err)
	}
	return nil
}

func writeFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sparestep-skill-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
