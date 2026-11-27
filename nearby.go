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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Nearby devices: ShareMile apps on the same Wi-Fi or LAN find each other
// and send files with a tap, like SHAREit. Nothing goes through the internet.
//
//   - Scan: a UDP broadcast ("who's there?") that every visible ShareMile
//     answers, plus an HTTP sweep of the local /24 on the ShareMile port, for
//     networks or firewalls that drop broadcasts.
//   - Send: the sender posts an offer (a share on its own PC) to the device.
//     The receiver sees Accept / Decline. Accept downloads the share with the
//     built-in downloader over the LAN link, so resume and file checks work
//     as usual. The link always points at the address the offer came from.
//
// The /nearby/ endpoints answer only requests from the local network, never
// ones that arrive through the relay.

const nearbyUDPPort = 47851

type nearbyDevice struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OS      string `json:"os"`
	Version string `json:"version"`
	IP      string `json:"ip"`
	Port    int    `json:"port"`
}

type nearbyOffer struct {
	ID    string       `json:"id"`
	From  nearbyDevice `json:"from"`
	Token string       `json:"token"`
	Name  string       `json:"name"`
	Count int          `json:"count"`
	Total int64        `json:"total"`
	At    time.Time    `json:"at"`
}

type nearbyOut struct {
	ID      string    `json:"id"`
	Share   string    `json:"share"` // share id
	Device  string    `json:"device"`
	DevID   string    `json:"devId"`
	Status  string    `json:"status"` // waiting|accepted|declined|failed
	Err     string    `json:"err,omitempty"`
	At      time.Time `json:"at"`
	replyTo string
}

var (
	nearbyMu      sync.Mutex
	nearbyDevs    []nearbyDevice
	nearbyScanAt  time.Time
	nearbyBusy    bool
	nearbyOffers  []*nearbyOffer // incoming, waiting for an answer
	nearbySent    []*nearbyOut   // outgoing
	nearbyClient  = &http.Client{Timeout: 4 * time.Second}
	nearbyStarted sync.Once
)

func nearbyVisible() bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return !cfg.NearbyHidden
}

func selfDevice() nearbyDevice {
	_, id, _ := selfChat()
	return nearbyDevice{ID: id, Name: pcName(), OS: runtime.GOOS, Version: appVersion, Port: publicPort}
}

