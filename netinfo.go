package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
)

// NetState describes how this PC can be reached.
type NetState struct {
	LanIPs     []string `json:"lanIPs"`
	IPv6       string   `json:"ipv6"`
	PublicIP   string   `json:"publicIP"`
	RouterWAN  string   `json:"routerWAN"`
	UPnP       string   `json:"upnp"` // off|searching|mapped|failed|none
	UPnPErr    string   `json:"upnpErr"`
	RealIP     string   `json:"realIP"` // yes|no|unknown: does this connection have a real (non-CGNAT) public IP?
	PublicOK   bool     `json:"publicOK"`
	BDIX       string   `json:"bdix"`    // ""|checking|ready|noRealIP|needForward|firewall|error
	BDIXMsg    string   `json:"bdixMsg"` // explanation / guide for the BDIX Transfer tab
	Checked    bool     `json:"checked"`
	RelayURL   string   `json:"relayURL"`
	RelayState string   `json:"relayState"` // off|starting|on|error|missing|downloading
	RelayErr   string   `json:"relayErr"`
}

var (
	netState   = NetState{UPnP: "off", RelayState: "off"}
	netMu      sync.RWMutex
	upnpClient interface {
		AddPortMapping(string, uint16, string, uint16, string, bool, string, uint32) error
		DeletePortMapping(string, uint16, string) error
		GetExternalIPAddress() (string, error)
	}
	upnpPort uint16
)

func getNet() NetState {
	netMu.RLock()
	defer netMu.RUnlock()
	s := netState
	s.LanIPs = append([]string(nil), netState.LanIPs...)
	return s
}

func isCGNAT(ip net.IP) bool {
	_, cg, _ := net.ParseCIDR("100.64.0.0/10")
	return ip != nil && (cg.Contains(ip) || ip.IsPrivate())
}

func localAddrs() (lan []string, v6 string) {
	// Prefer the interface that carries the default route.
	if c, err := net.Dial("udp4", "1.1.1.1:80"); err == nil {
		lan = append(lan, c.LocalAddr().(*net.UDPAddr).IP.String())
		c.Close()
	}
	if c, err := net.Dial("udp6", "[2606:4700:4700::1111]:80"); err == nil {
		ip := c.LocalAddr().(*net.UDPAddr).IP
		if ip.IsGlobalUnicast() && !ip.IsPrivate() {
			v6 = ip.String()
		}
		c.Close()
	}
	ifs, _ := net.Interfaces()
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		n := strings.ToLower(in.Name)
		if strings.Contains(n, "vethernet") || strings.Contains(n, "virtualbox") || strings.Contains(n, "vmware") || strings.Contains(n, "wsl") {
			continue
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			if ip4 := ip.To4(); ip4 != nil {
				if ip4.IsPrivate() && !contains(lan, ip4.String()) {
					lan = append(lan, ip4.String())
				}
			}
		}
	}
	return
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func fetchText(url string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "ShareMile")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return strings.TrimSpace(string(b))
}

// refreshNetwork re-detects addresses, public IP and UPnP mapping.
var refreshMu sync.Mutex

