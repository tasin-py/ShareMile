package main

import (
	"path"
	"strconv"
	"strings"
)

// localRels makes every file of a share land on its own path here. A Linux
// sender can have "Photo.jpg" and "photo.jpg" (or "a:b" and "a_b") side by
// side; on Windows those would be the same file, so the later ones get
// " (2)", " (3)"… added to their name. Elsewhere nothing changes.
func localRels(n int, get func(int) string, set func(int, string)) {
	if !windowsNames {
		return
	}
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		rel := get(i)
		key := pathKey(cleanRel(rel))
		if !seen[key] {
			seen[key] = true
			continue
		}
		dir, file := path.Split(rel)
		ext := path.Ext(file)
		stem := strings.TrimSuffix(file, ext)
		for k := 2; ; k++ {
			cand := dir + stem + " (" + strconv.Itoa(k) + ")" + ext
			ck := pathKey(cleanRel(cand))
			if !seen[ck] {
				seen[ck] = true
				set(i, cand)
				break
			}
		}
	}
}
