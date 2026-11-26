//go:build linux

package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	dir := filepath.Join(home, "dl")
	os.MkdirAll(dir, 0o755)
	var files []string
	for _, n := range []string{"a.txt", "a.txt.dup", "b c%.bin"} {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte(n), 0o644)
		files = append(files, p)
	}
	if !canRecycle(files[0], 1) {
		t.Fatal("a file in the home folder should be able to go to the Trash")
	}
	gone, err := recycle(files)
	if err != nil || gone != 3 {
		t.Fatalf("recycle: gone=%d err=%v", gone, err)
	}
	trash := filepath.Join(home, "data", "Trash")
	for _, n := range []string{"a.txt", "a.txt.dup", "b c%.bin"} {
		if _, err := os.Stat(filepath.Join(trash, "files", n)); err != nil {
			t.Errorf("%s not in Trash/files", n)
		}
		info, err := os.ReadFile(filepath.Join(trash, "info", n+".trashinfo"))
		if err != nil || !strings.Contains(string(info), "[Trash Info]") || !strings.Contains(string(info), "DeletionDate=") {
			t.Errorf("%s: bad trashinfo %q", n, info)
		}
	}
	info, _ := os.ReadFile(filepath.Join(trash, "info", "b c%.bin.trashinfo"))
	if !strings.Contains(string(info), "b%20c%25.bin") {
		t.Errorf("path not URL-escaped: %s", info)
	}
	// the same name again gets a new name in the Trash, nothing is overwritten
	os.WriteFile(files[0], []byte("second"), 0o644)
	if _, err := recycle(files[:1]); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(trash, "files", "a.2.txt")); string(b) != "second" {
		t.Errorf("second a.txt should be a.2.txt in the Trash, got %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(trash, "files", "a.txt")); string(b) != "a.txt" {
		t.Errorf("first a.txt was overwritten")
	}
}

func TestExtractProgram(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, files map[string]string) string {
		p := filepath.Join(dir, name)
		f, _ := os.Create(p)
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for n, body := range files {
			tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
			tw.Write([]byte(body))
		}
		tw.Close()
		gz.Close()
		f.Close()
		return p
	}
	good := mk("good.tgz", map[string]string{"ShareMile/README.txt": "x", "ShareMile/sharemile": "\x7fELF....program"})
	out := filepath.Join(dir, "out")
	if err := extractProgram(good, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "\x7fELF....program" {
		t.Fatalf("wrong program %q", b)
	}
	if st, _ := os.Stat(out); st.Mode()&0o111 == 0 {
		t.Fatal("program isn't executable")
	}
	bad := mk("bad.tgz", map[string]string{"ShareMile/sharemile": "<html>not found</html>"})
	if err := extractProgram(bad, out); err == nil {
		t.Fatal("an HTML page must not be accepted as the program")
	}
	none := mk("none.tgz", map[string]string{"ShareMile/other": "\x7fELF"})
	if err := extractProgram(none, out); err == nil {
		t.Fatal("a tarball without the program must fail")
	}
	os.WriteFile(filepath.Join(dir, "junk.tgz"), []byte("junk"), 0o644)
	if err := extractProgram(filepath.Join(dir, "junk.tgz"), out); err == nil {
		t.Fatal("junk must fail")
	}
}

func TestPortIn(t *testing.T) {
	cases := []struct {
		list string
		port int
		want bool
	}{
		{"8750/tcp", 8750, true},
		{"1025-65535/tcp 1025-65535/udp", 8750, true},
		{"8750/udp", 8750, false},
		{"80/tcp 443/tcp", 8750, false},
		{"", 8750, false},
		{"8000-8749/tcp", 8750, false},
	}
	for _, c := range cases {
		if got := portIn(c.list, c.port); got != c.want {
			t.Errorf("portIn(%q,%d)=%v", c.list, c.port, got)
		}
	}
}

func TestLinuxNames(t *testing.T) {
	for in, want := range map[string]string{
		".bashrc": ".bashrc", "Ep 1: Pilot?.mkv": "Ep 1: Pilot?.mkv", "a/b": "a_b",
		"..": "file", ".": "file", "": "file", "tab\there": "tab_here",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q)=%q want %q", in, got, want)
		}
	}
	if got := cleanRel("../../etc/passwd"); got != "etc/passwd" {
		t.Errorf("cleanRel escaped the folder: %q", got)
	}
}

func TestDownloadsFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if got := downloadsFolder(); got != filepath.Join(home, "Downloads") {
		t.Errorf("default: %s", got)
	}
	os.MkdirAll(filepath.Join(home, ".config"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "user-dirs.dirs"), []byte("# x\nXDG_DOWNLOAD_DIR=\"$HOME/Téléchargements\"\n"), 0o644)
	if got := downloadsFolder(); got != filepath.Join(home, "Téléchargements") {
		t.Errorf("xdg: %s", got)
	}
}

func TestQuoteExec(t *testing.T) {
	if quoteExec("/opt/sharemile/sharemile") != "/opt/sharemile/sharemile" {
		t.Error("plain path changed")
	}
	if got := quoteExec("/home/a b/sharemile"); got != `"/home/a b/sharemile"` {
		t.Errorf("space: %s", got)
	}
}