// needsInternet reports whether anything is being offered to others right now.
// The router port is only opened while this is true.
func needsInternet() bool {
	sharesMu.RLock()
	busy := len(shares) > 0
	sharesMu.RUnlock()
	swarmMu.RLock()
	defer swarmMu.RUnlock()
	for _, h := range hosted {
		if h.cfg.Active {
			busy = true
		}
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	for _, j := range joined {
		if j.cfg.Seed && len(j.cfg.Have) > 0 {
			busy = true
		}
	}
	return busy
}

// syncExposure opens or closes the router port (and the auto relay) to match
// what is currently shared. Call it whenever shares start or stop.
func syncExposure() {
	maybeAutoRelay() // start (or stop) the relay right away; it doesn't depend on the checks below
	refreshNetwork()
}

func refreshNetwork() {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	lan, v6 := localAddrs()
	cfgMu.Lock()
	useUPnP := cfg.UseUPnP
	cfgMu.Unlock()
	want := useUPnP && needsInternet()
	netMu.Lock()
	netState.LanIPs, netState.IPv6 = lan, v6
	switch {
	case !useUPnP:
		netState.UPnP = "off"
	case !want:
		netState.UPnP = "idle"
	default:
		netState.UPnP = "searching"
	}
	netMu.Unlock()
	if !want {
		unmapUPnP()
	}

	var wg sync.WaitGroup
	routerAnswered := false
	if useUPnP && !want {
		// Not sharing: don't open anything, just ask the router which address it holds.
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cs := gatewayClients(ctx)
			routerAnswered = len(cs) > 0
			if len(cs) > 0 {
				wan, _ := cs[0].GetExternalIPAddress()
				netMu.Lock()
				netState.RouterWAN = wan
				netMu.Unlock()
			}
		}()
	}
	var pub string
	wg.Add(1)
	go func() {
		defer wg.Done()
		pub = fetchText("https://api.ipify.org", 6*time.Second)
		if net.ParseIP(pub) == nil {
			pub = ""
		}
	}()
	if want && len(lan) > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); mapUPnP(lan[0], uint16(publicPort)) }()
	}
	wg.Wait()

	netMu.Lock()
	if pub != netState.PublicIP {
		netState.BDIX, netState.BDIXMsg = "", "" // new address: the old check no longer applies
	}
	netState.PublicIP = pub
	netState.Checked = true
	if netState.UPnP == "mapped" || netState.UPnP == "failed" {
		routerAnswered = true
	}
	// Real IP = the router itself holds the public address. If it holds a
	// private/shared one (or none), the ISP is doing NAT (CGNAT) and nobody
	// outside can connect in, whatever is forwarded at home.
	wan := net.ParseIP(netState.RouterWAN)
	switch {
	case pub == "":
		netState.RealIP = "unknown"
	case wan != nil && !isCGNAT(wan) && wan.String() == pub:
		netState.RealIP = "yes"
	case wan != nil || routerAnswered:
		netState.RealIP = "no"
	default:
		netState.RealIP = "unknown"
	}
	// An actual test from outside beats any guess.
	if probe.ip == pub && probe.real != "" {
		netState.RealIP = probe.real
	}
	netState.PublicOK = pub != "" && netState.UPnP == "mapped" && netState.RealIP == "yes"
	netMu.Unlock()
	maybeAutoRelay()
}

// maybeAutoRelay keeps the relay running while something is shared. The relay
// link is the default because it works on every connection (CGNAT included);
// BDIX Transfer (direct) is offered next to it when the connection allows.
func maybeAutoRelay() {
	cfgMu.Lock()
	auto := cfg.AutoRelay != nil && *cfg.AutoRelay
	cfgMu.Unlock()
	n := getNet()
	busy := needsInternet()
	if relayAuto && !busy && n.RelayState != "off" {
		stopRelay() // nothing shared any more: close the auto-started tunnel too
		return
	}
	if !auto || !busy || n.RelayState != "off" {
		return
	}
	startRelay()
	relayAuto = true
}

type gwClient interface {
	AddPortMapping(string, uint16, string, uint16, string, bool, string, uint32) error
	DeletePortMapping(string, uint16, string) error
	GetExternalIPAddress() (string, error)
}

// gatewayClients finds the router's UPnP services.
func gatewayClients(ctx context.Context) []gwClient {
	var clients []gwClient
	if cs, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx); err == nil {
		for _, c := range cs {
			clients = append(clients, c)
		}
	}
	if cs, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(ctx); err == nil {
		for _, c := range cs {
			clients = append(clients, c)
		}
	}
	if cs, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx); err == nil {
		for _, c := range cs {
			clients = append(clients, c)
		}
	}
	return clients
}

