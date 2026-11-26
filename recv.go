package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// In-app receiver for Quick Share links: downloads selected files with several
// parallel ranged connections (like IDM does), resumes after breaks and app
// restarts, and can hand the files to IDM or the browser instead.

const (
	recvChunk   = 4 << 20 // bytes per range request
	recvWorkers = 8       // parallel connections per download
)

type RecvFile struct {
	ID    int    `json:"id"`
	Rel   string `json:"rel"`
	Size  int64  `json:"size"`
	Kind  string `json:"kind"`
	MTime int64  `json:"mtime,omitempty"`
	CTime int64  `json:"ctime,omitempty"`
}

// setNameHeader tells the sender who is downloading ("Connected with ...").
func setNameHeader(req *http.Request) {
	if n := userName(); n != "" {
		req.Header.Set("X-SM-Name", url.QueryEscape(n))
	}
}

type recvManifest struct {
	By    string     `json:"by"` // the sender's name
	Token string     `json:"token"`
	Base  string     `json:"base"`
	Name  string     `json:"name"`
	Total int64      `json:"total"`
	Files []RecvFile `json:"files"`
}

// RecvTask is one in-app download of (part of) a share. Saved in recv.json.
type RecvTask struct {
	ID    string     `json:"id"`
	Token string     `json:"token"`
	Base  string     `json:"base"` // current address of the sender, e.g. https://x.trycloudflare.com
	Name  string     `json:"name"`
	By    string     `json:"by,omitempty"` // the sender's name
	Dir   string     `json:"dir"`
	Files []RecvFile `json:"files"`
	Total int64      `json:"total"`
	Done  []int      `json:"doneFiles"` // ids of finished files
	State string     `json:"state"`     // downloading|paused|done|error
	Err   string     `json:"err"`
	Added time.Time  `json:"added"`

	got    atomic.Int64 // bytes on disk so far
	chkN   atomic.Int64 // files being checked before downloading
	chkOK  atomic.Int64 // ... of which checked so far
	vBusy  atomic.Bool  // Check files is running
	vN     atomic.Int64 // files Check files compares with the sender
	vOK    atomic.Int64 // ... of which compared so far
	meter  Meter
	mu     sync.Mutex
	cancel context.CancelFunc
	runID  int
}

var (
	recvTasks  []*RecvTask
	recvMu     sync.Mutex
	recvCache  = map[string]*recvManifest{} // token -> last fetched manifest
	recvClient = &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialWithDNSFallback(&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}),
			MaxIdleConnsPerHost:   recvWorkers * 2,
			MaxConnsPerHost:       recvWorkers * 2,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       60 * time.Second,
			ForceAttemptHTTP2:     false, // separate TCP connections are what make parallel ranges fast
			ReadBufferSize:        256 << 10,
		},
	}
)

// parseShareURL splits a pasted link into the sender's address and the share token.
func parseShareURL(raw string) (base, token string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", errors.New("that isn't a ShareMile link")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "s" || parts[1] == "" {
		return "", "", errors.New("that isn't a ShareMile link (it should contain /s/…)")
	}
	return u.Scheme + "://" + u.Host, parts[1], nil
}

func fileURL(base, token string, id int) string {
	return base + "/s/" + token + "/f/" + strconv.Itoa(id) + "?dl=1"
}

func recvOpen(raw string) (*recvManifest, error) {
	m, err := fetchManifest(raw)
	if err != nil {
		return nil, err
	}
	applyManifest(m, nil)
	return m, nil
}

const blockedMsg = "the sender's PC is blocking you for a while (too many requests to an old link). It clears within an hour, or right away when they restart ShareMile"

// fetchManifest reads a link's file list.
func fetchManifest(raw string) (*recvManifest, error) {
	base, token, err := parseShareURL(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/s/"+token+"/api", nil)
		setNameHeader(req)
		resp, err = recvClient.Do(req)
		// A brand-new relay address can take a few seconds to go live: retry briefly.
		if err == nil || attempt >= 3 || ctx.Err() != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return nil, errors.New("couldn't reach the sender. Check the link, or ask them for a fresh one (relay links change when their app restarts)")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errors.New("this share has ended, or the link is wrong")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New(blockedMsg)
	case resp.StatusCode != 200:
		return nil, errors.New("the sender answered with " + resp.Status)
	}
	var m recvManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&m); err != nil || len(m.Files) == 0 {
		return nil, errors.New("the link didn't return a file list")
	}
	m.Token, m.Base = token, base
	localRels(len(m.Files), func(i int) string { return m.Files[i].Rel }, func(i int, r string) { m.Files[i].Rel = r })
	return &m, nil
}

