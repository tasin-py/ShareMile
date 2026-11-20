package main

import (
	"archive/zip"
	"bytes"
	"container/list"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

//go:embed web
var webFS embed.FS

//go:embed assets/favicon.png assets/icon.png
var assetFS embed.FS

var (
	publicSrv  *http.Server
	publicErr  string
	publicPort int
	publicMu   sync.Mutex
	bufPool    = sync.Pool{New: func() any { b := make([]byte, 1<<20); return &b }}
	thumbSem   = make(chan struct{}, 2)
	thumbCache = newLRU(256)
)

// bigBufListener enlarges socket buffers so single connections reach line rate.
type bigBufListener struct{ net.Listener }

func (l bigBufListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetWriteBuffer(4 << 20)
		tc.SetReadBuffer(4 << 20)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}
	return c, err
}

func startPublicServer(port int) error {
	publicMu.Lock()
	defer publicMu.Unlock()
	if publicSrv != nil {
		publicSrv.Close()
		publicSrv = nil
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		publicErr = fmt.Sprintf("Port %d is busy or blocked. Pick another port in Settings.", port)
		return err
	}
	publicErr = ""
	publicPort = port
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleLanding)
	mux.HandleFunc("/s/", handleShare)
	mux.HandleFunc("/n/", handleNetwork)
	mux.HandleFunc("/assets/", handleAssets)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		b, _ := assetFS.ReadFile("assets/favicon.png")
		w.Header().Set("Content-Type", "image/png")
		w.Write(b)
	})
	srv := &http.Server{Handler: secHeaders(mux), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	publicSrv = srv
	go srv.Serve(bigBufListener{ln})
	return nil
}

func secHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBanned(r) {
			http.Error(w, "Too many wrong links. Try again later.", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		h.ServeHTTP(w, r)
	})
}

// ---- brute-force guard: IPs that keep hitting wrong links get blocked -------

type strike struct {
	n     int
	first time.Time
	until time.Time
}

var (
	strikes   = map[string]*strike{}
	strikesMu sync.Mutex
)

const (
	strikeLimit  = 30
	strikeWindow = 10 * time.Minute
	banFor       = time.Hour
)

// clientIP returns the caller's address. Relay traffic arrives from the local
// cloudflared process, which passes the real address in Cf-Connecting-Ip.
func clientIP(r *http.Request) (string, bool) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if p := net.ParseIP(ip); p != nil && p.IsLoopback() {
		if c := r.Header.Get("Cf-Connecting-Ip"); c != "" {
			return c, true
		}
		return ip, false // the user's own PC: never ban
	}
	return ip, true
}

func isBanned(r *http.Request) bool {
	ip, ok := clientIP(r)
	if !ok {
		return false
	}
	strikesMu.Lock()
	defer strikesMu.Unlock()
	s := strikes[ip]
	return s != nil && time.Now().Before(s.until)
}

func addStrike(r *http.Request) {
	ip, ok := clientIP(r)
	if !ok {
		return
	}
	now := time.Now()
	strikesMu.Lock()
	defer strikesMu.Unlock()
	if len(strikes) > 10000 { // keep memory bounded
		for k, v := range strikes {
			if now.After(v.until) && now.Sub(v.first) > strikeWindow {
				delete(strikes, k)
			}
		}
	}
	s := strikes[ip]
	if s == nil || now.Sub(s.first) > strikeWindow {
		s = &strike{first: now}
		strikes[ip] = s
	}
	s.n++
	if s.n >= strikeLimit {
		s.until = now.Add(banFor)
	}
}

