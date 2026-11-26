package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanRecycle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(p, []byte("x"), 0o644)
	if !canRecycle(p, 1) {
		t.Errorf("a tiny file on a fixed drive should fit in the Recycle Bin")
	}
	if canRecycle(p, 1<<50) {
		t.Errorf("a 1 PB file can't fit in the Recycle Bin")
	}
	if canRecycle(`\server\share\x.txt`, 1) {
		t.Errorf("network paths have no Recycle Bin")
	}
}