// applyManifest remembers a freshly opened link and moves downloads of the same
// files over to it (force: also this download, whatever the share is called).
// Downloads that can continue are started.
func applyManifest(m *recvManifest, force *RecvTask) {
	token, base := m.Token, m.Base
	recvMu.Lock()
	recvCache[token] = m
	// Same share at a new address (e.g. the sender's relay restarted): move
	// unfinished downloads over and continue them.
	var revive []*RecvTask
	for _, t := range recvTasks {
		if t.Token == token && t.Base != base {
			t.mu.Lock()
			t.Base = base
			if t.State == "error" {
				revive = append(revive, t)
			}
			if m.By != "" {
				t.By = m.By
			}
			t.mu.Unlock()
		}
	}
	relinked, drop, orphans := relinkRecvLocked(m, force)
	revive = append(revive, relinked...)
	saveRecvLocked()
	recvMu.Unlock()
	if len(drop) > 0 {
		for _, d := range drop {
			d.pause()
		}
		time.Sleep(300 * time.Millisecond) // let their connections let go of the part files
		for _, p := range orphans {
			os.Remove(p)
			os.Remove(p + ".json")
		}
		recvMu.Lock()
		kept := recvTasks[:0]
		for _, t := range recvTasks {
			gone := false
			for _, d := range drop {
				gone = gone || t == d
			}
			if !gone {
				kept = append(kept, t)
			}
		}
		recvTasks = kept
		saveRecvLocked()
		recvMu.Unlock()
	}
	started := map[*RecvTask]bool{}
	for _, t := range revive {
		if !started[t] {
			started[t] = true
			t.start()
		}
	}
}

// recvRelink points a download at a new link from the sender (they restarted
// and shared the same files again) and continues it.
func recvRelink(id, raw string) error {
	t := findRecv(id)
	if t == nil {
		return errors.New("that download is gone")
	}
	m, err := fetchManifest(raw)
	if err != nil {
		return err
	}
	byRel := map[string]int64{}
	for _, f := range m.Files {
		byRel[f.Rel] = f.Size
	}
	t.mu.Lock()
	miss := 0
	for _, f := range t.Files {
		if sz, ok := byRel[f.Rel]; !ok || sz != f.Size {
			miss++
		}
	}
	n := len(t.Files)
	t.mu.Unlock()
	if miss > 0 {
		return fmt.Errorf("this link doesn't have the same files (%d of %d are missing or changed). Ask the sender to share the same folder again and send that link", miss, n)
	}
	applyManifest(m, t)
	if t.state() != "downloading" {
		t.start()
	}
	return nil
}

func (t *RecvTask) state() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.State
}