// lanOnly tells whether a request came straight from the local network.
// Relay traffic arrives from the local cloudflared with Cf-Connecting-Ip.
func lanOnly(r *http.Request) bool {
	if r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("Cf-Ray") != "" {
		return false
	}
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func remoteIP(r *http.Request) string {
	h, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(h); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	return h
}

// ---- answering ---------------------------------------------------------------

// startNearby listens for scans from other devices. Several ShareMile copies
// on one PC (tests) can all listen: the port is shared.
func startNearby() {
	nearbyStarted.Do(func() {
		go func() {
			for {
				pc, err := listenUDPShared(nearbyUDPPort)
				if err != nil {
					time.Sleep(30 * time.Second)
					continue
				}
				buf := make([]byte, 512)
				for {
					n, from, err := pc.ReadFrom(buf)
					if err != nil {
						break
					}
					msg := string(buf[:n])
					if !strings.HasPrefix(msg, "SM-NEARBY1 ") || !nearbyVisible() {
						continue
					}
					me := selfDevice()
					if strings.TrimPrefix(msg, "SM-NEARBY1 ") == me.ID {
						continue // our own scan
					}
					b, _ := json.Marshal(me)
					pc.WriteTo(b, from)
				}
				pc.Close()
				time.Sleep(5 * time.Second)
			}
		}()
	})
}

// handleNearby serves /nearby/hello, /nearby/offer and /nearby/reply.
func handleNearby(w http.ResponseWriter, r *http.Request) {
	if !lanOnly(r) {
		http.NotFound(w, r)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/nearby/") {
	case "hello":
		if !nearbyVisible() {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, selfDevice())
	case "offer":
		if r.Method != http.MethodPost || !nearbyVisible() {
			http.NotFound(w, r)
			return
		}
		var o nearbyOffer
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&o); err != nil || o.ID == "" || o.Token == "" || o.From.Port <= 0 {
			phoneErr(w, http.StatusBadRequest, "bad offer")
			return
		}
		o.From.IP = remoteIP(r) // the download always comes from where the offer came from
		o.From.Name = cleanChatName(o.From.Name)
		if o.From.Name == "" {
			o.From.Name = o.From.IP
		}
		if o.Name = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 32 || r == 0x7f {
				return -1
			}
			return r
		}, o.Name)); len([]rune(o.Name)) > 120 {
			o.Name = string([]rune(o.Name)[:120])
		}
		o.At = time.Now()
		nearbyMu.Lock()
		kept := nearbyOffers[:0]
		for _, x := range nearbyOffers {
			if time.Since(x.At) < 10*time.Minute && x.ID != o.ID && !(x.From.ID == o.From.ID && x.Token == o.Token) {
				kept = append(kept, x)
			}
		}
		nearbyOffers = kept
		if len(nearbyOffers) >= 20 {
			nearbyMu.Unlock()
			phoneErr(w, http.StatusTooManyRequests, "too many offers waiting")
			return
		}
		nearbyOffers = append(nearbyOffers, &o)
		nearbyMu.Unlock()
		trayBalloon("Incoming files", fmt.Sprintf("%s wants to send you %s. Open ShareMile to accept.", o.From.Name, plural(o.Count, "file", "files")))
		writeJSON(w, map[string]any{"ok": true})
	case "reply":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var a struct {
			ID       string `json:"id"`
			Accepted bool   `json:"accepted"`
		}
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&a)
		ip := remoteIP(r)
		nearbyMu.Lock()
		for _, s := range nearbySent {
			if s.ID == a.ID && s.Status == "waiting" && s.replyTo == ip {
				s.Status = map[bool]string{true: "accepted", false: "declined"}[a.Accepted]
			}
		}
		nearbyMu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---- scanning ----------------------------------------------------------------

// nearbyScan looks for ShareMile on the local network for about two seconds.
func nearbyScan() []nearbyDevice {
	nearbyMu.Lock()
	if nearbyBusy {
		nearbyMu.Unlock()
		for i := 0; i < 40; i++ { // another scan is running: wait for it
			time.Sleep(100 * time.Millisecond)
			nearbyMu.Lock()
			busy := nearbyBusy
			nearbyMu.Unlock()
			if !busy {
				break
			}
		}
		nearbyMu.Lock()
		defer nearbyMu.Unlock()
		return append([]nearbyDevice(nil), nearbyDevs...)
	}
	nearbyBusy = true
	nearbyMu.Unlock()

	me := selfDevice()
	found := map[string]nearbyDevice{}
	var mu sync.Mutex
	add := func(d nearbyDevice, ip string) {
		if d.ID == "" || d.ID == me.ID || d.Port <= 0 {
			return
		}
		d.IP = ip
		d.Name = cleanChatName(d.Name)
		if d.Name == "" {
			d.Name = ip
		}
		mu.Lock()
		if o, ok := found[d.ID]; !ok || (o.IP != ip && preferIP(ip, o.IP)) {
			found[d.ID] = d
		}
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); udpScan(me.ID, add) }()
	go func() { defer wg.Done(); httpSweep(add) }()
	wg.Wait()

	list := make([]nearbyDevice, 0, len(found))
	for _, d := range found {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	nearbyMu.Lock()
	nearbyDevs, nearbyScanAt, nearbyBusy = list, time.Now(), false
	nearbyMu.Unlock()
	return list
}

// preferIP tells whether address a beats b for the same device: an address
// on one of this PC's real LANs first, then loopback (a second copy on this
// PC), then anything else (virtual adapters).
func preferIP(a, b string) bool {
	return ipRank(a) > ipRank(b)
}

