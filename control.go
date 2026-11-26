package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed ui
var uiFS embed.FS

//go:embed assets/buymeacoffee-qr.png
var bmcQR []byte

var (
	ctrlToken = randToken(16)
	ctrlURL   string
	fwState   = "unknown"
	fwMu      sync.Mutex
)

func startControlServer() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	ctrlURL = "http://" + ln.Addr().String() + "/ui/" + ctrlToken + "/"
	mux := http.NewServeMux()
	mux.HandleFunc("/ui/"+ctrlToken+"/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/ui/"+ctrlToken+"/")
		if strings.HasPrefix(rest, "rpc/") {
			handleRPC(w, r, strings.TrimPrefix(rest, "rpc/"))
			return
		}
		if rest == "" {
			rest = "app.html"
		}
		var b []byte
		var err error
		switch rest {
		case "bmc.png":
			b = bmcQR
		case "icon.png":
			b, err = assetFS.ReadFile("assets/icon.png")
		case "chat.js", "chat.css": // shared with the browser share page
			b, err = webFS.ReadFile("web/" + rest)
		default:
			b, err = uiFS.ReadFile("ui/" + rest)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(rest)))
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	go http.Serve(ln, mux)
	return nil
}

type rpcArgs struct {
	ID          string   `json:"id"`
	Paths       []string `json:"paths"`
	Folders     bool     `json:"folders"`
	AllowUpload bool     `json:"allowUpload"`
	Text        string   `json:"text"`
	Name        string   `json:"name"`
	Code        string   `json:"code"`
	FileIDs     []string `json:"fileIds"`
	On          bool     `json:"on"`
	Path        string   `json:"path"`
	URL         string   `json:"url"`
	Port        int      `json:"port"`
	DownloadDir string   `json:"downloadDir"`
	Theme       string   `json:"theme"`
	UseUPnP     *bool    `json:"useUpnp"`
	LinkMode    string   `json:"linkMode"`
	AutoRelay   *bool    `json:"autoRelay"`
	Method      string   `json:"method"`
	Remove      []string `json:"remove"`
	Restore     []string `json:"restore"`
	RestoreAll  bool     `json:"restoreAll"`
	SetRoots    bool     `json:"setRoots"`
	Kind        string   `json:"kind"`
	Since       int64    `json:"since"`
	Op          string   `json:"op"`
	Reply       int64    `json:"reply"`
	Msg         int64    `json:"msg"`
	Emoji       string   `json:"emoji"`
	Mentions    []string `json:"mentions"`
	ChatName    *string  `json:"chatName"`
	Background  *bool    `json:"background"`
}

func handleRPC(w http.ResponseWriter, r *http.Request, method string) {
	if r.Method != http.MethodPost || r.Header.Get("X-SM") != "1" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var a rpcArgs
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if len(body) > 0 {
		json.Unmarshal(body, &a)
	}
	res, err := callRPC(method, a)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": res})
}