// relinkRecvLocked handles a sender who shared the same files again under a new
// link (after a restart the old link is gone). Unfinished downloads of those
// files move to the new link and continue where they stopped, and a newer
// duplicate download of the same files is folded into the older one, so
// finished files are never fetched twice. Returns the downloads to start and the
// duplicates to remove, plus the duplicates' part files of files that are
// already finished. Caller holds recvMu.
func relinkRecvLocked(m *recvManifest, force *RecvTask) (revive, drop []*RecvTask, orphans []string) {
	byRel := map[string]RecvFile{}
	for _, f := range m.Files {
		byRel[f.Rel] = f
	}
	// Downloads of this share (same name, every file still offered at the same size).
	var group []*RecvTask
	moved := false
	for _, t := range recvTasks {
		t.mu.Lock()
		ok := t.State != "done" && (t == force || t.Name == m.Name) && len(t.Files) > 0
		for _, f := range t.Files {
			if g, has := byRel[f.Rel]; !ok || !has || g.Size != f.Size {
				ok = false
				break
			}
		}
		t.mu.Unlock()
		if !ok {
			continue
		}
		group = append(group, t)
		if t.Token != m.Token {
			moved = true
		}
	}
	if !moved {
		return nil, nil, nil // nothing came from an older link
	}
	sort.SliceStable(group, func(i, j int) bool { return group[i].Added.Before(group[j].Added) })
	wasRunning := map[*RecvTask]bool{}
	for _, t := range group {
		t.mu.Lock()
		wasRunning[t] = t.State == "downloading"
		if t.Token != m.Token {
			// Same files, new link: file numbers can differ, so map them by path.
			rel := map[int]string{}
			for i, f := range t.Files {
				rel[f.ID] = f.Rel
				t.Files[i].ID = byRel[f.Rel].ID
			}
			for i, id := range t.Done {
				t.Done[i] = byRel[rel[id]].ID
			}
			t.Token, t.Base = m.Token, m.Base
			if m.By != "" {
				t.By = m.By
			}
		}
		t.mu.Unlock()
	}
	// Fold a later download into an earlier one that covers all of its files.
	merged := map[*RecvTask]bool{}
	for i, d := range group {
		for _, k := range group[:i] {
			if merged[k] || k.Dir != d.Dir {
				continue
			}
			k.mu.Lock()
			d.mu.Lock()
			inK := map[int]bool{}
			for _, f := range k.Files {
				inK[f.ID] = true
			}
			covered := true
			for _, f := range d.Files {
				covered = covered && inK[f.ID]
			}
			if covered {
				have := map[int]bool{}
				for _, id := range k.Done {
					have[id] = true
				}
				inD := map[int]bool{}
				for _, id := range d.Done {
					inD[id] = true
					if !have[id] {
						k.Done = append(k.Done, id)
					}
				}
				for _, f := range d.Files {
					if have[f.ID] && !inD[f.ID] {
						orphans = append(orphans, filepath.Join(d.Dir, cleanRel(f.Rel))+".smpart")
					}
				}
			}
			d.mu.Unlock()
			k.mu.Unlock()
			if covered {
				merged[d] = true
				drop = append(drop, d)
				if wasRunning[d] {
					wasRunning[k] = true
				}
				break
			}
		}
	}
	for _, t := range group {
		if merged[t] {
			continue
		}
		t.mu.Lock()
		st := t.State
		t.mu.Unlock()
		if st == "error" || wasRunning[t] {
			revive = append(revive, t)
		}
	}
	return revive, drop, orphans
}

