package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ShareMile Network: a private swarm. The host is tracker + first seed;
// members who downloaded a file can seed it to others, and downloads pull
// pieces from several sources in parallel. Pieces from members are verified
// against SHA-256 hashes computed by the host.

const pieceSize = 4 << 20

type NFile struct {
	ID   string `json:"id"`
	Rel  string `json:"rel"`
	Size int64  `json:"size"`
	Kind string `json:"kind"`
}

type Manifest struct {
	Name  string   `json:"name"`
	Files []*NFile `json:"files"`
	Total int64    `json:"total"`
}

type PeerInfo struct {
	ID        string    `json:"id"`
	Endpoints []string  `json:"endpoints"`
	Have      []string  `json:"have"`
	Seen      time.Time `json:"-"`
}

var selfPeerID = randToken(8)

// ---------------------------------------------------------------- host side

type HostNet struct {
	cfg     *HostedNetCfg
	key     []byte
	id      string
	mu      sync.Mutex
	files   map[string]*SFile
	man     *Manifest
	scanErr string
	peers   map[string]*PeerInfo
	hashes  map[string]*hashJob
	Meter
}

type hashJob struct {
	once   sync.Once
	hashes []string
	err    error
}

var (
	hosted   = map[string]*HostNet{}
	joined   = map[string]*JoinNet{}
	swarmMu  sync.RWMutex
	dlClient = &http.Client{Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         dialWithDNSFallback(&net.Dialer{Timeout: 6 * time.Second, KeepAlive: 30 * time.Second}),
		MaxIdleConnsPerHost: 16,
		DisableCompression:  true,
		WriteBufferSize:     256 << 10,
		ReadBufferSize:      1 << 20,
		IdleConnTimeout:     90 * time.Second,
	}}
	dlSem = make(chan struct{}, 3)
)

func fileNetID(f *SFile) string {
	h := sha256.Sum256([]byte(f.Rel + "|" + strconv.FormatInt(f.Size, 10) + "|" + strconv.FormatInt(f.Mod.Unix(), 10)))
	return hex.EncodeToString(h[:8])
}

func (h *HostNet) rescan() {
	files, total, err := scanPaths(h.cfg.Roots)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.scanErr = err.Error()
		h.man = &Manifest{Name: h.cfg.Name}
		h.files = map[string]*SFile{}
		return
	}
	h.scanErr = ""
	m := &Manifest{Name: h.cfg.Name, Total: total}
	fm := map[string]*SFile{}
	for _, f := range files {
		id := fileNetID(f)
		fm[id] = f
		m.Files = append(m.Files, &NFile{ID: id, Rel: f.Rel, Size: f.Size, Kind: f.Kind})
	}
	h.files, h.man = fm, m
}

func (h *HostNet) code() string {
	return encodeCode(NetCode{Key: h.key, Endpoints: selfEndpoints(true)})
}

// selfEndpoints lists addresses others can use to reach this PC.
func selfEndpoints(includePublicGuess bool) []string {
	n := getNet()
	var eps []string
	for i, ip := range n.LanIPs {
		if i < 2 {
			eps = append(eps, hostPort(ip, publicPort))
		}
	}
	if n.PublicIP != "" && (n.PublicOK || (includePublicGuess && n.RealIP != "no")) {
		eps = append(eps, hostPort(n.PublicIP, publicPort))
	}
	if n.IPv6 != "" {
		eps = append(eps, hostPort(n.IPv6, publicPort))
	}
	if n.RelayURL != "" {
		eps = append(eps, n.RelayURL)
	}
	return eps
}

func newKey() []byte {
	k := make([]byte, 10)
	rand.Read(k)
	return k
}

func addHostedNet(name string, roots []string) (*HostNet, error) {
	if len(roots) == 0 {
		return nil, errors.New("pick at least one file or folder")
	}
	if strings.TrimSpace(name) == "" {
		name = shareName(roots)
	}
	c := &HostedNetCfg{Key: hex.EncodeToString(newKey()), Name: strings.TrimSpace(name), Roots: roots, Active: true}
	h := startHostedNet(c)
	if h.scanErr != "" {
		swarmMu.Lock()
		delete(hosted, h.id)
		swarmMu.Unlock()
		return nil, errors.New(h.scanErr)
	}
	cfgMu.Lock()
	cfg.Networks = append(cfg.Networks, c)
	saveConfigLocked()
	cfgMu.Unlock()
	return h, nil
}

