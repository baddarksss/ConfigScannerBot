package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression (v1.0.13): the sanity check used os.Stat, which does not
// consult PATH — with the default XRAY_BIN=xray the bot exited even though
// the binary was on PATH.
func TestFindBinaryUsesPATH(t *testing.T) {
	if err := findBinary("sh"); err != nil {
		t.Fatalf("findBinary(sh): %v", err)
	}
	if err := findBinary("definitely-not-a-binary-xyz"); err == nil {
		t.Fatal("missing binary must error")
	}
	p := filepath.Join(t.TempDir(), "mybin")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := findBinary(p); err != nil {
		t.Fatalf("absolute path should work: %v", err)
	}
}