func pickFiles(m *recvManifest, ids []string) []RecvFile {
	if len(ids) == 0 {
		return m.Files
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []RecvFile
	for _, f := range m.Files {
		if want[strconv.Itoa(f.ID)] {
			out = append(out, f)
		}
	}
	return out
}

// recvRoot is where a share's files are saved. File paths already start with
// the shared folder's name, so a single folder or file goes straight into the
// download folder; several separate items get a folder named after the share.
func recvRoot(m *recvManifest) string {
	cfgMu.Lock()
	root := cfg.DownloadDir
	cfgMu.Unlock()
	return dirIn(root, m.Files, m.Name)
}

func dirIn(root string, files []RecvFile, name string) string {
	if topOf(files) == "" {
		return filepath.Join(root, safeName(name))
	}
	return root
}

// topOf returns the one top folder (or file) all paths share, or "".
func topOf(files []RecvFile) string {
	top := ""
	for _, f := range files {
		t := strings.SplitN(f.Rel, "/", 2)[0]
		if top == "" {
			top = t
		} else if t != top {
			return ""
		}
	}
	return top
}

// saveDirFor works out where a download's files go when the user picks a
// folder, like a torrent client's "set location": choosing the folder that
// holds the shared folder works, and so does choosing the shared folder itself.
func saveDirFor(chosen string, files []RecvFile, name string) string {
	chosen = filepath.Clean(chosen)
	norm := dirIn(chosen, files, name)
	b := filepath.Base(chosen)
	top := topOf(files)
	if (top != "" && strings.EqualFold(b, cleanRel(top))) || (top == "" && strings.EqualFold(b, safeName(name))) {
		if alt := dirIn(filepath.Dir(chosen), files, name); filesPresent(alt, files) > filesPresent(norm, files) {
			return alt
		}
	}
	return norm
}

// filesPresent counts (a sample of) the files that already exist, finished or partial.
func filesPresent(dir string, files []RecvFile) int {
	n := 0
	for i, f := range files {
		if i >= 300 {
			break
		}
		p := filepath.Join(dir, cleanRel(f.Rel))
		if fileExists(p) || fileExists(p+".smpart") {
			n++
		}
	}
	return n
}

func cachedManifest(token string) (*recvManifest, error) {
	recvMu.Lock()
	defer recvMu.Unlock()
	m := recvCache[token]
	if m == nil {
		return nil, errors.New("open the link again first")
	}
	return m, nil
}

func recvStart(token string, ids []string, chosen string) (*RecvTask, error) {
	m, err := cachedManifest(token)
	if err != nil {
		return nil, err
	}
	files := pickFiles(m, ids)
	if len(files) == 0 {
		return nil, errors.New("select at least one file")
	}
	// Two unfinished downloads of the same file would write the same part file
	// (also across links: the sender may have shared the same files again).
	dir := recvRoot(m)
	if chosen != "" {
		dir = saveDirFor(chosen, files, m.Name)
	}
	recvMu.Lock()
	for _, o := range recvTasks {
		o.mu.Lock()
		busy := o.Dir == dir && o.State != "done"
		o.mu.Unlock()
		if !busy {
			continue
		}
		for _, f := range files {
			for _, g := range o.Files {
				if f.Rel == g.Rel && !o.isDone(g.ID) {
					recvMu.Unlock()
					return nil, errors.New("some of these files are already in your downloads list. Resume that download, or cancel it first")
				}
			}
		}
	}
	recvMu.Unlock()
	t := &RecvTask{ID: randToken(6), Token: token, Base: m.Base, Name: m.Name, By: m.By, Dir: dir,
		Files: files, State: "downloading", Added: time.Now()}
	for _, f := range files {
		t.Total += f.Size
	}
	recvMu.Lock()
	recvTasks = append(recvTasks, t)
	saveRecvLocked()
	recvMu.Unlock()
	t.start()
	return t, nil
}

func findRecv(id string) *RecvTask {
	recvMu.Lock()
	defer recvMu.Unlock()
	for _, t := range recvTasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (t *RecvTask) set(state, e string) {
	t.mu.Lock()
	t.State, t.Err = state, e
	t.mu.Unlock()
	recvMu.Lock()
	saveRecvLocked()
	recvMu.Unlock()
}

func (t *RecvTask) start() {
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.runID++
	run := t.runID
	t.State, t.Err = "downloading", ""
	t.mu.Unlock()
	go func() {
		err := t.run(ctx)
		t.mu.Lock()
		current := t.runID == run
		t.mu.Unlock()
		if !current {
			return // paused/restarted meanwhile
		}
		switch {
		case err == nil:
			t.set("done", "")
		case ctx.Err() != nil:
			t.set("paused", "")
		default:
			t.set("error", err.Error())
		}
	}()
}

func (t *RecvTask) pause() {
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.runID++
	if t.State == "downloading" {
		t.State = "paused"
	}
	t.mu.Unlock()
	recvMu.Lock()
	saveRecvLocked()
	recvMu.Unlock()
}

// partState is the resume record kept next to an unfinished file.
type partState struct {
	Size  int64  `json:"size"`
	Chunk int64  `json:"chunk"`
	Done  []bool `json:"done"`
}

type recvJob struct {
	f    *recvFileRun
	idx  int
	from int64
	to   int64 // inclusive
}

type recvFileRun struct {
	t       *RecvTask
	rf      RecvFile
	final   string
	part    string
	fh      *os.File
	mu      sync.Mutex
	st      partState
	left    int
	lastSav time.Time
	replace bool // a different file with this name is in the way: overwrite it
}

func (fr *recvFileRun) save(force bool) {
	if !force && time.Since(fr.lastSav) < time.Second {
		return
	}
	fr.lastSav = time.Now()
	b, _ := json.Marshal(fr.st)
	os.WriteFile(fr.part+".json", b, 0o644)
}

func (t *RecvTask) isDone(id int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range t.Done {
		if d == id {
			return true
		}
	}
	return false
}

func (t *RecvTask) run(ctx context.Context) error {
	t.mu.Lock()
	base, token, dir := t.Base, t.Token, t.Dir
	t.mu.Unlock()

	replace, err := t.checkExisting(ctx, base, token, dir)
	if err != nil {
		return err
	}

	// Prepare every file: open/resume its .smpart and queue the missing chunks.
	var runs []*recvFileRun
	var got int64
	var jobs []recvJob
	for _, f := range t.Files {
		if t.isDone(f.ID) {
			got += f.Size
			continue
		}
		final := filepath.Join(dir, cleanRel(f.Rel))
		part := final + ".smpart"
		if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
			return err
		}
		fr := &recvFileRun{t: t, rf: f, final: final, part: part, replace: replace[f.ID]}
		if b, err := os.ReadFile(part + ".json"); err == nil {
			json.Unmarshal(b, &fr.st)
		}
		n := int((f.Size + recvChunk - 1) / recvChunk)
		if fr.st.Size != f.Size || fr.st.Chunk != recvChunk || len(fr.st.Done) != n || !fileExists(part) {
			fr.st = partState{Size: f.Size, Chunk: recvChunk, Done: make([]bool, n)}
		}
		fh, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		// Sparse: parallel connections write out of order, and without this
		// NTFS zero-fills every gap first, doubling the disk writes.
		setSparse(fh)
		if err := fh.Truncate(f.Size); err != nil {
			fh.Close()
			return err
		}
		fr.fh = fh
		for i, d := range fr.st.Done {
			if d {
				got += min64(recvChunk, f.Size-int64(i)*recvChunk)
				continue
			}
			from := int64(i) * recvChunk
			jobs = append(jobs, recvJob{fr, i, from, min64(from+recvChunk, f.Size) - 1})
			fr.left++
		}
		runs = append(runs, fr)
	}
	t.got.Store(got)
	defer func() {
		for _, fr := range runs {
			fr.mu.Lock()
			if fr.fh != nil {
				fr.fh.Close()
				fr.fh = nil
				fr.save(true)
			}
			fr.mu.Unlock()
		}
	}()
	for _, fr := range runs {
		if fr.left == 0 { // empty or already complete
			if err := fr.finish(); err != nil {
				return err
			}
		}
	}

	// Small files first so they complete quickly; big ones fill the pipes.
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].f.rf.Size < jobs[j].f.rf.Size })
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	q := make(chan recvJob)
	var firstErr error
	var errOnce sync.Once
	var wg sync.WaitGroup
	for w := 0; w < recvWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range q {
				if err := t.fetch(ctx, base, token, j); err != nil {
					errOnce.Do(func() { firstErr = err; cancel() })
					return
				}
			}
		}()
	}