func startHostedNet(c *HostedNetCfg) *HostNet {
	key, _ := hex.DecodeString(c.Key)
	h := &HostNet{cfg: c, key: key, id: netID(key), peers: map[string]*PeerInfo{}, hashes: map[string]*hashJob{}}
	h.rescan()
	swarmMu.Lock()
	hosted[h.id] = h
	swarmMu.Unlock()
	ensureDiscovery()
	return h
}

func removeHostedNet(id string) {
	swarmMu.Lock()
	h := hosted[id]
	delete(hosted, id)
	swarmMu.Unlock()
	if h == nil {
		return
	}
	cfgMu.Lock()
	out := cfg.Networks[:0]
	for _, c := range cfg.Networks {
		if c != h.cfg {
			out = append(out, c)
		}
	}
	cfg.Networks = out
	saveConfigLocked()
	cfgMu.Unlock()
}

func (h *HostNet) pieceHashes(fid string) ([]string, error) {
	h.mu.Lock()
	f := h.files[fid]
	if f == nil {
		h.mu.Unlock()
		return nil, errors.New("not found")
	}
	j := h.hashes[fid]
	if j == nil {
		j = &hashJob{}
		h.hashes[fid] = j
	}
	h.mu.Unlock()
	j.once.Do(func() {
		fh, err := os.Open(f.Abs)
		if err != nil {
			j.err = err
			return
		}
		defer fh.Close()
		bp := bufPool.Get().(*[]byte)
		defer bufPool.Put(bp)
		for {
			hs := sha256.New()
			n, err := io.CopyBuffer(hs, io.LimitReader(fh, pieceSize), *bp)
			if n > 0 {
				j.hashes = append(j.hashes, hex.EncodeToString(hs.Sum(nil)))
			}
			if err != nil {
				j.err = err
				return
			}
			if n < pieceSize {
				return
			}
		}
	})
	return j.hashes, j.err
}

func (h *HostNet) livePeers() []*PeerInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*PeerInfo
	for id, p := range h.peers {
		if time.Since(p.Seen) > 100*time.Second {
			delete(h.peers, id)
			continue
		}
		out = append(out, p)
	}
	return out
}

// ------------------------------------------------------------- HTTP surface

// /n/{netID}/{op}...
func handleNetwork(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/n/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	swarmMu.RLock()
	h := hosted[parts[0]]
	j := joined[parts[0]]
	swarmMu.RUnlock()
	var key []byte
	if h != nil && h.cfg.Active {
		key = h.key
	} else if j != nil {
		key = j.key
		h = nil
	} else if isRetiredNet(parts[0]) {
		// The host replaced this network's code: tell old members, without a strike.
		http.Error(w, "code replaced", http.StatusGone)
		return
	} else {
		addStrike(r)
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("X-SM-Auth") != authToken(key) {
		addStrike(r)
		http.Error(w, "wrong code", http.StatusForbidden)
		return
	}
	op := parts[1]
	if h != nil {
		switch op {
		case "ping":
			writeJSON(w, map[string]any{"ok": true, "role": "host", "name": h.cfg.Name})
		case "manifest":
			h.mu.Lock()
			m := h.man
			h.mu.Unlock()
			writeJSON(w, m)
		case "h":
			if len(parts) < 3 {
				http.NotFound(w, r)
				return
			}
			hs, err := h.pieceHashes(parts[2])
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"pieceSize": pieceSize, "hashes": hs})
		case "f":
			if len(parts) < 3 {
				http.NotFound(w, r)
				return
			}
			h.mu.Lock()
			f := h.files[parts[2]]
			h.mu.Unlock()
			if f == nil {
				http.NotFound(w, r)
				return
			}
			serveFile(w, r, f, &h.Meter, true)
		case "announce":
			var p PeerInfo
			if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p) != nil || p.ID == "" {
				http.Error(w, "bad announce", http.StatusBadRequest)
				return
			}
			// Add the address we actually saw, in case the member doesn't know its public IP.
			if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && len(p.Endpoints) > 0 && r.Header.Get("X-SM-Port") != "" {
				if pip := net.ParseIP(ip); pip != nil && !pip.IsLoopback() {
					seen := hostPort(ip, atoiDef(r.Header.Get("X-SM-Port"), 0))
					if !contains(p.Endpoints, seen) {
						p.Endpoints = append(p.Endpoints, seen)
					}
				}
			}
			p.Seen = time.Now()
			h.mu.Lock()
			if r.URL.Query().Get("leave") == "1" {
				delete(h.peers, p.ID)
			} else {
				h.peers[p.ID] = &p
			}
			h.mu.Unlock()
			var others []*PeerInfo
			for _, x := range h.livePeers() {
				if x.ID != p.ID {
					others = append(others, x)
				}
			}
			writeJSON(w, map[string]any{"peers": others})
		default:
			http.NotFound(w, r)
		}
		return
	}
	// member (seeding) side
	switch op {
	case "ping":
		writeJSON(w, map[string]any{"ok": true, "role": "member"})
	case "f":
		if len(parts) < 3 || !j.cfgSeed() {
			http.NotFound(w, r)
			return
		}
		path := j.havePath(parts[2])
		st, err := os.Stat(path)
		if path == "" || err != nil {
			http.NotFound(w, r)
			return
		}
		serveFile(w, r, &SFile{Rel: filepath.Base(path), Abs: path, Size: st.Size(), Mod: st.ModTime(), Kind: "file"}, &j.Meter, true)
	default:
		http.NotFound(w, r)
	}
}

