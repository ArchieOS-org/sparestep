package main

import (
	"os"
	"testing"
)

func retainedTestDir(t *testing.T) string {
	t.Helper()
	if os.Getenv("ONE_SHOT_KEEP_TEST_DIRS") == "" && os.Getenv("SHIP_IT_KEEP_TEST_DIRS") == "" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("", "one-shot-tally-test-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
