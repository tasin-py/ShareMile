package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Check files: compares a download's folder with the sender's current file
// list. Files with the sender's size and date match; same size with another
// date is compared at three spots (like the check before downloading). Files
// in the shared folder that the sender doesn't have are "extra".

type verifyItem struct {
	Rel  string `json:"rel"`
	Size int64  `json:"size"`
	Why  string `json:"why,omitempty"` // missing|partial|changed
}

type verifyState struct {
	token   string
	missing []RecvFile
	extra   []string // full paths
	scan    string   // folder searched for extra files
}

var (
	verifyMu sync.Mutex
	verifies = map[string]*verifyState{} // task id -> last check
)

const verifyListMax = 2000

func recvVerify(id string) (map[string]any, error) {
	t := findRecv(id)
	if t == nil {
		return nil, errors.New("that download is gone")
	}
	if !t.vBusy.CompareAndSwap(false, true) {
		return nil, errors.New("already checking these files")
	}
	defer t.vBusy.Store(false)
	t.mu.Lock()
	base, token, dir, name := t.Base, t.Token, t.Dir, t.Name
	t.mu.Unlock()

	m, err := fetchManifest(base + "/s/" + token)
	if err != nil {
		if err.Error() == blockedMsg {
			return nil, err
		}
		return nil, fmt.Errorf("couldn't get the sender's file list (%v). If they restarted ShareMile, choose New download link in the ⋮ menu first, then check again", err)
	}
	recvMu.Lock()
	recvCache[token] = m
	recvMu.Unlock()

	var missing, sample []RecvFile
	why := map[int]string{}
	ok, okBytes := 0, int64(0)
	for _, f := range m.Files {
		p := filepath.Join(dir, cleanRel(f.Rel))
		st, err := os.Stat(p)
		switch {
		case err != nil || st.IsDir():
			why[f.ID] = "missing"
			if fileExists(p + ".smpart") {
				why[f.ID] = "partial"
			}
			missing = append(missing, f)
		case st.Size() != f.Size:
			why[f.ID] = "changed"
			missing = append(missing, f)
		case f.MTime > 0 && abs64(st.ModTime().UnixMilli()-f.MTime) < 2000:
			ok++
			okBytes += f.Size
		default:
			sample = append(sample, f)
		}
	}

	// Same size, other date: compare a few spots with the sender, 8 at a time.
	if len(sample) > 0 {
		t.vN.Store(int64(len(sample)))
		t.vOK.Store(0)
		defer t.vN.Store(0)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mu sync.Mutex
		var firstErr error
		q := make(chan RecvFile)
		var wg sync.WaitGroup
		for w := 0; w < recvWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for f := range q {
					same, err := sameAsSender(ctx, base, token, f, filepath.Join(dir, cleanRel(f.Rel)))
					mu.Lock()
					switch {
					case err != nil:
						if firstErr == nil {
							firstErr = err
							cancel()
						}
					case same:
						ok++
						okBytes += f.Size
					default:
						why[f.ID] = "changed"
						missing = append(missing, f)
					}
					mu.Unlock()
					t.vOK.Add(1)
				}
			}()
		}
		for _, f := range sample {
			if ctx.Err() != nil {
				break
			}
			q <- f
		}
		close(q)
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Rel < missing[j].Rel })

	// Extra files: only inside the folder made for this share, never loose
	// files next to a single shared file.
	scan := ""
	if top := topOf(m.Files); top == "" {
		if strings.EqualFold(filepath.Base(dir), safeName(name)) {
			scan = dir
		}
	} else {
		for _, f := range m.Files {
			if strings.Contains(f.Rel, "/") {
				scan = filepath.Join(dir, cleanRel(top))
				break
			}
		}
	}
	if scan != "" && filepath.Dir(scan) == scan {
		scan = "" // a drive root
	}
	var extra []string
	var extraBytes int64
	if scan != "" {
		want := map[string]bool{}
		for _, f := range m.Files {
			want[strings.ToLower(filepath.Join(dir, cleanRel(f.Rel)))] = true
		}
		recvMu.Lock() // files other downloads keep here aren't extra
		for _, o := range recvTasks {
			if o == t {
				continue
			}
			for _, f := range o.Files {
				want[strings.ToLower(filepath.Join(o.Dir, cleanRel(f.Rel)))] = true
			}
		}
		recvMu.Unlock()
		n := 0
		filepath.WalkDir(scan, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if n++; n > 500000 {
				return filepath.SkipAll
			}
			lp := strings.ToLower(p)
			if want[lp] || want[strings.TrimSuffix(lp, ".smpart")] || want[strings.TrimSuffix(lp, ".smpart.json")] {
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			extra = append(extra, p)
			if fi, err := d.Info(); err == nil {
				extraBytes += fi.Size()
			}
			return nil
		})
		sort.Strings(extra)
	}

	verifyMu.Lock()
	verifies[id] = &verifyState{token: token, missing: missing, extra: extra, scan: scan}
	verifyMu.Unlock()

	var missBytes int64
	mi := []verifyItem{}
	for _, f := range missing {
		missBytes += f.Size
		if len(mi) < verifyListMax {
			mi = append(mi, verifyItem{Rel: f.Rel, Size: f.Size, Why: why[f.ID]})
		}
	}
	ex := []verifyItem{}
	for _, p := range extra {
		if len(ex) >= verifyListMax {
			break
		}
		var sz int64
		if st, err := os.Stat(p); err == nil {
			sz = st.Size()
		}
		rel, _ := filepath.Rel(dir, p)
		ex = append(ex, verifyItem{Rel: filepath.ToSlash(rel), Size: sz})
	}
	return map[string]any{
		"dir": dir, "files": len(m.Files), "ok": ok, "okBytes": okBytes,
		"missing": mi, "missingCount": len(missing), "missingBytes": missBytes,
		"extra": ex, "extraCount": len(extra), "extraBytes": extraBytes, "extraScanned": scan != "",
	}, nil
}

