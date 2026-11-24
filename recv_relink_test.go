package main

import (
	"path/filepath"
	"testing"
	"time"
)

// The sender restarted and shared the same files again (new link, new file
// numbers). An older download and a newer duplicate of it must become one
// download on the new link that keeps every finished file.
func TestRelinkMergesDuplicate(t *testing.T) {
	dir := t.TempDir()
	files := func(a, b int) []RecvFile {
		return []RecvFile{{ID: a, Rel: "Game/a.bin", Size: 10}, {ID: b, Rel: "Game/b.bin", Size: 20}}
	}
	old := &RecvTask{ID: "old", Token: "tok1", Base: "http://x:1", Name: "Game", Dir: dir, Files: files(0, 1),
		Done: []int{0}, State: "error", Added: time.Now().Add(-time.Hour)}
	dup := &RecvTask{ID: "dup", Token: "tok2", Base: "http://x:1", Name: "Game", Dir: dir, Files: files(5, 6),
		State: "downloading", Added: time.Now()}
	other := &RecvTask{ID: "other", Token: "tok9", Name: "Other", Dir: dir, Files: []RecvFile{{ID: 0, Rel: "Other/z", Size: 1}}, State: "error"}
	recvTasks = []*RecvTask{old, dup, other}
	defer func() { recvTasks = nil }()

	m := &recvManifest{Token: "tok2", Base: "http://y:2", Name: "Game", By: "Rafi", Files: files(5, 6)}
	revive, drop, orphans := relinkRecvLocked(m, nil)

	if old.Token != "tok2" || old.Base != "http://y:2" || old.By != "Rafi" {
		t.Fatalf("old download not moved to the new link: %+v", old)
	}
	if old.Files[0].ID != 5 || old.Files[1].ID != 6 || len(old.Done) != 1 || old.Done[0] != 5 {
		t.Fatalf("file numbers not remapped: files=%+v done=%v", old.Files, old.Done)
	}
	if len(drop) != 1 || drop[0] != dup {
		t.Fatalf("duplicate not folded in: %v", drop)
	}
	if len(revive) != 1 || revive[0] != old {
		t.Fatalf("old download should continue: %v", revive)
	}
	want := filepath.Join(dir, "Game", "a.bin") + ".smpart"
	if len(orphans) != 1 || orphans[0] != want {
		t.Fatalf("orphans = %v, want [%s]", orphans, want)
	}
	if other.Token != "tok9" {
		t.Fatal("an unrelated download was touched")
	}

	// Opening the same link again changes nothing.
	if r, d, o := relinkRecvLocked(m, nil); r != nil || d != nil || o != nil {
		t.Fatalf("second open should be a no-op: %v %v %v", r, d, o)
	}
}

// Files that differ in size are a different share: never relinked.
func TestRelinkIgnoresChangedFiles(t *testing.T) {
	old := &RecvTask{ID: "old", Token: "tok1", Name: "Game", Dir: t.TempDir(),
		Files: []RecvFile{{ID: 0, Rel: "Game/a.bin", Size: 10}}, State: "error"}
	recvTasks = []*RecvTask{old}
	defer func() { recvTasks = nil }()
	m := &recvManifest{Token: "tok2", Name: "Game", Files: []RecvFile{{ID: 0, Rel: "Game/a.bin", Size: 11}}}
	if r, d, _ := relinkRecvLocked(m, nil); r != nil || d != nil || old.Token != "tok1" {
		t.Fatal("a changed file must not be resumed")
	}
}