func callRPC(method string, a rpcArgs) (any, error) {
	switch method {
	case "state":
		return buildState(), nil
	case "pick":
		title := "Choose files to share"
		if a.Folders {
			title = "Choose folders to share"
		}
		return pickPaths(a.Folders, title)
	case "scan":
		var ok []string
		for _, p := range a.Paths {
			p = strings.Trim(strings.TrimSpace(p), `"`)
			if p == "" {
				continue
			}
			if _, err := os.Stat(p); err != nil {
				return nil, errors.New("can't find: " + p)
			}
			ok = append(ok, filepath.Clean(p))
		}
		files, total, err := scanPaths(ok)
		if err != nil {
			return nil, err
		}
		kinds := map[string]int{}
		for _, f := range files {
			kinds[f.Kind]++
		}
		return map[string]any{"paths": ok, "count": len(files), "total": total, "kinds": kinds}, nil
	case "createShare":
		s, err := createShare(a.Paths, a.AllowUpload)
		if err != nil {
			return nil, err
		}
		go syncExposure()
		return s.ID, nil
	case "stopShare":
		stopShare(a.ID)
		go syncExposure()
		return true, nil
	case "phone":
		if a.Op == "folder" {
			d := phoneDir()
			os.MkdirAll(d, 0o755)
			return true, openPath(d)
		}
		setPhone(a.On, a.Op == "renew")
		return true, nil
	case "qr":
		png, err := qrcode.Encode(a.Text, qrcode.Medium, 320)
		if err != nil {
			return nil, err
		}
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
	case "setConfig":
		return nil, setConfig(a)
	case "pickDownloadDir":
		p, err := pickPaths(true, "Choose where downloads are saved")
		if err != nil || len(p) == 0 {
			return nil, err
		}
		a.DownloadDir = p[0]
		return p[0], setConfig(a)
	case "refreshNetwork":
		go refreshNetwork()
		return true, nil
	case "relay":
		autoRelayMu.Lock()
		relayAuto, relayUserOff = false, !a.On
		autoRelayMu.Unlock()
		if a.On {
			startRelay()
		} else {
			stopRelay()
		}
		return true, nil
	case "hostNet":
		h, err := addHostedNet(a.Name, a.Paths, a.Method)
		if err != nil {
			return nil, err
		}
		go syncExposure()
		return h.id, nil
	case "hostFiles", "editHostNet", "setHostMethod":
		swarmMu.RLock()
		h := hosted[a.ID]
		swarmMu.RUnlock()
		if h == nil {
			return nil, errors.New("network not found")
		}
		switch method {
		case "hostFiles":
			return h.hostFiles()
		case "editHostNet":
			var roots []string
			if a.SetRoots {
				roots = a.Paths
				if roots == nil {
					roots = []string{}
				}
			}
			return true, editHostNet(a.ID, a.Name, roots, a.Remove, a.Restore, a.RestoreAll)
		default:
			if err := setHostNetMethod(a.ID, a.Method); err != nil {
				return nil, err
			}
			go syncExposure()
			return true, nil
		}
	case "removeHostNet":
		removeHostedNet(a.ID)
		go syncExposure()
		return true, nil
	case "rescanHostNet":
		swarmMu.RLock()
		h := hosted[a.ID]
		swarmMu.RUnlock()
		if h == nil {
			return nil, errors.New("network not found")
		}
		h.rescan()
		return true, nil
	case "pauseHostNet":
		swarmMu.RLock()
		h := hosted[a.ID]
		swarmMu.RUnlock()
		if h == nil {
			return nil, errors.New("network not found")
		}
		cfgMu.Lock()
		h.cfg.Active = a.On
		saveConfigLocked()
		cfgMu.Unlock()
		go syncExposure()
		return true, nil
	case "join":
		j, err := joinNetwork(a.Code)
		if err != nil {
			return nil, err
		}
		return j.id, nil
	case "leave":
		leaveNetwork(a.ID)
		go syncExposure()
		return true, nil
	case "manifest", "download", "cancel", "pauseDl", "removeDl", "clearDone", "setSeed", "reconnect":
		swarmMu.RLock()
		j := joined[a.ID]
		swarmMu.RUnlock()
		if j == nil {
			return nil, errors.New("network not found")
		}
		switch method {
		case "manifest":
			if err := j.refreshManifest(true); err != nil {
				return nil, err
			}
			j.mu.Lock()
			n := 0
			if j.man != nil {
				n = len(j.man.Files)
			}
			j.mu.Unlock()
			return n, nil
		case "download":
			return j.startDownloads(a.FileIDs), nil
		case "cancel":
			for _, id := range a.FileIDs {
				j.cancelTask(id)
			}
		case "pauseDl":
			for _, id := range a.FileIDs {
				j.pauseTask(id)
			}
		case "removeDl":
			for _, id := range a.FileIDs {
				j.removeTask(id)
			}
		case "clearDone":
			j.clearFinished()
		case "setSeed":
			cfgMu.Lock()
			j.cfg.Seed = a.On
			saveConfigLocked()
			cfgMu.Unlock()
			j.mu.Lock()
			host := j.host
			j.mu.Unlock()
			if host != "" {
				go j.announce(host, false)
			}
			go syncExposure()
		case "reconnect":
			go j.connect()
		}
		return true, nil
	case "openPath":
		if a.Path == "" {
			a.Path = cfg.DownloadDir
			os.MkdirAll(a.Path, 0o755)
			return true, openPath(a.Path)
		}
		return true, revealPath(a.Path)
	case "refreshShare":
		return true, refreshShareLink(a.ID)
	case "renewHostNet":
		id, err := renewHostedNet(a.ID)
		if err != nil {
			return nil, err
		}
		go syncExposure()
		return id, nil
	case "bookmark":
		return toggleBookmark(a.Code, a.On)
	case "recvOpen":
		m, err := recvOpen(a.URL)
		if err != nil {
			return nil, err
		}
		return map[string]any{"manifest": m, "idm": idmPath() != ""}, nil
	case "recvStart":
		t, err := recvStart(a.Code, a.FileIDs, a.Path)
		if err != nil {
			return nil, err
		}
		return t.ID, nil
	case "recvIDM":
		return recvIDM(a.Code, a.FileIDs)
	case "recvBrowser":
		return true, recvBrowser(a.Code, a.FileIDs)
	case "pickFolder":
		p, err := pickPaths(true, a.Name)
		if err != nil || len(p) == 0 {
			return "", err
		}
		return p[0], nil
	case "recvRelocate":
		return recvRelocate(a.ID, a.Path)
	case "recvVerify":
		return recvVerify(a.ID)
	case "recvGetMissing":
		return recvGetMissing(a.ID)
	case "recvRemoveExtra":
		return recvRemoveExtra(a.ID, a.On)
	case "recvRelink":
		return true, recvRelink(a.ID, a.URL)
	case "recvPause":
		if t := findRecv(a.ID); t != nil {
			t.pause()
		}
		return true, nil
	case "recvResume":
		if t := findRecv(a.ID); t != nil {
			t.start()
		}
		return true, nil
	case "recvRemove":
		removeRecv(a.ID, a.On) // on = also delete unfinished parts
		return true, nil
	case "recvOpenDir":
		if t := findRecv(a.ID); t != nil {
			os.MkdirAll(t.Dir, 0o755)
			// Open the shared folder itself (or highlight the file) inside the download folder.
			if len(t.Files) > 0 {
				p := filepath.Join(t.Dir, cleanRel(strings.SplitN(t.Files[0].Rel, "/", 2)[0]))
				if st, err := os.Stat(p); err == nil {
					if st.IsDir() {
						return true, openPath(p)
					}
					return true, revealPath(p)
				}
			}
			return true, openPath(t.Dir)
		}
		return true, nil
	case "chat":
		return chatCall(a.Kind, a.ID, a.Since, nil)
	case "chatPost":
		return chatCall(a.Kind, a.ID, 0, &chatPost{Op: a.Op, Text: a.Text, Reply: a.Reply, Mentions: a.Mentions, Msg: a.Msg, Emoji: a.Emoji})
	case "pauseAll":
		setPausedAll(a.On)
		return true, nil
	case "bdixCheck":
		startBDIXCheck()
		return true, nil
	case "openURL":
		if !strings.HasPrefix(a.URL, "https://") && !strings.HasPrefix(a.URL, "http://") {
			return nil, errors.New("bad url")
		}
		return true, openURL(a.URL)
	case "checkUpdate":
		return checkUpdate()
	case "applyUpdate":
		return true, applyUpdate()
	case "firewall":
		if err := allowFirewall(); err != nil {
			return nil, err
		}
		go func() { checkFirewallRule() }()
		return true, nil
	}
	return nil, errors.New("unknown action " + method)
}