feed:
	for _, j := range jobs {
		select {
		case q <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(q)
	wg.Wait()
	if firstErr != nil && ctx.Err() != nil && !errors.Is(firstErr, context.Canceled) {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return firstErr
}

// checkExisting looks at files already in the download folder that have no
// part file: finished earlier, by another link or with another app. A file
// with the sender's size and date (ShareMile keeps the sender's dates) counts
// as finished. One with the right size but another date is compared with the
// sender at three spots (start, middle, end), which takes a moment even for
// huge files. Anything else is downloaded again and replaces the file.
func (t *RecvTask) checkExisting(ctx context.Context, base, token, dir string) (map[int]bool, error) {
	replace := map[int]bool{}
	var sample []RecvFile
	var done []int
	for _, f := range t.Files {
		if t.isDone(f.ID) {
			continue
		}
		final := filepath.Join(dir, cleanRel(f.Rel))
		if fileExists(final + ".smpart") {
			continue
		}
		st, err := os.Stat(final)
		if err != nil || st.IsDir() {
			continue
		}
		d := st.ModTime().UnixMilli() - f.MTime
		switch {
		case st.Size() != f.Size:
			replace[f.ID] = true
		case f.MTime > 0 && d > -2000 && d < 2000:
			done = append(done, f.ID)
		default:
			sample = append(sample, f)
		}
	}
	if len(sample) > 0 {
		t.chkN.Store(int64(len(sample)))
		t.chkOK.Store(0)
		defer t.chkN.Store(0)
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
						}
					case same:
						done = append(done, f.ID)
					default:
						replace[f.ID] = true
					}
					mu.Unlock()
					t.chkOK.Add(1)
				}
			}()
		}
		for _, f := range sample {
			mu.Lock()
			stop := firstErr != nil
			mu.Unlock()
			if stop || ctx.Err() != nil {
				break
			}
			q <- f
		}
		close(q)
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}
	if len(done) > 0 {
		t.mu.Lock()
		t.Done = append(t.Done, done...)
		t.mu.Unlock()
		recvMu.Lock()
		saveRecvLocked()
		recvMu.Unlock()
	}
	return replace, nil
}

