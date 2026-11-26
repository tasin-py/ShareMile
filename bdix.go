package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// BDIX Transfer check: can people on the internet reach this PC directly?
// It answers two questions for the user:
//   1. Does the connection have a real public IP (or is the ISP doing CGNAT)?
//   2. If it does, is the port forwarded so the direct link opens?
// The port is tested from outside with portchecker.io (it only learns the
// public IP and port number, which every receiver sees anyway).

var probe struct {
	ip   string // public IP the last outside test was for
	real string // "yes"|"no" learned from that test
}

func setBDIX(state, msg string) {
	netMu.Lock()
	netState.BDIX, netState.BDIXMsg = state, msg
	netMu.Unlock()
}

// portOpenFromOutside asks an outside service to connect to ip:port.
func portOpenFromOutside(ip string, port int) (bool, error) {
	body, _ := json.Marshal(map[string]any{"host": ip, "ports": []int{port}})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://portchecker.io/api/v1/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ShareMile")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Error bool `json:"error"`
		Check []struct {
			Port   int  `json:"port"`
			Status bool `json:"status"`
		} `json:"check"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Error || len(out.Check) == 0 {
		return false, errors.New("the online port tester gave no answer")
	}
	return out.Check[0].Status, nil
}

func startBDIXCheck() {
	if getNet().BDIX == "checking" {
		return
	}
	setBDIX("checking", "Testing from the internet whether people can reach this PC directly…")
	go runBDIXCheck()
}

func runBDIXCheck() {
	if !needsInternet() {
		setBDIX("error", "Start a share first, then check again.")
		return
	}
	refreshNetwork() // make sure the router port is open right now
	n := getNet()
	if n.PublicIP == "" {
		setBDIX("error", "No internet connection was detected. Check your connection and try again.")
		return
	}
	port := itoa(publicPort)
	lan := firstOr(n.LanIPs, "this PC")
	open, err := portOpenFromOutside(n.PublicIP, publicPort)
	fwMu.Lock()
	fw := fwState
	fwMu.Unlock()

	learn := func(real string) {
		netMu.Lock()
		probe.ip, probe.real = n.PublicIP, real
		netState.RealIP = real
		netState.PublicOK = real == "yes" && open
		netMu.Unlock()
	}
	noReal := "Your ISP shares one public IP between many customers (CGNAT), so nobody on the internet can connect to your PC directly, whatever you set in your router. Ask your ISP to enable a real IP (most BD ISPs offer it), then check again. Until then, send the Relay link."
	forward := "Your connection has a real IP, but port " + port + " isn't reachable yet. In your router, either turn on UPnP, or add a port forward: TCP " + port + " → " + lan + " (port " + port + "). Reserve " + lan + " for this PC so it doesn't change, then check again."

	switch {
	case err == nil && open:
		learn("yes")
		setBDIX("ready", "")
	case n.RealIP == "no" && n.RouterWAN != "":
		// The router itself reports a private/shared address: no outside test needed.
		learn("no")
		setBDIX("noRealIP", noReal)
	case err != nil:
		setBDIX("error", "Couldn't run the outside test ("+err.Error()+"). Check again in a minute.")
	case fw == "missing":
		setBDIX("firewall", fwLong+" is blocking incoming connections to ShareMile. Go to Settings → Allow through "+fwShort+", then check again.")
	case n.RealIP == "yes":
		learn("yes")
		setBDIX("needForward", forward)
	case n.UPnP == "mapped":
		// The router accepted the port forward and the firewall is open, yet the
		// outside can't reach us: the block is at the ISP, i.e. no real IP.
		learn("no")
		setBDIX("noRealIP", noReal)
	default:
		setBDIX("needForward", forward+" If it still fails after that, your ISP hasn't given you a real IP.")
	}
	netMu.Lock()
	netState.PublicOK = netState.BDIX == "ready"
	netMu.Unlock()
}
