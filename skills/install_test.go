package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallQuotesAbsoluteBinaryAndPreservesOtherSkills(t *testing.T) {
	destination := t.TempDir()
	other := filepath.Join(destination, "other")
	if err := os.MkdirAll(other, 0755); err != nil {
		t.Fatal(err)
	}
	const unrelated = "---\nname: other\n---\nKeep this skill.\n"
	if err := os.WriteFile(filepath.Join(other, "SKILL.md"), []byte(unrelated), 0644); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(destination, "spare step", "it's binary")
	installed, err := Install(destination, binary)
	if err != nil {
		t.Fatal(err)
	}
	if installed != destination {
		t.Fatalf("installed path = %q", installed)
	}
	got, err := os.ReadFile(filepath.Join(installed, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	absBinary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	wantToken := "'" + strings.ReplaceAll(absBinary, "'", "'\\''") + "'"
	if !strings.Contains(string(got), wantToken) {
		t.Fatalf("skill did not contain safely quoted absolute binary %q:\n%s", wantToken, got)
	}
	if gotOther, _ := os.ReadFile(filepath.Join(other, "SKILL.md")); string(gotOther) != unrelated {
		t.Fatal("unrelated existing skill was changed")
	}
	metadata, err := os.ReadFile(filepath.Join(installed, "agents", "openai.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "--json") || !strings.Contains(string(metadata), "allow_implicit_invocation: false") {
		t.Fatal("installed skill is missing bounded invocation instructions")
	}
}

func TestInstallRefusesUnownedSkill(t *testing.T) {
	destination := t.TempDir()
	dir := destination
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	const existing = "user-owned skill\n"
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(destination, "/tmp/sparestep"); err == nil {
		t.Fatal("expected unowned skill refusal")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != existing {
		t.Fatalf("unowned skill changed: %q, %v", got, readErr)
	}
}

func TestInstallBacksUpOwnedChangesOnce(t *testing.T) {
	destination := t.TempDir()
	path, err := Install(destination, "/tmp/sparestep")
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(path, "SKILL.md")
	first, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := append(append([]byte{}, first...), []byte("\nUser edit.\n")...)
	if err := os.WriteFile(skillPath, edited, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(destination, "/tmp/sparestep"); err != nil {
		t.Fatal(err)
	}
	backupPath := skillPath + ".bak"
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(edited) {
		t.Fatal("backup does not preserve the first owned edit")
	}
	if _, err := Install(destination, "/tmp/sparestep"); err != nil {
		t.Fatal(err)
	}
	backupAgain, err := os.ReadFile(backupPath)
	if err != nil || string(backupAgain) != string(edited) {
		t.Fatal("owned backup was overwritten")
	}
}

func TestInstallPreflightsMetadataBeforeChangingSkill(t *testing.T) {
	destination := t.TempDir()
	path, err := Install(destination, "/tmp/sparestep")
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(path, "SKILL.md")
	metadataPath := filepath.Join(path, "agents", "openai.yaml")
	original, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := append(append([]byte{}, original...), []byte("\nUser edit.\n")...)
	if err := os.WriteFile(skillPath, edited, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte("user-owned metadata\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(destination, "/tmp/another-sparestep"); err == nil {
		t.Fatal("expected unowned metadata refusal")
	}
	got, err := os.ReadFile(skillPath)
	if err != nil || string(got) != string(edited) {
		t.Fatalf("skill changed before metadata preflight: %q, %v", got, err)
	}
	if _, err := os.Stat(skillPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("skill backup created before metadata preflight: %v", err)
	}
}