// sameAsSender compares three 64 KB spots of a local file with the sender's copy.
func sameAsSender(ctx context.Context, base, token string, f RecvFile, path string) (bool, error) {
	if f.Size == 0 {
		return true, nil
	}
	fh, err := os.Open(path)
	if err != nil {
		return false, nil
	}
	defer fh.Close()
	n := min64(64<<10, f.Size)
	for _, off := range []int64{0, (f.Size - n) / 2, f.Size - n} {
		local := make([]byte, n)
		if _, err := fh.ReadAt(local, off); err != nil {
			return false, nil
		}
		var remote []byte
		var last error
		for attempt := 0; attempt < 4; attempt++ {
			if attempt > 0 {
				select {
				case <-time.After(time.Duration(attempt) * time.Second):
				case <-ctx.Done():
					return false, ctx.Err()
				}
			}
			remote, last = fetchRange(ctx, base, token, f, off, n)
			var p permanentErr
			if last == nil || errors.As(last, &p) || ctx.Err() != nil {
				break
			}
		}
		if last != nil {
			var p permanentErr
			if errors.As(last, &p) {
				return false, p.error
			}
			return false, fmt.Errorf("couldn't check the files already in the folder (%v)", last)
		}
		if !bytes.Equal(local, remote) {
			return false, nil
		}
	}
	return true, nil
}

func fetchRange(ctx context.Context, base, token string, f RecvFile, off, n int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fileURL(base, token, f.ID), nil)
	setNameHeader(req)
	req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(off+n-1, 10))
	req.Header.Set("User-Agent", "ShareMile/"+appVersion)
	resp, err := recvClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := statusErr(resp.StatusCode, off == 0 && n == f.Size); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, n))
	if err == nil && int64(len(b)) != n {
		err = io.ErrUnexpectedEOF
	}
	return b, err
}

// statusErr turns a sender's answer to a range request into an error.
func statusErr(code int, whole bool) error {
	switch code {
	case http.StatusPartialContent:
		return nil
	case http.StatusOK:
		if !whole {
			return permanentErr{errors.New("the sender doesn't support resumable downloads")}
		}
		return nil
	case http.StatusNotFound:
		return permanentErr{errors.New("the sender stopped sharing these files. If they shared them again, choose New download link in the ⋮ menu and paste their new link")}
	case http.StatusTooManyRequests:
		return permanentErr{errors.New(blockedMsg)}
	case http.StatusRequestedRangeNotSatisfiable:
		return permanentErr{errors.New("the file changed on the sender's PC. Remove this download and start again")}
	}
	return errors.New(strconv.Itoa(code) + " " + http.StatusText(code))
}

// recvRelocate points a download at another folder (where its files already
// are, for example), checks what's there and continues.
func recvRelocate(id, chosen string) (string, error) {
	t := findRecv(id)
	if t == nil {
		return "", errors.New("that download is gone")
	}
	t.mu.Lock()
	dir := saveDirFor(chosen, t.Files, t.Name)
	rels := map[string]bool{}
	for _, f := range t.Files {
		rels[f.Rel] = true
	}
	t.mu.Unlock()
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
				return "", errors.New("another download in your list is saving these files in that folder. Use that one, or cancel it first")
			}
		}
	}
	recvMu.Unlock()
	t.pause()
	t.mu.Lock()
	t.Dir = dir
	t.Done = nil // checked again in the new folder
	t.mu.Unlock()
	t.start()
	return dir, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// finish renames a completed .smpart to its real name.
func (fr *recvFileRun) finish() error {
	fr.mu.Lock()
	if fr.fh != nil {
		fr.fh.Close()
		fr.fh = nil
	}
	fr.mu.Unlock()
	final := fr.final
	if fileExists(final) && !fr.replace {
		final = uniquePath(final)
	}
	if err := os.Rename(fr.part, final); err != nil {
		return err
	}
	os.Remove(fr.part + ".json")
	setFileTimes(final, fr.rf.MTime, fr.rf.CTime) // keep the sender's dates
	t := fr.t
	t.mu.Lock()
	t.Done = append(t.Done, fr.rf.ID)
	t.mu.Unlock()
	recvMu.Lock()
	saveRecvLocked()
	recvMu.Unlock()
	return nil
}