func ipRank(s string) int {
	ip := net.ParseIP(s)
	if ip == nil {
		return 0
	}
	for _, n := range lanNets() {
		if n.Contains(ip) {
			return 2
		}
	}
	if ip.IsLoopback() {
		return 1
	}
	return 0
}

func udpScan(myID string, add func(nearbyDevice, string)) {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return
	}
	defer pc.Close()
	msg := []byte("SM-NEARBY1 " + myID)
	targets := []string{"255.255.255.255", "127.0.0.1"}
	for _, b := range lanBroadcasts() {
		targets = append(targets, b)
	}
	send := func() {
		for _, t := range targets {
			pc.WriteTo(msg, &net.UDPAddr{IP: net.ParseIP(t), Port: nearbyUDPPort})
		}
	}
	send()
	deadline := time.Now().Add(1800 * time.Millisecond)
	resent := false
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		if !resent && time.Until(deadline) < 1200*time.Millisecond {
			send() // broadcasts get lost on busy Wi-Fi: ask twice
			resent = true
		}
		pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			continue
		}
		var d nearbyDevice
		if json.Unmarshal(buf[:n], &d) == nil {
			add(d, from.(*net.UDPAddr).IP.To4().String())
		}
	}
}

// lanBroadcasts lists the directed broadcast address of each LAN.
func lanBroadcasts() []string {
	var out []string
	for _, n := range lanNets() {
		ip := n.IP.To4()
		b := make(net.IP, 4)
		for i := range b {
			b[i] = ip[i] | ^n.Mask[i]
		}
		out = append(out, b.String())
	}
	return out
}

// lanNets lists this PC's private IPv4 networks (skipping virtual adapters).
func lanNets() []*net.IPNet {
	var out []*net.IPNet
	ifs, _ := net.Interfaces()
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		n := strings.ToLower(in.Name)
		if strings.Contains(n, "vethernet") || strings.Contains(n, "virtualbox") || strings.Contains(n, "vmware") || strings.Contains(n, "wsl") || strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "veth") {
			continue
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || !ipn.IP.IsPrivate() || len(ipn.Mask) == 0 {
				continue
			}
			m := ipn.Mask
			if len(m) == 16 {
				m = m[12:]
			}
			out = append(out, &net.IPNet{IP: ipn.IP.To4(), Mask: m})
		}
	}
	return out
}

// httpSweep asks every address of each local /24 (or smaller network) on the
// usual ShareMile port and on this PC's port.
func httpSweep(add func(nearbyDevice, string)) {
	ports := []int{8750}
	if publicPort != 8750 && publicPort > 0 {
		ports = append(ports, publicPort)
	}
	var hosts []string
	seen := map[string]bool{}
	for _, n := range lanNets() {
		ones, _ := n.Mask.Size()
		if ones < 24 {
			ones = 24 // never sweep more than 254 addresses per network
		}
		base := n.IP.Mask(net.CIDRMask(ones, 32)).To4()
		count := 1 << (32 - ones)
		for i := 1; i < count-1; i++ {
			ip := net.IPv4(base[0], base[1], base[2], base[3]+byte(i)).String()
			if !seen[ip] {
				seen[ip] = true
				hosts = append(hosts, ip)
			}
		}
	}
	hosts = append(hosts, "127.0.0.1") // another copy on this PC
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tr := &http.Transport{DialContext: (&net.Dialer{Timeout: 900 * time.Millisecond}).DialContext, DisableKeepAlives: true, MaxConnsPerHost: 1}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr}
	sem := make(chan struct{}, 300)
	var wg sync.WaitGroup
	for _, h := range hosts {
		for _, p := range ports {
			wg.Add(1)
			sem <- struct{}{}
			go func(h string, p int) {
				defer wg.Done()
				defer func() { <-sem }()
				req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(h, itoa(p))+"/nearby/hello", nil)
				resp, err := cl.Do(req)
				if err != nil {
					return
				}
				defer resp.Body.Close()
				var d nearbyDevice
				if resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&d) == nil {
					add(d, h)
				}
			}(h, p)
		}
	}
	wg.Wait()
}