func handleAssets(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	var b []byte
	var err error
	if name == "icon.png" || name == "favicon.png" {
		b, err = assetFS.ReadFile("assets/" + name)
	} else {
		b, err = webFS.ReadFile("web/" + name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(b)
}

func handleLanding(w http.ResponseWriter, r *http.Request) {
	// Nothing lives at the bare address: a port scanner learns nothing.
	if r.URL.Path != "/" && r.URL.Path != "/favicon.ico" {
		addStrike(r)
	}
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// /s/{token}[/api | /f/{id} | /t/{id} | /zip | /up]
func handleShare(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/s/"), "/")
	s := getShare(parts[0])
	if s == nil {
		addStrike(r)
		b, _ := webFS.ReadFile("web/gone.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write(bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(appVersion)))
		return
	}
	if len(parts) == 1 || parts[1] == "" {
		if len(parts) == 1 {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusFound)
			return
		}
		s.hits.Add(1)
		b, _ := webFS.ReadFile("web/share.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(appVersion)))
		return
	}
	switch parts[1] {
	case "api":
		writeJSON(w, map[string]any{"name": s.Name, "files": s.Files, "total": s.Total, "allowUpload": s.AllowUpload, "app": appName, "version": appVersion})
	case "f", "t":
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		id, err := strconv.Atoi(parts[2])
		if err != nil || id < 0 || id >= len(s.Files) {
			http.NotFound(w, r)
			return
		}
		if parts[1] == "t" {
			serveThumb(w, r, s.Token, s.Files[id])
			return
		}
		serveFile(w, r, s.Files[id], &s.Meter, r.URL.Query().Get("dl") == "1")
	case "zip":
		serveZip(w, r, s)
	case "up":
		handleUpload(w, r, s)
	default:
		http.NotFound(w, r)
	}
}

// countingFile counts bytes read so the app can show live transfer speed.
type countingFile struct {
	*os.File
	m *Meter
}

func (c countingFile) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	c.m.bytes.Add(int64(n))
	return n, err
}

func contentDisposition(kind, name string) string {
	return kind + `; filename="` + strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name) + `"; filename*=UTF-8''` + url.PathEscape(name)
}

func serveFile(w http.ResponseWriter, r *http.Request, f *SFile, m *Meter, download bool) {
	fh, err := os.Open(f.Abs)
	if err != nil {
		http.Error(w, "File is no longer available on the sharer's PC.", http.StatusGone)
		return
	}
	defer fh.Close()
	m.active.Add(1)
	defer m.active.Add(-1)
	name := filepath.Base(f.Rel)
	ext := strings.ToLower(filepath.Ext(name))
	ctype := mime.TypeByExtension(ext)
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	if f.Kind == "text" || ext == ".svg" || strings.HasPrefix(ctype, "text/html") {
		ctype = "text/plain; charset=utf-8" // never render shared markup as a live page
	}
	w.Header().Set("Content-Type", ctype)
	if f.Kind != "pdf" {
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	if download {
		w.Header().Set("Content-Disposition", contentDisposition("attachment", name))
	} else {
		w.Header().Set("Content-Disposition", contentDisposition("inline", name))
	}
	http.ServeContent(w, r, "", f.Mod, countingFile{fh, m})
}

// writer that counts bytes for zip streaming.
type countWriter struct {
	w io.Writer
	m *Meter
}

func (c countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.m.bytes.Add(int64(n))
	return n, err
}

func serveZip(w http.ResponseWriter, r *http.Request, s *Share) {
	r.ParseForm()
	var sel []*SFile
	var pre string
	if ids := r.FormValue("ids"); ids != "" {
		for _, p := range strings.Split(ids, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && id >= 0 && id < len(s.Files) {
				sel = append(sel, s.Files[id])
			}
		}
	} else if pre = r.FormValue("prefix"); pre != "" {
		for _, f := range s.Files {
			if strings.HasPrefix(f.Rel, pre) {
				sel = append(sel, f)
			}
		}
	} else {
		sel = s.Files
	}
	if len(sel) == 0 {
		http.Error(w, "Nothing selected", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		name = s.Name
	}
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, name)
	s.active.Add(1)
	defer s.active.Add(-1)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", name+".zip"))
	zw := zip.NewWriter(countWriter{w, &s.Meter})
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	for _, f := range sel {
		fh, err := os.Open(f.Abs)
		if err != nil {
			continue
		}
		zname := f.Rel
		if pre != "" {
			// keep the chosen folder as the top level: "Trip/a.jpg", not "tf/Photos/Trip/a.jpg"
			parent := strings.TrimSuffix(pre, "/")
			if i := strings.LastIndex(parent, "/"); i >= 0 {
				zname = strings.TrimPrefix(zname, parent[:i+1])
			}
		}
		hdr := &zip.FileHeader{Name: zname, Method: zip.Store, Modified: f.Mod}
		hdr.SetMode(0o644)
		zf, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.CopyBuffer(zf, fh, *bp)
		}
		fh.Close()
		if err != nil {
			return // client went away
		}
	}
	zw.Close()
}

func handleUpload(w http.ResponseWriter, r *http.Request, s *Share) {
	if !s.AllowUpload {
		http.Error(w, "Uploads are not allowed for this share", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "Bad upload", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(cfg.DownloadDir, "Received", safeName(s.Name))
	os.MkdirAll(dir, 0o755)
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	var saved []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "Upload interrupted", http.StatusBadRequest)
			return
		}
		if part.FileName() == "" {
			continue
		}
		dst := uniquePath(filepath.Join(dir, safeName(filepath.Base(part.FileName()))))
		out, err := os.Create(dst + ".part")
		if err != nil {
			http.Error(w, "Cannot save file", http.StatusInternalServerError)
			return
		}
		_, err = io.CopyBuffer(out, part, *bp)
		out.Close()
		if err != nil {
			os.Remove(dst + ".part")
			http.Error(w, "Upload interrupted", http.StatusBadRequest)
			return
		}
		os.Rename(dst+".part", dst)
		saved = append(saved, dst)
	}
	s.recvMu.Lock()
	s.Received = append(s.Received, saved...)
	s.recvMu.Unlock()
	writeJSON(w, map[string]any{"saved": len(saved)})
}

func safeName(n string) string {
	n = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, n)
	n = strings.Trim(n, ". ")
	if n == "" {
		n = "file"
	}
	return n
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		if _, err := os.Stat(p + ".part"); os.IsNotExist(err) {
			return p
		}
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := base + " (" + itoa(i) + ")" + ext
		if _, err := os.Stat(c); os.IsNotExist(err) {
			return c
		}
	}
}