func atoiDef(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}

// -------------------------------------------------------------- member side

type DLTask struct {
	File    *NFile  `json:"file"`
	State   string  `json:"state"` // queued|preparing|downloading|done|error|cancelled
	Err     string  `json:"err"`
	Done    int64   `json:"done"`
	Sources int     `json:"sources"`
	Rate    float64 `json:"speed"`
	Path    string  `json:"path"`
	done    atomic.Int64
	cancel  context.CancelFunc
	mu      sync.Mutex
	Meter
}

type JoinNet struct {
	cfg    *JoinedNetCfg
	key    []byte
	id     string
	mu     sync.Mutex
	host   string
	status string // connecting|online|offline
	err    string
	man    *Manifest
	peers  []*PeerInfo
	tasks  map[string]*DLTask
	order  []string
	stop   chan struct{}
	Meter
}

func (j *JoinNet) cfgSeed() bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return j.cfg.Seed
}

func (j *JoinNet) havePath(fid string) string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return j.cfg.Have[fid]
}

func (j *JoinNet) req(ctx context.Context, method, base, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpointURL(base), "/")+"/n/"+j.id+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-SM-Auth", authToken(j.key))
	req.Header.Set("X-SM-Port", itoa(publicPort))
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	return dlClient.Do(req)
}

func joinNetwork(code string) (*JoinNet, error) {
	c, err := decodeCode(code)
	if err != nil {
		return nil, err
	}
	id := netID(c.Key)
	swarmMu.RLock()
	if hosted[id] != nil {
		swarmMu.RUnlock()
		return nil, errors.New("you are the host of this network")
	}
	if j := joined[id]; j != nil {
		swarmMu.RUnlock()
		j.mu.Lock()
		cfgMu.Lock()
		j.cfg.Endpoints = c.Endpoints
		j.cfg.Code = strings.TrimSpace(code)
		saveConfigLocked()
		cfgMu.Unlock()
		j.mu.Unlock()
		go j.connect()
		return j, nil
	}
	swarmMu.RUnlock()
	jc := &JoinedNetCfg{Key: hex.EncodeToString(c.Key), Code: strings.TrimSpace(code), Endpoints: c.Endpoints, Have: map[string]string{}, Seed: true}
	j := startJoined(jc)
	cfgMu.Lock()
	cfg.Joined = append(cfg.Joined, jc)
	saveConfigLocked()
	cfgMu.Unlock()
	return j, nil
}

