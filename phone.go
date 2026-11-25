package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

// "Receive from phone": the app shows a QR code for /p/<token>/. A phone (or
// any browser) opens it and sends files to this PC. Files are sent in chunks
// so big videos get through the relay (Cloudflare caps one request at 100 MB)
// and a dropped connection resumes where it stopped, even after the phone
// reloads the page or the app restarts.

const phoneChunkMax = 64 << 20 // the page sends 16 MB; anything far bigger is refused

var (
	phoneMu   sync.Mutex
	phoneUps  = map[string]*phoneUp{} // upload id -> live upload
	phoneGot  []phoneFile             // received this session, newest last
	phoneUIDs = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
)

type phoneUp struct {
	mu     sync.Mutex
	Name   string
	Size   int64
	Have   atomic.Int64
	Device string
	Start  time.Time
	Seen   atomic.Int64 // unix ms
	first  int64        // bytes already there when this session first saw it (for speed)
}

type phoneFile struct {
	Name   string    `json:"name"`
	Path   string    `json:"path"`
	Size   int64     `json:"size"`
	Device string    `json:"device"`
	At     time.Time `json:"at"`
}

func phoneDir() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return filepath.Join(cfg.DownloadDir, "From phone")
}

func phoneOn() (bool, string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.PhoneOn && cfg.PhoneToken != "", cfg.PhoneToken
}

// setPhone turns receiving on or off. renew makes a new link (the old QR stops working).
func setPhone(on, renew bool) {
	cfgMu.Lock()
	if cfg.PhoneToken == "" || renew {
		cfg.PhoneToken = randToken(12)
	}
	cfg.PhoneOn = on
	saveConfigLocked()
	cfgMu.Unlock()
	go syncExposure()
}

// deviceName makes a short label from the browser's user agent.
func deviceName(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android"):
		// "Linux; Android 14; SM-S918B Build/..." -> "SM-S918B"
		if i := strings.Index(ua, "Android"); i >= 0 {
			rest := ua[i:]
			if j := strings.Index(rest, ")"); j > 0 {
				f := strings.Split(rest[:j], ";")
				if len(f) >= 2 {
					m := strings.TrimSpace(f[len(f)-1])
					if k := strings.Index(m, " Build"); k >= 0 {
						m = m[:k]
					}
					if m != "" && m != "K" && !strings.HasPrefix(m, "Android") && !strings.HasPrefix(m, "wv") {
						return m
					}
				}
			}
		}
		return "Android phone"
	case strings.Contains(ua, "Macintosh"):
		return "Mac"
	case strings.Contains(ua, "Windows"):
		return "Windows PC"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	}
	return "Browser"
}