func mapUPnP(lanIP string, port uint16) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	set := func(state, e string) {
		netMu.Lock()
		netState.UPnP, netState.UPnPErr = state, e
		netMu.Unlock()
	}
	clients := gatewayClients(ctx)
	if len(clients) == 0 {
		set("none", "Router did not answer UPnP. Enable UPnP on the router or forward the port manually.")
		return
	}
	var lastErr error
	for _, c := range clients {
		wan, _ := c.GetExternalIPAddress()
		netMu.Lock()
		netState.RouterWAN = wan
		netMu.Unlock()
		err := c.AddPortMapping("", port, "TCP", port, lanIP, true, "ShareMile", 3600) // short lease: a crash can't leave it open for long
		if err == nil {
			upnpClient, upnpPort = c, port
			set("mapped", "")
			return
		}
		lastErr = err
	}
	set("failed", "Router refused the port mapping: "+lastErr.Error())
}

func unmapUPnP() {
	if upnpClient != nil {
		upnpClient.DeletePortMapping("", upnpPort, "TCP")
		upnpClient = nil
	}
}

// netWatcher keeps the mapping alive and notices IP changes.
func netWatcher() {
	refreshNetwork()
	t := time.NewTicker(10 * time.Minute) // dynamic IPs change; keep links current
	for range t.C {
		refreshNetwork()
	}
}

// Link endpoints --------------------------------------------------------------

type Endpoint struct {
	Kind  string `json:"kind"` // public|ipv6|lan|relay
	Label string `json:"label"`
	Base  string `json:"base"` // e.g. http://1.2.3.4:8750
	Note  string `json:"note"`
}

func hostPort(ip string, port int) string {
	return net.JoinHostPort(ip, itoa(port))
}

// endpoints lists the links offered for a share, in display order:
// Relay (default, works everywhere), BDIX Transfer (direct), IPv6, LAN.
// A link with an empty Base isn't usable yet (relay starting, etc.).
func endpoints() []Endpoint {
	n := getNet()
	var eps []Endpoint
	relayNote := "Works for anyone, anywhere, even when your ISP blocks incoming connections. Encrypted (HTTPS). Files stream from your PC through Cloudflare; nothing is stored there."
	switch {
	case n.RelayURL != "":
		eps = append(eps, Endpoint{"relay", "Relay", n.RelayURL, relayNote})
	case n.RelayState == "starting" || n.RelayState == "downloading":
		eps = append(eps, Endpoint{"relay", "Relay", "", "Starting the relay. This takes a few seconds…"})
	case n.RelayState == "error":
		eps = append(eps, Endpoint{"relay", "Relay", "", n.RelayErr + " Turn it on again in Settings → Relay."})
	}
	if n.PublicIP != "" {
		eps = append(eps, Endpoint{"public", "BDIX Transfer", "http://" + hostPort(n.PublicIP, publicPort),
			"Direct from your PC at full speed, and BDIX speed for receivers on BD ISPs. Plain HTTP, not encrypted."})
	}
	if n.IPv6 != "" {
		eps = append(eps, Endpoint{"ipv6", "Internet IPv6 (direct)", "http://" + hostPort(n.IPv6, publicPort), "Direct over IPv6. Works if the receiver's ISP also has IPv6."})
	}
	for _, ip := range n.LanIPs {
		eps = append(eps, Endpoint{"lan", "Same Wi-Fi / LAN", "http://" + hostPort(ip, publicPort), "Fastest. Only for devices on the same network as you."})
	}
	return eps
}

func firstOr(l []string, d string) string {
	if len(l) > 0 {
		return l[0]
	}
	return d
}

// bestEndpoint picks the preferred link according to the configured mode.
// Automatic = Relay first (it always works), then the rest in order.
func bestEndpoint(eps []Endpoint) *Endpoint {
	mode := cfg.LinkMode
	pick := func(kind string) *Endpoint {
		for i := range eps {
			if eps[i].Kind == kind && eps[i].Base != "" {
				return &eps[i]
			}
		}
		return nil
	}
	if mode != "auto" {
		if e := pick(mode); e != nil {
			return e
		}
	}
	for _, k := range []string{"relay", "public", "ipv6", "lan"} {
		if e := pick(k); e != nil {
			if k == "public" && !getNet().PublicOK {
				continue
			}
			return e
		}
	}
	if len(eps) > 0 {
		return &eps[0]
	}
	return nil
}