func startJoined(jc *JoinedNetCfg) *JoinNet {
	key, _ := hex.DecodeString(jc.Key)
	j := &JoinNet{cfg: jc, key: key, id: netID(key), status: "connecting", tasks: map[string]*DLTask{}, stop: make(chan struct{})}
	swarmMu.Lock()
	joined[j.id] = j
	swarmMu.Unlock()
	go j.loop()
	return j
}

func leaveNetwork(id string) {
	swarmMu.Lock()
	j := joined[id]
	delete(joined, id)
	swarmMu.Unlock()
	if j == nil {
		return
	}
	close(j.stop)
	j.mu.Lock()
	for _, t := range j.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
	host := j.host
	j.mu.Unlock()
	if host != "" {
		go j.announce(host, true)
	}
	cfgMu.Lock()
	out := cfg.Joined[:0]
	for _, c := range cfg.Joined {
		if c != j.cfg {
			out = append(out, c)
		}
	}
	cfg.Joined = out
	saveConfigLocked()
	cfgMu.Unlock()
}

func (j *JoinNet) loop() {
	j.connect()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-j.stop:
			return
		case <-t.C:
			j.mu.Lock()
			host := j.host
			j.mu.Unlock()
			if host == "" || j.announce(host, false) != nil {
				j.connect()
			}
		}
	}
}

