package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Describe only types and field names when a result is unknown; never retain
// command output or user data for protocol diagnostics.
func responseShape(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return "invalid JSON"
	}
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k, child := range v {
			keys = append(keys, fmt.Sprintf("%s:%T", k, child))
		}
		sort.Strings(keys)
		return strings.Join(keys, ",")
	case []any:
		if len(v) > 0 {
			first, _ := json.Marshal(v[0])
			return "array[" + responseShape(first) + "]"
		}
		return "empty array"
	case string:
		return fmt.Sprintf("string; native exit header=%v; json object=%v", strings.Contains(v, "Process exited with code"), json.Valid([]byte(v)))
	default:
		return fmt.Sprintf("%T", value)
	}
}

// hookResponseResult classifies a tool response while keeping serialized
// native command envelopes scoped to command-capable tools.
func hookResponseResult(tool string, raw json.RawMessage) (known, succeeded bool) {
	if known, succeeded := explicitResponseResult(raw); known {
		return known, succeeded
	}
	if !nativeResponseTool(tool) {
		return false, false
	}

	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	envelopes := nativeResponseEnvelopes(value)
	if len(envelopes) == 0 {
		return false, false
	}
	failed := false
	for _, envelope := range envelopes {
		if envelope.ExitCode != 0 {
			failed = true
		}
	}
	return true, !failed
}

type nativeResponseEnvelope struct {
	ExitCode int
}

// nativeResponseEnvelopes only examines the response itself, its top-level
// content blocks, or the output wrapper used by the native command tool.
func nativeResponseEnvelopes(value any) []nativeResponseEnvelope {
	var result []nativeResponseEnvelope
	for _, object := range nativeResponseObjects(value) {
		if envelope, ok := decodeNativeResponseObject(object); ok {
			result = append(result, envelope)
		}
	}
	return result
}

func nativeResponseObjects(value any) []map[string]any {
	var result []map[string]any
	appendObject := func(object map[string]any) {
		result = append(result, object)
	}
	var inspectBlock func(any)
	inspectBlock = func(item any) {
		object, ok := item.(map[string]any)
		if !ok {
			return
		}
		blockType, typeOK := object["type"].(string)
		if !typeOK || (blockType != "input_text" && blockType != "text") {
			return
		}
		if text, ok := object["text"].(string); ok {
			var decoded map[string]any
			if json.Unmarshal([]byte(text), &decoded) == nil {
				appendObject(decoded)
			}
		}
	}

	switch item := value.(type) {
	case map[string]any:
		appendObject(item)
		if blocks, ok := item["output"].([]any); ok {
			for _, block := range blocks {
				inspectBlock(block)
			}
		}
	case []any:
		for _, block := range item {
			inspectBlock(block)
		}
	}
	return result
}

func nativeRunningSession(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, object := range nativeResponseObjects(value) {
		if _, complete := object["exit_code"]; complete {
			continue
		}
		if _, ok := object["chunk_id"].(string); !ok {
			continue
		}
		if _, ok := object["output"].(string); !ok {
			continue
		}
		if wallTime, ok := object["wall_time_seconds"].(float64); !ok || wallTime < 0 {
			continue
		}
		if sessionID, ok := canonicalSessionID(object["session_id"]); ok {
			return sessionID
		}
	}
	return ""
}

func inputExecutionSession(raw json.RawMessage) string {
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	sessionID, _ := canonicalSessionID(object["session_id"])
	return sessionID
}

func canonicalSessionID(value any) (string, bool) {
	switch item := value.(type) {
	case string:
		parsed, err := strconv.ParseUint(strings.TrimSpace(item), 10, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(parsed, 10), true
	case float64:
		if item < 0 || item != float64(uint64(item)) {
			return "", false
		}
		return strconv.FormatUint(uint64(item), 10), true
	default:
		return "", false
	}
}

func decodeNativeResponseObject(object map[string]any) (nativeResponseEnvelope, bool) {
	chunkID, chunkOK := object["chunk_id"].(string)
	exitCode, exitOK := object["exit_code"].(float64)
	_, outputOK := object["output"].(string)
	wallTime, wallOK := object["wall_time_seconds"].(float64)
	if !chunkOK || strings.TrimSpace(chunkID) == "" || !exitOK || !outputOK || !wallOK || wallTime < 0 || exitCode != float64(int(exitCode)) {
		return nativeResponseEnvelope{}, false
	}
	return nativeResponseEnvelope{ExitCode: int(exitCode)}, true
}

func nativeResponseTool(tool string) bool {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if isCommandTool(tool) {
		return true
	}
	base := filepath.Base(tool)
	return base == "write_stdin" || tool == "functions.write_stdin"
}
