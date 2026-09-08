package focus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadNativeMode(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
		ok   bool
	}{
		{
			name: "plan",
			data: nativeModeLine("turn-1", `{"mode":"plan"}`),
			want: "plan", ok: true,
		},
		{
			name: "default",
			data: nativeModeLine("turn-1", `{"mode":"default"}`),
			want: "default", ok: true,
		},
		{
			name: "wrong turn",
			data: nativeModeLine("turn-other", `{"mode":"plan"}`),
		},
		{
			name: "plaintext prompt fake",
			data: `{"type":"response_item","payload":{"text":"turn-1 collaboration_mode plan"}}` + "\n",
		},
		{
			name: "missing mode",
			data: nativeModeLine("turn-1", `{}`),
		},
		{
			name: "null mode",
			data: nativeModeLine("turn-1", `null`),
		},
		{
			name: "invalid mode",
			data: nativeModeLine("turn-1", `{"mode":"review"}`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			got, ok := readNativeMode(path, "turn-1")
			if got != tc.want || ok != tc.ok {
				t.Fatalf("readNativeMode() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestReadNativeModeSkipsPartialRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := "partial prefix " + nativeModeLine("turn-1", `{"mode":"plan"}`) + "truncated"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readNativeMode(path, "turn-1"); ok || got != "" {
		t.Fatalf("partial records must abstain, got (%q, %v)", got, ok)
	}

	data = nativeModeLine("turn-1", `{"mode":"plan"}`) + "truncated"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readNativeMode(path, "turn-1"); !ok || got != "plan" {
		t.Fatalf("partial final record must be skipped, got (%q, %v)", got, ok)
	}
}

func TestReadNativeModeOversizeContextAbstains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	giant := fmt.Sprintf(`{"type":"turn_context","payload":{"turn_id":"turn-1","collaboration_mode":{"mode":"plan"},"padding":"%s"}}`, strings.Repeat("x", int(nativeModeReadLimit))) + "\n"
	if err := os.WriteFile(path, []byte(giant), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readNativeMode(path, "turn-1"); ok || got != "" {
		t.Fatalf("oversize context must abstain, got (%q, %v)", got, ok)
	}
}

func TestReadNativeModeRejectsNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	if got, ok := readNativeMode(dir, "turn-1"); ok || got != "" {
		t.Fatalf("directory must be rejected, got (%q, %v)", got, ok)
	}

	pipe := filepath.Join(t.TempDir(), "rollout.pipe")
	if err := os.MkdirAll(filepath.Dir(pipe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := makeFIFO(pipe); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	if got, ok := readNativeMode(pipe, "turn-1"); ok || got != "" {
		t.Fatalf("FIFO must be rejected before open, got (%q, %v)", got, ok)
	}

	target := filepath.Join(t.TempDir(), "target.jsonl")
	if err := os.WriteFile(target, []byte(nativeModeLine("turn-1", `{"mode":"plan"}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, ok := readNativeMode(link, "turn-1"); ok || got != "" {
		t.Fatalf("symlink must be rejected, got (%q, %v)", got, ok)
	}
}

func nativeModeLine(turnID, mode string) string {
	return fmt.Sprintf(`{"type":"turn_context","payload":{"turn_id":%q,"collaboration_mode":%s}}`+"\n", turnID, mode)
}

func makeFIFO(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