func setConfig(a rpcArgs) error {
	cfgMu.Lock()
	restart := false
	if a.Port != 0 && a.Port != cfg.Port {
		if a.Port < 1024 || a.Port > 65535 {
			cfgMu.Unlock()
			return errors.New("port must be between 1024 and 65535")
		}
		cfg.Port = a.Port
		restart = true
	}
	if a.DownloadDir != "" {
		cfg.DownloadDir = a.DownloadDir
	}
	if a.Theme != "" {
		cfg.Theme = a.Theme
	}
	upnpChanged := false
	if a.UseUPnP != nil && *a.UseUPnP != cfg.UseUPnP {
		cfg.UseUPnP = *a.UseUPnP
		upnpChanged = true
	}
	if a.LinkMode != "" {
		cfg.LinkMode = a.LinkMode
	}
	relayChanged := false
	if a.AutoRelay != nil {
		v := *a.AutoRelay
		cfg.AutoRelay = &v
		relayChanged = true
	}
	bgChanged := false
	if a.Background != nil {
		v := *a.Background
		cfg.Background = &v
		bgChanged = true
	}
	if a.ChatName != nil {
		n := cleanChatName(*a.ChatName)
		if n == "" {
			cfgMu.Unlock()
			return errors.New("type a name")
		}
		cfg.ChatName = n
	}
	saveConfigLocked()
	port := cfg.Port
	cfgMu.Unlock()
	if bgChanged {
		go syncTray()
	}
	if relayChanged {
		autoRelayMu.Lock()
		relayUserOff = false
		autoRelayMu.Unlock()
		go maybeAutoRelay()
	}
	if restart {
		unmapUPnP()
		if err := startPublicServer(port); err != nil {
			return errors.New("port " + itoa(port) + " is busy. Choose another")
		}
		// The relay points at the port: move it along, also when it is
		// still starting (it would keep the old port otherwise).
		if s := getNet().RelayState; s == "on" || s == "starting" || s == "downloading" {
			startRelay()
		}
		go checkFirewallRule()
	}
	if restart || upnpChanged {
		if !cfg.UseUPnP {
			unmapUPnP()
		}
		go refreshNetwork()
	}
	return nil
}

// ---- state snapshot for the UI ---------------------------------------------

type linkOut struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	URL   string `json:"url"`
	Note  string `json:"note"`
}