type permanentErr struct{ error }

// fetch downloads one chunk, retrying network hiccups with backoff.
func (t *RecvTask) fetch(ctx context.Context, base, token string, j recvJob) error {
	var last error
	for attempt := 0; attempt < 7; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(1<<min(attempt-1, 4)) * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			// The link may have been refreshed meanwhile (new relay address).
			t.mu.Lock()
			base = t.Base
			t.mu.Unlock()
		}
		err := t.fetchOnce(ctx, base, token, j)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var p permanentErr
		if errors.As(err, &p) {
			return p.error
		}
		last = err
	}
	return fmt.Errorf("the connection to the sender keeps failing (%v). If they restarted ShareMile, paste their new link on Receive and the download continues", last)
}

func (t *RecvTask) fetchOnce(ctx context.Context, base, token string, j recvJob) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", fileURL(base, token, j.f.rf.ID), nil)
	setNameHeader(req)
	req.Header.Set("Range", "bytes="+strconv.FormatInt(j.from, 10)+"-"+strconv.FormatInt(j.to, 10))
	req.Header.Set("User-Agent", "ShareMile/"+appVersion)
	resp, err := recvClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := statusErr(resp.StatusCode, j.from == 0 && j.to == j.f.rf.Size-1); err != nil {
		return err
	}
	want := j.to - j.from + 1
	// Stall watchdog: if no data arrives for 45 s, drop the connection and retry.
	stall := time.AfterFunc(45*time.Second, cancel)
	defer stall.Stop()
	buf := make([]byte, 256<<10)
	var n int64
	t.meter.active.Add(1)
	defer t.meter.active.Add(-1)
	for n < want {
		k, rerr := resp.Body.Read(buf[:min64(int64(len(buf)), want-n)])
		if k > 0 {
			stall.Reset(45 * time.Second)
			j.f.mu.Lock()
			fh := j.f.fh
			var werr error
			if fh == nil {
				werr = context.Canceled
			} else {
				_, werr = fh.WriteAt(buf[:k], j.from+n)
			}
			j.f.mu.Unlock()
			if werr != nil {
				t.got.Add(-n)
				return permanentErr{werr}
			}
			n += int64(k)
			t.got.Add(int64(k))
			t.meter.bytes.Add(int64(k))
		}
		if rerr != nil {
			if rerr == io.EOF && n == want {
				break
			}
			t.got.Add(-n) // this chunk will be fetched again from its start
			if rerr == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return rerr
		}
	}
	fr := j.f
	fr.mu.Lock()
	fr.st.Done[j.idx] = true
	fr.left--
	left := fr.left
	fr.save(left == 0)
	fr.mu.Unlock()
	if left == 0 {
		return fr.finish()
	}
	return nil
}

// ---- persistence -------------------------------------------------------------

func recvFile() string { return filepath.Join(appDataDir(), "recv.json") }

func saveRecvLocked() {
	b, _ := json.MarshalIndent(recvTasks, "", " ")
	os.WriteFile(recvFile(), b, 0o644)
}

func loadRecv() {
	b, err := os.ReadFile(recvFile())
	if err != nil {
		return
	}
	var ts []*RecvTask
	if json.Unmarshal(b, &ts) != nil {
		return
	}
	for _, t := range ts {
		if t.State == "downloading" {
			t.State = "paused" // resumes with one click; the sender may not be online yet
		}
		done := int64(0)
		for _, f := range t.Files {
			if t.isDone(f.ID) {
				done += f.Size
				continue
			}
			// count the chunks already on disk so progress shows correctly
			var st partState
			if b, err := os.ReadFile(filepath.Join(t.Dir, cleanRel(f.Rel)) + ".smpart.json"); err == nil && json.Unmarshal(b, &st) == nil && st.Size == f.Size {
				for i, d := range st.Done {
					if d {
						done += min64(st.Chunk, f.Size-int64(i)*st.Chunk)
					}
				}
			}
		}
		t.got.Store(done)
	}
	recvMu.Lock()
	recvTasks = ts
	recvMu.Unlock()
}