// connect probes every known address (plus LAN discovery) and keeps the first that answers.
func (j *JoinNet) connect() {
	j.mu.Lock()
	j.status = "connecting"
	cands := append([]string(nil), j.cfg.Endpoints...)
	j.mu.Unlock()
	for _, e := range lanDiscover(j.id) {
		if !contains(cands, e) {
			cands = append([]string{e}, cands...)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type res struct {
		base string
		man  *Manifest
	}
	ch := make(chan res, len(cands))
	var replaced atomic.Bool
	for _, e := range cands {
		go func(e string) {
			resp, err := j.req(ctx, "GET", e, "/manifest", nil)
			if err != nil {
				ch <- res{}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusGone {
				replaced.Store(true)
			}
			var m Manifest
			if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&m) != nil {
				ch <- res{}
				return
			}
			ch <- res{e, &m}
		}(e)
	}
	for range cands {
		r := <-ch
		if r.man != nil {
			cancel()
			j.mu.Lock()
			j.host, j.man, j.status, j.err = r.base, r.man, "online", ""
			j.mu.Unlock()
			cfgMu.Lock()
			j.cfg.Name = r.man.Name
			saveConfigLocked()
			cfgMu.Unlock()
			j.announce(r.base, false)
			return
		}
	}
	j.mu.Lock()
	j.host, j.status = "", "offline"
	j.err = "Can't reach the host. They may be offline, or their address changed (ask them for a fresh code)."
	if replaced.Load() {
		j.err = "The host replaced this network's secret code. Ask them for the new code and join with it."
	}
	j.mu.Unlock()
}

func (j *JoinNet) announce(host string, leave bool) error {
	var have []string
	var eps []string
	if j.cfgSeed() {
		cfgMu.Lock()
		for id, p := range j.cfg.Have {
			if fileExists(p) {
				have = append(have, id)
			}
		}
		cfgMu.Unlock()
		eps = selfEndpoints(false)
	}
	b, _ := json.Marshal(PeerInfo{ID: selfPeerID, Endpoints: eps, Have: have})
	q := ""
	if leave {
		q = "?leave=1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := j.req(ctx, "POST", host, "/announce"+q, bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Peers []*PeerInfo `json:"peers"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&out) != nil {
		return errors.New("announce failed")
	}
	j.mu.Lock()
	j.peers = out.Peers
	j.mu.Unlock()
	return nil
}

func (j *JoinNet) refreshManifest() error {
	j.mu.Lock()
	host := j.host
	j.mu.Unlock()
	if host == "" {
		j.connect()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	resp, err := j.req(ctx, "GET", host, "/manifest", nil)
	if err != nil {
		go j.connect()
		return err
	}
	defer resp.Body.Close()
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return err
	}
	j.mu.Lock()
	j.man = &m
	code := j.cfg.Code
	j.mu.Unlock()
	syncBookmark(j.id, m.Name, code)
	return nil
}

func (j *JoinNet) startDownloads(ids []string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.man == nil {
		return 0
	}
	byID := map[string]*NFile{}
	for _, f := range j.man.Files {
		byID[f.ID] = f
	}
	n := 0
	for _, id := range ids {
		f := byID[id]
		if f == nil {
			continue
		}
		if t := j.tasks[id]; t != nil && (t.state() == "queued" || t.state() == "preparing" || t.state() == "downloading") {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		t := &DLTask{File: f, State: "queued", cancel: cancel}
		if _, ok := j.tasks[id]; !ok {
			j.order = append(j.order, id)
		}
		j.tasks[id] = t
		n++
		go j.runTask(ctx, t)
	}
	return n
}

func (j *JoinNet) cancelTask(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if t := j.tasks[id]; t != nil && t.cancel != nil {
		t.cancel()
	}
}

func (j *JoinNet) clearFinished() {
	j.mu.Lock()
	defer j.mu.Unlock()
	var order []string
	for _, id := range j.order {
		t := j.tasks[id]
		if st := t.stateOr(); st == "done" || st == "cancelled" || st == "error" {
			delete(j.tasks, id)
			continue
		}
		order = append(order, id)
	}
	j.order = order
}

func (t *DLTask) set(state, err string) {
	t.mu.Lock()
	t.State, t.Err = state, err
	t.mu.Unlock()
}

func (t *DLTask) stateOr() string {
	if t == nil {
		return "done"
	}
	return t.state()
}

func (t *DLTask) state() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.State
}

type source struct {
	base   string
	verify bool
	fails  int
}

func (j *JoinNet) runTask(ctx context.Context, t *DLTask) {
	select {
	case dlSem <- struct{}{}:
	case <-ctx.Done():
		t.set("cancelled", "")
		return
	}
	defer func() { <-dlSem }()
	err := j.download(ctx, t)
	switch {
	case ctx.Err() != nil:
		t.set("cancelled", "")
	case err != nil:
		t.set("error", err.Error())
	default:
		t.set("done", "")
		go syncExposure() // now seeding this file: make sure we're reachable
		j.mu.Lock()
		host := j.host
		j.mu.Unlock()
		if host != "" {
			go j.announce(host, false)
		}
	}
}

func cleanRel(rel string) string {
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		p = safeName(p)
		if p == ".." || p == "." {
			continue
		}
		parts = append(parts, p)
	}
	return filepath.Join(parts...)
}

func (j *JoinNet) download(ctx context.Context, t *DLTask) error {
	j.mu.Lock()
	host := j.host
	name := "ShareMile network"
	if j.man != nil {
		name = j.man.Name
	}
	var holders []*PeerInfo
	for _, p := range j.peers {
		if contains(p.Have, t.File.ID) && len(p.Endpoints) > 0 {
			holders = append(holders, p)
		}
	}
	j.mu.Unlock()
	if host == "" {
		return errors.New("host is offline")
	}
	t.set("preparing", "")
	peerSrc := j.reachablePeers(ctx, holders)
	f := t.File
	dest := filepath.Join(cfg.DownloadDir, safeName(name), cleanRel(f.Rel))
	os.MkdirAll(filepath.Dir(dest), 0o755)
	part := dest + ".smpart"
	statePath := part + ".json"

	n := int((f.Size + pieceSize - 1) / pieceSize)
	have := make([]bool, n)
	if b, err := os.ReadFile(statePath); err == nil {
		var st struct {
			Size   int64  `json:"size"`
			Pieces string `json:"pieces"`
		}
		if json.Unmarshal(b, &st) == nil && st.Size == f.Size {
			bits, _ := base64.StdEncoding.DecodeString(st.Pieces)
			for i := 0; i < n && i/8 < len(bits); i++ {
				have[i] = bits[i/8]&(1<<(i%8)) != 0
			}
		}
	}
	fh, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("can't write to %s", filepath.Dir(dest))
	}
	if st, _ := fh.Stat(); st.Size() != f.Size {
		if err := fh.Truncate(f.Size); err != nil {
			fh.Close()
			return errors.New("not enough disk space")
		}
	}
	var doneBytes int64
	for i, ok := range have {
		if ok {
			doneBytes += pieceLen(f.Size, i)
		}
	}
	t.done.Store(doneBytes)

	var hashes []string
	if len(peerSrc) > 0 {
		hctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		resp, err := j.req(hctx, "GET", host, "/h/"+f.ID, nil)
		if err == nil {
			var hr struct {
				Hashes []string `json:"hashes"`
			}
			if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&hr) == nil && len(hr.Hashes) == n {
				hashes = hr.Hashes
			}
			resp.Body.Close()
		}
		cancel()
		if hashes == nil {
			peerSrc = nil // can't verify member pieces; use host only
		}
	}
	srcs := append([]*source{{base: host}}, peerSrc...)
	t.mu.Lock()
	t.Sources = 1 + len(peerSrc)
	t.mu.Unlock()
	t.set("downloading", "")

	pending := make(chan int, n)
	remaining := atomic.Int64{}
	for i := 0; i < n; i++ {
		if !have[i] {
			pending <- i
			remaining.Add(1)
		}
	}
	var haveMu sync.Mutex
	saveState := func() {
		haveMu.Lock()
		bits := make([]byte, (n+7)/8)
		for i, ok := range have {
			if ok {
				bits[i/8] |= 1 << (i % 8)
			}
		}
		haveMu.Unlock()
		b, _ := json.Marshal(map[string]any{"size": f.Size, "pieces": base64.StdEncoding.EncodeToString(bits)})
		os.WriteFile(statePath, b, 0o644)
	}
	allDone := make(chan struct{})
	if remaining.Load() == 0 {
		close(allDone)
	}
	fctx, fcancel := context.WithCancel(ctx)
	defer fcancel()
	go func() {
		select {
		case <-allDone:
			fcancel() // stop slow duplicate/late requests immediately
		case <-fctx.Done():
		}
	}()
	var closeOnce sync.Once
	var wg sync.WaitGroup
	worker := func(s *source) {
		defer wg.Done()
		buf := make([]byte, pieceSize)
		for {
			var p int
			select {
			case p = <-pending:
			case <-allDone:
				return
			case <-ctx.Done():
				return
			}
			off := int64(p) * pieceSize
			ln := pieceLen(f.Size, p)
			err := fetchPiece(fctx, j, s.base, f.ID, off, buf[:ln], &t.Meter)
			if err == nil && s.verify {
				sum := sha256.Sum256(buf[:ln])
				if hex.EncodeToString(sum[:]) != hashes[p] {
					err = errors.New("bad piece")
				}
			}
			if err == nil {
				_, err = fh.WriteAt(buf[:ln], off)
				if err != nil {
					pending <- p
					return
				}
				haveMu.Lock()
				have[p] = true
				haveMu.Unlock()
				t.done.Add(ln)
				s.fails = 0
				if remaining.Add(-1) == 0 {
					closeOnce.Do(func() { close(allDone) })
				}
				continue
			}
			pending <- p
			if fctx.Err() != nil {
				return
			}
			s.fails++
			if s.fails >= 4 {
				return
			}
			select {
			case <-time.After(time.Duration(s.fails) * 700 * time.Millisecond):
			case <-fctx.Done():
				return
			}
		}
	}
	for _, s := range srcs {
		k := 2
		if !s.verify {
			k = 4 // parallel connections to the host, like a download manager
		}
		for i := 0; i < k; i++ {
			wg.Add(1)
			go worker(s)
		}
	}
	stopSaver := make(chan struct{})
	go func() {
		tk := time.NewTicker(3 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				saveState()
			case <-stopSaver:
				return
			}
		}
	}()
	wg.Wait()
	close(stopSaver)
	fh.Close()
	if remaining.Load() > 0 {
		saveState()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("connection lost. Press Download again to resume.")
	}
	os.Remove(statePath)
	final := uniquePath(dest)
	if err := os.Rename(part, final); err != nil {
		return err
	}
	t.mu.Lock()
	t.Path = final
	t.mu.Unlock()
	cfgMu.Lock()
	j.cfg.Have[f.ID] = final
	saveConfigLocked()
	cfgMu.Unlock()
	return nil
}