func buildState() map[string]any {
	eps := endpoints()
	best := bestEndpoint(eps)
	mkLinks := func(path string) ([]linkOut, string) {
		var out []linkOut
		for _, e := range eps {
			u := ""
			if e.Base != "" {
				u = e.Base + path
			}
			out = append(out, linkOut{e.Kind, e.Label, u, e.Note})
		}
		b := ""
		if best != nil {
			b = best.Base + path
		}
		return out, b
	}

	sharesMu.RLock()
	var sl []*Share
	for _, s := range shares {
		sl = append(sl, s)
	}
	sharesMu.RUnlock()
	sort.Slice(sl, func(i, j int) bool { return sl[i].Created.After(sl[j].Created) })
	var so []map[string]any
	for _, s := range sl {
		links, bestURL := mkLinks("/s/" + s.Token + "/")
		s.recvMu.Lock()
		recv := append([]string(nil), s.Received...)
		s.recvMu.Unlock()
		s.chat.mu.Lock()
		chatSeq := s.chat.Seq
		s.chat.mu.Unlock()
		so = append(so, map[string]any{
			"id": s.ID, "name": s.Name, "count": len(s.Files), "total": s.Total, "links": links, "best": bestURL,
			"allowUpload": s.AllowUpload, "visits": s.hits.Load(), "sent": s.bytes.Load(), "speed": s.Speed(),
			"active": s.active.Load(), "received": recv, "roots": s.Roots, "chat": chatSeq, "connected": s.connected(),
		})
	}

	swarmMu.RLock()
	var hl []*HostNet
	for _, h := range hosted {
		hl = append(hl, h)
	}
	var jl []*JoinNet
	for _, j := range joined {
		jl = append(jl, j)
	}
	swarmMu.RUnlock()
	sort.Slice(hl, func(i, j int) bool { return hl[i].cfg.Name < hl[j].cfg.Name })
	sort.Slice(jl, func(a, b int) bool { return jl[a].id < jl[b].id })

	var ho []map[string]any
	for _, h := range hl {
		h.mu.Lock()
		count, total, scanErr := 0, int64(0), h.scanErr
		if h.man != nil {
			count, total = len(h.man.Files), h.man.Total
		}
		h.mu.Unlock()
		members := []string{}
		for _, p := range h.livePeers() {
			if p.Name != "" && !contains(members, p.Name) {
				members = append(members, p.Name)
			}
		}
		sort.Strings(members)
		h.chat.mu.Lock()
		hchat := h.chat.Seq
		h.chat.mu.Unlock()
		ho = append(ho, map[string]any{
			"id": h.id, "name": h.cfg.Name, "code": h.code(), "count": count, "total": total, "roots": h.cfg.Roots,
			"active": h.cfg.Active, "method": h.method(), "methodNote": methodStatus(h.method()), "excluded": len(h.cfg.Exclude), "peers": len(h.livePeers()), "sent": h.bytes.Load(), "speed": h.Speed(), "err": scanErr, "chat": hchat, "members": members,
		})
	}
	var jo []map[string]any
	for _, j := range jl {
		j.mu.Lock()
		var files []*NFile
		name := j.cfg.Name
		hostName := ""
		if j.man != nil {
			files = j.man.Files
			name = j.man.Name
			hostName = j.man.Host
		}
		peers := len(j.peers)
		status, errS := j.status, j.err
		jchat := j.chatSeq
		j.mu.Unlock()
		cfgMu.Lock()
		have := map[string]bool{}
		for id, p := range j.cfg.Have {
			have[id] = p != ""
		}
		seed := j.cfg.Seed
		code := j.cfg.Code
		bookmarked := bookmarkIndexLocked(j.id) >= 0
		cfgMu.Unlock()
		if name == "" {
			name = "Network"
		}
		jo = append(jo, map[string]any{
			"id": j.id, "name": name, "status": status, "err": errS, "files": files, "peers": peers,
			"tasks": j.taskList(), "have": have, "seed": seed, "uploaded": j.bytes.Load(), "code": code, "bookmarked": bookmarked, "chat": jchat, "hostName": hostName,
		})
	}
	fwMu.Lock()
	fw := fwState
	fwMu.Unlock()
	publicMu.Lock()
	pe := publicErr
	publicMu.Unlock()
	cfgMu.Lock()
	c := *cfg
	cfgMu.Unlock()
	return map[string]any{
		"app": appName, "version": appVersion, "repo": "https://github.com/" + repoSlug,
		"net": getNet(), "links": eps, "shares": so, "hosted": ho, "joined": jo, "receives": recvView(), "bookmarks": bookmarksView(),
		"config": map[string]any{"port": c.Port, "downloadDir": c.DownloadDir, "theme": c.Theme, "useUpnp": c.UseUPnP, "linkMode": c.LinkMode, "autoRelay": c.AutoRelay != nil && *c.AutoRelay, "chatName": c.ChatName, "background": c.Background == nil || *c.Background},
		"paused": isPausedAll(),
		"os":     runtime.GOOS, "trash": trashName, "fwName": fwShort,
		"phone":    phoneState(mkLinks),
		"firewall": fw, "publicErr": pe, "listenPort": publicPort,
	}
}
