package focus

import (
	"encoding/json"
	"io"
	"os"
	"syscall"
)

const nativeModeReadLimit int64 = 256 << 10

// readNativeMode reads only the typed turn-context metadata for turnID from a
// native Codex rollout. It intentionally does not expose or retain transcript
// content. The bool is false when the mode cannot be established safely.
func readNativeMode(path, turnID string) (string, bool) {
	if path == "" || turnID == "" {
		return "", false
	}

	// Lstat rejects symlinks and non-regular files before opening. The second
	// Stat protects the bounded read if the path changes between those calls.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", false
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}

	size := info.Size()
	if size <= 0 {
		return "", false
	}
	readSize := size
	if readSize > nativeModeReadLimit {
		readSize = nativeModeReadLimit
	}
	buf := make([]byte, readSize)
	_, err = file.ReadAt(buf, size-readSize)
	if err != nil && err != io.EOF {
		return "", false
	}

	// A tail may begin or end in the middle of a JSONL record. Discard both
	// partial records so a large or concurrently-written record cannot be used.
	start := 0
	if size > readSize {
		for start < len(buf) && buf[start] != '\n' {
			start++
		}
		if start == len(buf) {
			return "", false
		}
		start++
	}
	end := len(buf)
	if end > start && buf[end-1] != '\n' {
		for end > start && buf[end-1] != '\n' {
			end--
		}
	}
	if start >= end {
		return "", false
	}

	lines := splitNativeModeLines(buf[start:end])
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		var record nativeTurnContextRecord
		if json.Unmarshal(line, &record) != nil || record.Type != "turn_context" || record.Payload == nil {
			continue
		}
		if record.Payload.TurnID != turnID || record.Payload.CollaborationMode == nil {
			continue
		}
		switch record.Payload.CollaborationMode.Mode {
		case "plan", "default":
			return record.Payload.CollaborationMode.Mode, true
		}
	}
	return "", false
}

type nativeTurnContextRecord struct {
	Type    string                    `json:"type"`
	Payload *nativeTurnContextPayload `json:"payload"`
}

type nativeTurnContextPayload struct {
	TurnID            string                   `json:"turn_id"`
	CollaborationMode *nativeCollaborationMode `json:"collaboration_mode"`
}

type nativeCollaborationMode struct {
	Mode string `json:"mode"`
}

func splitNativeModeLines(data []byte) [][]byte {
	var lines [][]byte
	for len(data) > 0 {
		idx := 0
		for idx < len(data) && data[idx] != '\n' {
			idx++
		}
		if idx == len(data) {
			break
		}
		lines = append(lines, data[:idx])
		data = data[idx+1:]
	}
	return lines
}
