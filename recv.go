package main

import (
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

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
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

type recvManifest struct {
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
	Dir   string     `json:"dir"`
	Files []RecvFile `json:"files"`
	Total int64      `json:"total"`
	Done  []int      `json:"doneFiles"` // ids of finished files
	State string     `json:"state"`     // downloading|paused|done|error
	Err   string     `json:"err"`
	Added time.Time  `json:"added"`

	got    atomic.Int64 // bytes on disk so far
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
	base, token, err := parseShareURL(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/s/"+token+"/api", nil)
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
		return nil, errors.New("the sender's PC is temporarily blocking this connection. Try again later")
	case resp.StatusCode != 200:
		return nil, errors.New("the sender answered with " + resp.Status)
	}
	var m recvManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&m); err != nil || len(m.Files) == 0 {
		return nil, errors.New("the link didn't return a file list")
	}
	m.Token, m.Base = token, base
	recvMu.Lock()
	recvCache[token] = &m
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
			t.mu.Unlock()
		}
	}
	saveRecvLocked()
	recvMu.Unlock()
	for _, t := range revive {
		t.start()
	}
	return &m, nil
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
	top := ""
	for _, f := range m.Files {
		t := strings.SplitN(f.Rel, "/", 2)[0]
		if top == "" {
			top = t
		} else if t != top {
			return filepath.Join(root, safeName(m.Name))
		}
	}
	return root
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

func recvStart(token string, ids []string) (*RecvTask, error) {
	m, err := cachedManifest(token)
	if err != nil {
		return nil, err
	}
	files := pickFiles(m, ids)
	if len(files) == 0 {
		return nil, errors.New("select at least one file")
	}
	// Two unfinished downloads of the same file would write the same part file.
	recvMu.Lock()
	for _, o := range recvTasks {
		o.mu.Lock()
		busy := o.Token == token && o.State != "done"
		o.mu.Unlock()
		if !busy {
			continue
		}
		for _, f := range files {
			for _, g := range o.Files {
				if f.ID == g.ID && !o.isDone(g.ID) {
					recvMu.Unlock()
					return nil, errors.New("some of these files are already in your downloads list. Resume that download, or cancel it first")
				}
			}
		}
	}
	recvMu.Unlock()
	t := &RecvTask{ID: randToken(6), Token: token, Base: m.Base, Name: m.Name, Dir: recvRoot(m),
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
		fr := &recvFileRun{t: t, rf: f, final: final, part: part}
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
		var ret uint32
		windows.DeviceIoControl(windows.Handle(fh.Fd()), 0x000900c4 /* FSCTL_SET_SPARSE */, nil, 0, nil, 0, &ret, nil)
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
	if fileExists(final) {
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
	req.Header.Set("Range", "bytes="+strconv.FormatInt(j.from, 10)+"-"+strconv.FormatInt(j.to, 10))
	req.Header.Set("User-Agent", "ShareMile/"+appVersion)
	resp, err := recvClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		if j.from != 0 || j.to != j.f.rf.Size-1 {
			return permanentErr{errors.New("the sender doesn't support resumable downloads")}
		}
	case http.StatusNotFound:
		return permanentErr{errors.New("the sender stopped sharing these files")}
	case http.StatusRequestedRangeNotSatisfiable:
		return permanentErr{errors.New("the file changed on the sender's PC. Remove this download and start again")}
	default:
		return errors.New(resp.Status)
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
		for _, f := range t.Files {
			if !t.isDone(f.ID) {
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
		v := map[string]any{"id": t.ID, "token": t.Token, "name": t.Name, "dir": t.Dir, "count": len(t.Files), "doneCount": len(t.Done),
			"total": t.Total, "got": t.got.Load(), "state": t.State, "err": t.Err, "speed": t.meter.Speed()}
		t.mu.Unlock()
		out = append(out, v)
	}
	return out
}

// ---- IDM and browser hand-off -------------------------------------------------

func idmPath() string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		if k, err := registry.OpenKey(root, `Software\DownloadManager`, registry.QUERY_VALUE); err == nil {
			p, _, err := k.GetStringValue("ExePath")
			k.Close()
			if err == nil && fileExists(p) {
				return p
			}
		}
	}
	for _, p := range []string{
		os.Getenv("ProgramFiles(x86)") + `\Internet Download Manager\IDMan.exe`,
		os.Getenv("ProgramFiles") + `\Internet Download Manager\IDMan.exe`,
	} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

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