func recvBusy() bool {
	recvMu.Lock()
	defer recvMu.Unlock()
	for _, t := range recvTasks {
		t.mu.Lock()
		s := t.State
		t.mu.Unlock()
		if s == "downloading" {
			return true
		}
	}
	return false
}

func removeRecv(id string, deleteParts bool) {
	t := findRecv(id)
	if t == nil {
		return
	}
	t.pause()
	if deleteParts {
		// Part files another download still uses (the same files under another link) stay.
		inUse := map[string]bool{}
		recvMu.Lock()
		for _, o := range recvTasks {
			if o == t {
				continue
			}
			for _, f := range o.Files {
				if !o.isDone(f.ID) {
					inUse[filepath.Join(o.Dir, cleanRel(f.Rel))] = true
				}
			}
		}
		recvMu.Unlock()
		for _, f := range t.Files {
			if !t.isDone(f.ID) && !inUse[filepath.Join(t.Dir, cleanRel(f.Rel))] {
				p := filepath.Join(t.Dir, cleanRel(f.Rel)) + ".smpart"
				os.Remove(p)
				os.Remove(p + ".json")
			}
		}
	}
	recvMu.Lock()
	for i, x := range recvTasks {
		if x == t {
			recvTasks = append(recvTasks[:i], recvTasks[i+1:]...)
			break
		}
	}
	saveRecvLocked()
	recvMu.Unlock()
}

func recvView() []map[string]any {
	recvMu.Lock()
	ts := append([]*RecvTask(nil), recvTasks...)
	recvMu.Unlock()
	out := []map[string]any{}
	for i := len(ts) - 1; i >= 0; i-- { // newest first
		t := ts[i]
		t.mu.Lock()
		v := map[string]any{"id": t.ID, "token": t.Token, "name": t.Name, "by": t.By, "dir": t.Dir, "count": len(t.Files), "doneCount": len(t.Done),
			"total": t.Total, "got": t.got.Load(), "chkN": t.chkN.Load(), "chkOK": t.chkOK.Load(), "vN": t.vN.Load(), "vOK": t.vOK.Load(), "vBusy": t.vBusy.Load(), "state": t.State, "err": t.Err, "speed": t.meter.Speed()}
		t.mu.Unlock()
		out = append(out, v)
	}
	return out
}

// ---- IDM and browser hand-off -------------------------------------------------

// recvIDM hands each selected file to IDM as a normal download (keeping
// folders). IDM shows its usual "Download File Info" window, so the user can
// start or cancel it there; nothing is put in IDM's queue, so a cancelled
// download is never restarted by the queue.
func recvIDM(token string, ids []string) (int, error) {
	exe := idmPath()
	if exe == "" {
		return 0, errors.New("IDM (Internet Download Manager) isn't installed")
	}
	m, err := cachedManifest(token)
	if err != nil {
		return 0, err
	}
	files := pickFiles(m, ids)
	root := recvRoot(m)
	if len(files) > 20 {
		return 0, errors.New("IDM opens one window per file. Select 20 files or fewer, or use Download (it's as fast as IDM)")
	}
	for _, f := range files {
		dest := filepath.Join(root, cleanRel(f.Rel))
		os.MkdirAll(filepath.Dir(dest), 0o755)
		// No /n (silent) and no /a (queue): IDM shows its normal download window.
		c := exec.Command(exe, "/d", fileURL(m.Base, token, f.ID), "/p", filepath.Dir(dest), "/f", filepath.Base(dest))
		if err := c.Start(); err != nil {
			return 0, errors.New("couldn't open IDM: " + err.Error())
		}
		go c.Wait()
		time.Sleep(150 * time.Millisecond) // keep IDM's windows in order
	}
	return len(files), nil
}

// recvBrowser opens one file as a direct download, or the share page for several.
func recvBrowser(token string, ids []string) error {
	m, err := cachedManifest(token)
	if err != nil {
		return err
	}
	files := pickFiles(m, ids)
	if len(files) == 1 {
		return openURL(fileURL(m.Base, token, files[0].ID))
	}
	return openURL(m.Base + "/s/" + token + "/")
}