// ---- thumbnails ----------------------------------------------------------

func serveThumb(w http.ResponseWriter, r *http.Request, token string, f *SFile) {
	key := token + "/" + itoa(f.ID)
	if b, ok := thumbCache.get(key); ok {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Write(b)
		return
	}
	ext := strings.ToLower(filepath.Ext(f.Rel))
	if f.Kind != "image" || f.Size > 60<<20 || !(ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp" || ext == ".bmp") {
		http.NotFound(w, r)
		return
	}
	thumbSem <- struct{}{}
	defer func() { <-thumbSem }()
	fh, err := os.Open(f.Abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	img, _, err := image.Decode(fh)
	fh.Close()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b := img.Bounds()
	const max = 320
	tw, th := b.Dx(), b.Dy()
	if tw > max || th > max {
		if tw > th {
			th = th * max / tw
			tw = max
		} else {
			tw = tw * max / th
			th = max
		}
	}
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	var buf bytes.Buffer
	jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 72})
	thumbCache.put(key, buf.Bytes())
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(buf.Bytes())
}

type lru struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	m   map[string]*list.Element
}
type lruItem struct {
	k string
	v []byte
}

func newLRU(n int) *lru { return &lru{max: n, ll: list.New(), m: map[string]*list.Element{}} }
func (c *lru) get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*lruItem).v, true
	}
	return nil, false
}
func (c *lru) put(k string, v []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		e.Value.(*lruItem).v = v
		c.ll.MoveToFront(e)
		return
	}
	c.m[k] = c.ll.PushFront(&lruItem{k, v})
	for c.ll.Len() > c.max {
		e := c.ll.Back()
		c.ll.Remove(e)
		delete(c.m, e.Value.(*lruItem).k)
	}
}