// reachablePeers pings every address of each member that holds the file and
// keeps the first one that answers, so dead addresses never hold up pieces.
func (j *JoinNet) reachablePeers(ctx context.Context, holders []*PeerInfo) []*source {
	if len(holders) == 0 {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	var out []*source
	var wg sync.WaitGroup
	for _, p := range holders {
		wg.Add(1)
		go func(p *PeerInfo) {
			defer wg.Done()
			ch := make(chan string, len(p.Endpoints))
			for _, e := range p.Endpoints {
				go func(e string) {
					resp, err := j.req(pctx, "GET", e, "/ping", nil)
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode == 200 {
							ch <- e
							return
						}
					}
					ch <- ""
				}(e)
			}
			for range p.Endpoints {
				if e := <-ch; e != "" {
					mu.Lock()
					out = append(out, &source{base: e, verify: true})
					mu.Unlock()
					return
				}
			}
		}(p)
	}
	wg.Wait()
	return out
}

func pieceLen(size int64, i int) int64 {
	off := int64(i) * pieceSize
	if size-off < pieceSize {
		return size - off
	}
	return pieceSize
}

func fetchPiece(ctx context.Context, j *JoinNet, base, fid string, off int64, buf []byte, m *Meter) error {
	if len(buf) == 0 {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(pctx, "GET", strings.TrimRight(endpointURL(base), "/")+"/n/"+j.id+"/f/"+fid, nil)
	req.Header.Set("X-SM-Auth", authToken(j.key))
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(buf))-1))
	resp, err := dlClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent && !(resp.StatusCode == 200 && off == 0) {
		return errors.New(resp.Status)
	}
	got := 0
	for got < len(buf) {
		n, err := resp.Body.Read(buf[got:])
		got += n
		m.bytes.Add(int64(n))
		if err != nil {
			if got == len(buf) {
				break
			}
			return err
		}
	}
	return nil
}