func handlePhone(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/p/"), "/")
	on, tok := phoneOn()
	if tok == "" || parts[0] != tok {
		addStrike(r, "p/"+parts[0])
		phoneGone(w, "This link is wrong or old", "Ask for the QR code again: in ShareMile on the PC, open Receive and press Receive from phone.")
		return
	}
	if !on {
		phoneGone(w, "Receiving is off on the PC", "In ShareMile on the PC, open Receive and press Receive from phone, then scan the QR code again.")
		return
	}
	if len(parts) == 1 {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusFound)
		return
	}
	switch parts[1] {
	case "":
		b, _ := webFS.ReadFile("web/phone.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(appVersion)))
	case "api":
		writeJSON(w, map[string]any{"pc": pcName(), "app": appName, "version": appVersion, "chunk": 16 << 20})
	case "up":
		phoneUpload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func phoneGone(w http.ResponseWriter, title, msg string) {
	b, _ := webFS.ReadFile("web/gone.html")
	b = bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(appVersion))
	b = bytes.Replace(b, []byte("This share has ended</h1>"), []byte(htmlEsc(title)+"</h1>"), 1)
	if i, j := bytes.Index(b, []byte(`<p class="muted">`)), bytes.Index(b, []byte("</p>")); i >= 0 && j > i {
		b = append(append(append([]byte{}, b[:i]...), []byte(`<p class="muted">`+htmlEsc(msg))...), b[j:]...)
	}
	b = bytes.Replace(b, []byte("Share ended ·"), []byte("Send to PC ·"), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	w.Write(b)
}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func pcName() string {
	if n := userName(); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "this PC"
}

func phoneErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// phoneUpload takes one chunk: POST up?u=<id>&n=<name>&s=<size>&o=<offset>&m=<mtime ms>.
// The chunk is appended only when the offset is where the file ends now;
// otherwise the answer says how much is there so the page continues from it.
// GET up?u=<id>&s=<size> just tells how much is there.
func phoneUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uid := q.Get("u")
	size, err1 := strconv.ParseInt(q.Get("s"), 10, 64)
	if !phoneUIDs.MatchString(uid) || err1 != nil || size < 0 {
		phoneErr(w, http.StatusBadRequest, "bad upload")
		return
	}
	dir := phoneDir()
	part := filepath.Join(dir, ".sm-"+uid+".part")
	if r.Method == http.MethodGet {
		var have int64
		if st, err := os.Stat(part); err == nil {
			have = st.Size()
		}
		writeJSON(w, map[string]any{"have": have})
		return
	}
	if r.Method != http.MethodPost {
		phoneErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	off, err2 := strconv.ParseInt(q.Get("o"), 10, 64)
	name := safeName(filepath.Base(strings.ReplaceAll(q.Get("n"), `\`, "/")))
	if err2 != nil || off < 0 || off > size || name == "" {
		phoneErr(w, http.StatusBadRequest, "bad upload")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		phoneErr(w, http.StatusInternalServerError, "The PC can't save files in "+dir)
		return
	}

	phoneMu.Lock()
	u := phoneUps[uid]
	if u == nil {
		if len(phoneUps) > 500 { // keep memory bounded: forget finished or idle ones
			for k, v := range phoneUps {
				if time.Since(time.UnixMilli(v.Seen.Load())) > time.Hour {
					delete(phoneUps, k)
				}
			}
		}
		u = &phoneUp{Name: name, Size: size, Device: deviceName(r.UserAgent()), Start: time.Now(), first: -1}
		phoneUps[uid] = u
	}
	phoneMu.Unlock()

	u.mu.Lock() // one writer per file
	defer u.mu.Unlock()
	u.Seen.Store(time.Now().UnixMilli())
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		phoneErr(w, http.StatusInternalServerError, "The PC can't save "+name)
		return
	}
	st, _ := f.Stat()
	have := st.Size()
	if u.first < 0 {
		u.first = have
	}
	u.Have.Store(have)
	if have != off || have > size {
		f.Close()
		if have > size { // a different file reused the id: start over
			os.Remove(part)
			have = 0
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]any{"have": have})
		return
	}
	if off == 0 && size > 0 {
		if free, ok := freeBytes(dir); ok && free < uint64(size)+(64<<20) {
			f.Close()
			os.Remove(part)
			phoneErr(w, http.StatusInsufficientStorage, "The PC doesn't have enough free space for "+name)
			return
		}
	}
	f.Seek(have, io.SeekStart)
	body := http.MaxBytesReader(w, r.Body, phoneChunkMax)
	bp := bufPool.Get().(*[]byte)
	n, err := io.CopyBuffer(phoneCounter{f, u, size}, body, *bp)
	bufPool.Put(bp)
	have += n
	if have > size { // more than the file's size: drop the extra
		f.Truncate(size)
		have = size
	}
	f.Close()
	u.Have.Store(have)
	if err != nil {
		phoneErr(w, http.StatusBadRequest, "upload interrupted")
		return
	}
	if have < size {
		writeJSON(w, map[string]any{"have": have})
		return
	}
	final := uniquePath(filepath.Join(dir, name))
	if err := os.Rename(part, final); err != nil {
		phoneErr(w, http.StatusInternalServerError, "The PC couldn't finish saving "+name)
		return
	}
	if ms, err := strconv.ParseInt(q.Get("m"), 10, 64); err == nil && ms > 0 {
		t := time.UnixMilli(ms)
		os.Chtimes(final, t, t)
	}
	phoneMu.Lock()
	delete(phoneUps, uid)
	phoneGot = append(phoneGot, phoneFile{Name: filepath.Base(final), Path: final, Size: size, Device: u.Device, At: time.Now()})
	if len(phoneGot) > 200 {
		phoneGot = append([]phoneFile(nil), phoneGot[len(phoneGot)-200:]...)
	}
	phoneMu.Unlock()
	writeJSON(w, map[string]any{"have": have, "done": true, "name": filepath.Base(final)})
}

// phoneCounter writes the chunk and keeps the live progress current.
type phoneCounter struct {
	f    *os.File
	u    *phoneUp
	size int64
}

func (c phoneCounter) Write(p []byte) (int, error) {
	n, err := c.f.Write(p)
	c.u.Have.Add(int64(n))
	c.u.Seen.Store(time.Now().UnixMilli())
	return n, err
}

func freeBytes(dir string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free uint64
	if windows.GetDiskFreeSpaceEx(p, &free, nil, nil) != nil {
		return 0, false
	}
	return free, true
}

// phoneView is the inbox for the app: uploads in progress and files received.
func phoneView() map[string]any {
	on, _ := phoneOn()
	dir := phoneDir()
	phoneMu.Lock()
	defer phoneMu.Unlock()
	act := []map[string]any{}
	for _, u := range phoneUps {
		if time.Since(time.UnixMilli(u.Seen.Load())) > 30*time.Second {
			continue
		}
		speed := float64(0)
		if el := time.Since(u.Start).Seconds(); el > 1 && u.first >= 0 {
			speed = float64(u.Have.Load()-u.first) / el
		}
		act = append(act, map[string]any{"name": u.Name, "size": u.Size, "have": u.Have.Load(), "device": u.Device, "speed": speed, "start": u.Start.UnixMilli()})
	}
	sort.Slice(act, func(i, j int) bool { return act[i]["start"].(int64) < act[j]["start"].(int64) })
	got := make([]phoneFile, 0, len(phoneGot))
	for i := len(phoneGot) - 1; i >= 0; i-- {
		got = append(got, phoneGot[i])
	}
	return map[string]any{"on": on, "dir": dir, "active": act, "received": got}
}

// phoneState adds the links (relay first) to the inbox view.
func phoneState(mkLinks func(string) ([]linkOut, string)) map[string]any {
	v := phoneView()
	if _, tok := phoneOn(); tok != "" && v["on"] == true {
		v["links"], v["best"] = mkLinks("/p/" + tok + "/")
	}
	return v
}