// ---- sending -----------------------------------------------------------------

// nearbySend offers one of this PC's shares to a device found by a scan.
func nearbySend(shareID, devID string) (string, error) {
	sharesMu.RLock()
	var s *Share
	for _, x := range shares {
		if x.ID == shareID {
			s = x
		}
	}
	sharesMu.RUnlock()
	if s == nil {
		return "", errors.New("that share has stopped")
	}
	nearbyMu.Lock()
	var dev *nearbyDevice
	for i := range nearbyDevs {
		if nearbyDevs[i].ID == devID {
			d := nearbyDevs[i]
			dev = &d
		}
	}
	nearbyMu.Unlock()
	if dev == nil {
		return "", errors.New("that device isn't nearby any more. Scan again")
	}
	if isPausedAll() {
		return "", errors.New("sharing is paused. Resume it first")
	}
	o := nearbyOffer{ID: randToken(9), From: selfDevice(), Token: s.Token, Name: s.Name, Count: len(s.Files), Total: s.Total}
	b, _ := json.Marshal(o)
	out := &nearbyOut{ID: o.ID, Share: s.ID, Device: dev.Name, DevID: dev.ID, Status: "waiting", At: time.Now(), replyTo: dev.IP}
	resp, err := nearbyClient.Post("http://"+net.JoinHostPort(dev.IP, itoa(dev.Port))+"/nearby/offer", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("couldn't reach %s. Is ShareMile still open there?", dev.Name)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s didn't take the files (%s)", dev.Name, resp.Status)
	}
	nearbyMu.Lock()
	if len(nearbySent) > 50 {
		nearbySent = nearbySent[len(nearbySent)-50:]
	}
	nearbySent = append(nearbySent, out)
	nearbyMu.Unlock()
	return o.ID, nil
}

// nearbyAnswer accepts (downloads) or declines an incoming offer.
func nearbyAnswer(id string, accept bool) (string, error) {
	nearbyMu.Lock()
	var o *nearbyOffer
	kept := nearbyOffers[:0]
	for _, x := range nearbyOffers {
		if x.ID == id {
			o = x
		} else {
			kept = append(kept, x)
		}
	}
	nearbyOffers = kept
	nearbyMu.Unlock()
	if o == nil {
		return "", errors.New("that offer is gone")
	}
	base := "http://" + net.JoinHostPort(o.From.IP, itoa(o.From.Port))
	reply := func(ok bool) {
		b, _ := json.Marshal(map[string]any{"id": o.ID, "accepted": ok})
		if resp, err := nearbyClient.Post(base+"/nearby/reply", "application/json", bytes.NewReader(b)); err == nil {
			resp.Body.Close()
		}
	}
	if !accept {
		go reply(false)
		return "", nil
	}
	m, err := recvOpen(base + "/s/" + o.Token + "/")
	if err != nil {
		go reply(false)
		return "", fmt.Errorf("couldn't open the files from %s: %v", o.From.Name, err)
	}
	t, err := recvStart(m.Token, nil, "")
	if err != nil {
		go reply(false)
		return "", err
	}
	go reply(true)
	return t.ID, nil
}

// nearbyView is the state the UI shows.
func nearbyView() map[string]any {
	nearbyMu.Lock()
	defer nearbyMu.Unlock()
	offers := []*nearbyOffer{}
	for _, o := range nearbyOffers {
		if time.Since(o.At) < 10*time.Minute {
			offers = append(offers, o)
		}
	}
	sent := []*nearbyOut{}
	for _, s := range nearbySent {
		if s.Status == "waiting" && time.Since(s.At) > 10*time.Minute {
			s.Status = "failed"
			s.Err = "no answer"
		}
		sent = append(sent, s)
	}
	devs := nearbyDevs
	if devs == nil {
		devs = []nearbyDevice{}
	}
	return map[string]any{"visible": nearbyVisible(), "me": pcName(), "devices": devs, "scanning": nearbyBusy, "scannedAt": nearbyScanAt.UnixMilli(), "offers": offers, "sent": sent}
}