type TaskView struct {
	File    *NFile  `json:"file"`
	State   string  `json:"state"`
	Err     string  `json:"err"`
	Done    int64   `json:"done"`
	Sources int     `json:"sources"`
	Speed   float64 `json:"speed"`
	Path    string  `json:"path"`
}

func (t *DLTask) snapshot() TaskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TaskView{File: t.File, State: t.State, Err: t.Err, Done: t.done.Load(), Sources: t.Sources, Speed: t.Speed(), Path: t.Path}
}

func (j *JoinNet) taskList() []TaskView {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []TaskView
	for _, id := range j.order {
		if t := j.tasks[id]; t != nil {
			out = append(out, t.snapshot())
		}
	}
	return out
}

// ------------------------------------------------------- LAN discovery (UDP)

const discPort = 8751

var discOnce sync.Once

func ensureDiscovery() {
	discOnce.Do(func() {
		go func() {
			pc, err := net.ListenPacket("udp4", ":"+itoa(discPort))
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			for {
				n, addr, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				if n != 4+16 || string(buf[:4]) != "SMQ1" {
					continue
				}
				id := string(buf[4:20])
				swarmMu.RLock()
				h := hosted[id]
				swarmMu.RUnlock()
				if h == nil || !h.cfg.Active {
					continue
				}
				resp := append([]byte("SMR1"+id), byte(publicPort>>8), byte(publicPort))
				pc.WriteTo(resp, addr)
			}
		}()
	})
}

// lanDiscover broadcasts a query for the network id and returns hosts that answered.
func lanDiscover(id string) []string {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil
	}
	defer pc.Close()
	msg := []byte("SMQ1" + id)
	targets := []string{"255.255.255.255"}
	ifs, _ := net.Interfaces()
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
					b := make(net.IP, 4)
					for i := range b {
						b[i] = ip4[i] | ^ipn.Mask[len(ipn.Mask)-4+i]
					}
					targets = append(targets, b.String())
				}
			}
		}
	}
	for _, t := range targets {
		pc.WriteTo(msg, &net.UDPAddr{IP: net.ParseIP(t), Port: discPort})
	}
	pc.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	var out []string
	buf := make([]byte, 64)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		if n == 4+16+2 && string(buf[:4]) == "SMR1" && string(buf[4:20]) == id {
			port := int(buf[20])<<8 | int(buf[21])
			ua := addr.(*net.UDPAddr)
			e := hostPort(ua.IP.String(), port)
			if !contains(out, e) {
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return out
}
