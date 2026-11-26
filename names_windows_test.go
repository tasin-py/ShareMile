package main

import "testing"

func TestLocalRelsWindows(t *testing.T) {
	rels := []string{"Top/Photo.jpg", "Top/photo.jpg", "Top/a:b.txt", "Top/a_b.txt", "Top/PHOTO.jpg", "Top/x"}
	localRels(len(rels), func(i int) string { return rels[i] }, func(i int, r string) { rels[i] = r })
	want := []string{"Top/Photo.jpg", "Top/photo (2).jpg", "Top/a:b.txt", "Top/a_b (2).txt", "Top/PHOTO (3).jpg", "Top/x"}
	for i := range want {
		if rels[i] != want[i] {
			t.Errorf("%d: %q want %q", i, rels[i], want[i])
		}
	}
}

func TestSafeNameWindows(t *testing.T) {
	for in, want := range map[string]string{
		".hidden.conf": ".hidden.conf", "trail.": "trail", "end. . ": "end", "  lead": "lead",
		"a:b?.txt": "a_b_.txt", "CON": "CON_", "con.txt": "con_.txt", "aux": "aux_", "Nul.tar.gz": "Nul_.tar.gz",
		"COM1.log": "COM1_.log", "lpt9": "lpt9_", "COM10": "COM10", "console.txt": "console.txt", "...": "file", "": "file",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q)=%q want %q", in, got, want)
		}
	}
}