func lastVerify(id string) (*verifyState, error) {
	verifyMu.Lock()
	defer verifyMu.Unlock()
	v := verifies[id]
	if v == nil {
		return nil, errors.New("check the files first")
	}
	return v, nil
}

// recvGetMissing adds the missing and changed files from the last check to
// the download and starts it. Files that are there and match stay untouched.
func recvGetMissing(id string) (int, error) {
	t := findRecv(id)
	if t == nil {
		return 0, errors.New("that download is gone")
	}
	v, err := lastVerify(id)
	if err != nil {
		return 0, err
	}
	t.mu.Lock()
	sameLink, dir := t.Token == v.token, t.Dir
	t.mu.Unlock()
	if !sameLink {
		return 0, errors.New("the link changed since the check. Check the files again")
	}
	if len(v.missing) == 0 {
		return 0, nil
	}
	rels := map[string]bool{}
	for _, f := range v.missing {
		rels[f.Rel] = true
	}
	recvMu.Lock()
	for _, o := range recvTasks {
		if o == t {
			continue
		}
		o.mu.Lock()
		clash := o.Dir == dir && o.State != "done"
		o.mu.Unlock()
		if !clash {
			continue
		}
		for _, f := range o.Files {
			if rels[f.Rel] && !o.isDone(f.ID) {
				recvMu.Unlock()
				return 0, errors.New("another download in your list is saving some of these files. Use that one, or cancel it first")
			}
		}
	}
	recvMu.Unlock()

	t.pause()
	t.mu.Lock()
	byID := map[int]RecvFile{}
	for _, f := range v.missing {
		byID[f.ID] = f
	}
	for i, f := range t.Files { // the sender's current size and date
		if nf, ok := byID[f.ID]; ok {
			t.Files[i] = nf
			delete(byID, f.ID)
		}
	}
	for _, f := range v.missing {
		if _, ok := byID[f.ID]; ok {
			t.Files = append(t.Files, f)
		}
	}
	t.Total = 0
	for _, f := range t.Files {
		t.Total += f.Size
	}
	var done []int
	miss := rels2id(v.missing)
	for _, d := range t.Done {
		if !miss[d] {
			done = append(done, d)
		}
	}
	t.Done = done
	t.mu.Unlock()
	recvMu.Lock()
	saveRecvLocked()
	recvMu.Unlock()
	verifyMu.Lock()
	delete(verifies, id)
	verifyMu.Unlock()
	t.start()
	return len(v.missing), nil
}

func rels2id(files []RecvFile) map[int]bool {
	m := map[int]bool{}
	for _, f := range files {
		m[f.ID] = true
	}
	return m
}

// recvRemoveExtra moves the extra files from the last check to the Recycle
// Bin and clears out folders that are left empty.
func recvRemoveExtra(id string) (int, error) {
	v, err := lastVerify(id)
	if err != nil {
		return 0, err
	}
	var paths []string
	for _, p := range v.extra {
		if fileExists(p) {
			paths = append(paths, p)
		}
	}
	gone, err := recycle(paths)
	dirs := map[string]bool{}
	for _, p := range paths {
		for d := filepath.Dir(p); len(d) > len(v.scan) && strings.HasPrefix(strings.ToLower(d), strings.ToLower(v.scan)); d = filepath.Dir(d) {
			dirs[d] = true
		}
	}
	ds := make([]string, 0, len(dirs))
	for d := range dirs {
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return len(ds[i]) > len(ds[j]) }) // deepest first
	for _, d := range ds {
		os.Remove(d) // only succeeds when empty
	}
	verifyMu.Lock()
	if cur := verifies[id]; cur == v {
		cur.extra = nil
	}
	verifyMu.Unlock()
	return gone, err
}

var procSHFileOperation = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

type shFileOpStruct struct {
	hwnd    uintptr
	wFunc   uint32
	pFrom   *uint16
	pTo     *uint16
	fFlags  uint16
	aborted int32
	mapping uintptr
	title   *uint16
}

func shDelete(paths []string) bool {
	var buf []uint16
	for _, p := range paths {
		u, err := windows.UTF16FromString(p)
		if err != nil {
			return false
		}
		buf = append(buf, u...)
	}
	buf = append(buf, 0)
	op := shFileOpStruct{wFunc: 3 /* FO_DELETE */, pFrom: &buf[0],
		fFlags: 0x40 | 0x10 | 0x04 | 0x400 /* ALLOWUNDO|NOCONFIRMATION|SILENT|NOERRORUI */}
	r, _, _ := procSHFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	return r == 0 && op.aborted == 0
}

// recycle moves files to the Recycle Bin, a batch at a time, and reports how
// many are gone.
func recycle(paths []string) (int, error) {
	gone, failed := 0, 0
	for len(paths) > 0 {
		n := len(paths)
		if n > 200 {
			n = 200
		}
		batch := paths[:n]
		paths = paths[n:]
		if !shDelete(batch) {
			for _, p := range batch { // one at a time to find the ones that won't go
				if fileExists(p) {
					shDelete([]string{p})
				}
			}
		}
		for _, p := range batch {
			if fileExists(p) {
				failed++
			} else {
				gone++
			}
		}
	}
	if failed > 0 {
		return gone, fmt.Errorf("%d file(s) couldn't be removed. They may be open in another app", failed)
	}
	return gone, nil
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
