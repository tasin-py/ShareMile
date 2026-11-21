package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// SFile is one shared file.
type SFile struct {
	ID   int       `json:"id"`
	Rel  string    `json:"rel"` // forward-slash relative path shown to receivers
	Abs  string    `json:"-"`
	Size int64     `json:"size"`
	Mod  time.Time `json:"-"`
	Kind string    `json:"kind"`
	// Sender's file times (Unix ms), so receivers can keep them.
	MTime int64 `json:"mtime,omitempty"`
	CTime int64 `json:"ctime,omitempty"`
}

// fileTimes returns a file's modified and created times in Unix ms.
func fileTimes(info fs.FileInfo) (mtime, ctime int64) {
	mtime = info.ModTime().UnixMilli()
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		ctime = time.Unix(0, d.CreationTime.Nanoseconds()).UnixMilli()
	}
	return
}

// setFileTimes applies the sender's created/modified times to a received file.
func setFileTimes(path string, mtime, ctime int64) {
	if mtime <= 0 && ctime <= 0 {
		return
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	h, err := syscall.CreateFile(p, syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(h)
	ft := func(ms int64) *syscall.Filetime {
		if ms <= 0 {
			return nil
		}
		f := syscall.NsecToFiletime(ms * int64(time.Millisecond))
		return &f
	}
	syscall.SetFileTime(h, ft(ctime), nil, ft(mtime))
}

// Meter tracks bytes and active transfers for a share.
type Meter struct {
	bytes   atomic.Int64
	active  atomic.Int32
	hits    atomic.Int64
	lastB   int64
	lastT   time.Time
	speed   float64
	speedMu sync.Mutex
}

func (m *Meter) Speed() float64 {
	m.speedMu.Lock()
	defer m.speedMu.Unlock()
	now := time.Now()
	b := m.bytes.Load()
	if m.lastT.IsZero() {
		m.lastT, m.lastB = now, b
		return 0
	}
	dt := now.Sub(m.lastT).Seconds()
	if dt >= 0.5 {
		inst := float64(b-m.lastB) / dt
		m.speed = m.speed*0.4 + inst*0.6
		if m.active.Load() == 0 && inst == 0 {
			m.speed = 0
		}
		m.lastT, m.lastB = now, b
	}
	return m.speed
}

// Share is a quick share reachable from any browser via its link.
type Share struct {
	ID          string
	Token       string
	Name        string
	Roots       []string
	Files       []*SFile
	Total       int64
	AllowUpload bool
	Created     time.Time
	Received    []string
	recvMu      sync.Mutex
	chat        *Chat
	Meter
}

var (
	shares   = map[string]*Share{} // by token
	sharesMu sync.RWMutex
)

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(b), "=")
}

var kindByExt = map[string]string{}

func init() {
	add := func(kind string, exts ...string) {
		for _, e := range exts {
			kindByExt[e] = kind
		}
	}
	add("image", "jpg", "jpeg", "png", "gif", "webp", "bmp", "svg", "avif", "ico", "heic")
	add("video", "mp4", "webm", "mkv", "mov", "avi", "m4v", "wmv", "flv", "3gp", "ts")
	add("audio", "mp3", "wav", "flac", "aac", "ogg", "m4a", "opus", "wma")
	add("text", "txt", "md", "log", "csv", "json", "xml", "yml", "yaml", "ini", "cfg", "go", "py", "js", "ts", "css", "html", "htm", "c", "cpp", "h", "java", "cs", "rs", "sh", "bat", "ps1", "sql", "srt", "toml")
	add("pdf", "pdf")
	add("archive", "zip", "rar", "7z", "tar", "gz", "bz2", "xz", "iso")
	add("doc", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "odt", "rtf", "epub")
	add("app", "exe", "msi", "apk", "appx", "msix", "dmg")
}

func fileKind(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if k, ok := kindByExt[ext]; ok {
		return k
	}
	return "file"
}

var skipNames = map[string]bool{"desktop.ini": true, "thumbs.db": true, ".ds_store": true}

// scanPaths walks the given files/folders and returns a flat, sorted file list.
func scanPaths(roots []string) ([]*SFile, int64, error) {
	var files []*SFile
	var total int64
	used := map[string]bool{}
	uniqueTop := func(name string) string {
		base, ext := name, ""
		if i := strings.LastIndex(name, "."); i > 0 {
			base, ext = name[:i], name[i:]
		}
		n := name
		for i := 2; used[strings.ToLower(n)]; i++ {
			n = base + " (" + itoa(i) + ")" + ext
		}
		used[strings.ToLower(n)] = true
		return n
	}
	for _, root := range roots {
		root = filepath.Clean(root)
		st, err := os.Stat(root)
		if err != nil {
			continue
		}
		top := filepath.Base(root)
		if strings.HasSuffix(root, `:\`) || top == `\` || top == "." {
			top = strings.TrimSuffix(strings.TrimSuffix(root, `\`), ":") + " drive"
		}
		top = uniqueTop(top)
		if !st.IsDir() {
			mt, ct := fileTimes(st)
			files = append(files, &SFile{Rel: top, Abs: root, Size: st.Size(), Mod: st.ModTime(), Kind: fileKind(root), MTime: mt, CTime: ct})
			total += st.Size()
			continue
		}
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			name := strings.ToLower(d.Name())
			if d.IsDir() {
				if name == "$recycle.bin" || name == "system volume information" {
					return fs.SkipDir
				}
				return nil
			}
			if skipNames[name] || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			mt, ct := fileTimes(info)
			files = append(files, &SFile{Rel: top + "/" + filepath.ToSlash(rel), Abs: p, Size: info.Size(), Mod: info.ModTime(), Kind: fileKind(p), MTime: mt, CTime: ct})
			total += info.Size()
			return nil
		})
	}
	if len(files) == 0 {
		return nil, 0, errors.New("no files found in the selected location")
	}
	sort.SliceStable(files, func(i, j int) bool { return strings.ToLower(files[i].Rel) < strings.ToLower(files[j].Rel) })
	for i, f := range files {
		f.ID = i
	}
	return files, total, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func shareName(roots []string) string {
	if len(roots) == 0 {
		return "Shared files"
	}
	n := filepath.Base(filepath.Clean(roots[0]))
	if len(roots) > 1 {
		n += " + " + itoa(len(roots)-1) + " more"
	}
	return n
}

func createShare(roots []string, allowUpload bool) (*Share, error) {
	files, total, err := scanPaths(roots)
	if err != nil {
		return nil, err
	}
	s := &Share{ID: randToken(6), Token: randToken(12), Name: shareName(roots), Roots: roots, Files: files, Total: total, AllowUpload: allowUpload, Created: time.Now(), chat: newChat("")}
	sharesMu.Lock()
	shares[s.Token] = s
	sharesMu.Unlock()
	return s, nil
}

func stopShare(id string) {
	sharesMu.Lock()
	defer sharesMu.Unlock()
	for t, s := range shares {
		if s.ID == id {
			delete(shares, t)
		}
	}
}

func getShare(token string) *Share {
	sharesMu.RLock()
	defer sharesMu.RUnlock()
	return shares[token]
}
