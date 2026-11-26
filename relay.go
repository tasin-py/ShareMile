package main

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The relay is an optional Cloudflare quick tunnel ("cloudflared") that makes
// links work even behind CGNAT. Direct links are always preferred.

var (
	relayAuto bool // relay was started automatically (and may be stopped automatically)
	relayCmd  *exec.Cmd
	relayRe   = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
)

const cloudflaredBase = "https://github.com/cloudflare/cloudflared/releases/latest/download/"

func cloudflaredPath() string {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	exe, _ := os.Executable()
	if p := filepath.Join(filepath.Dir(exe), cloudflaredFile); fileExists(p) {
		return p
	}
	p := filepath.Join(appDataDir(), "bin", cloudflaredFile)
	if fileExists(p) {
		return p
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func setRelay(state, url, e string) {
	netMu.Lock()
	netState.RelayState, netState.RelayURL, netState.RelayErr = state, url, e
	netMu.Unlock()
}

func downloadCloudflared() (string, error) {
	setRelay("downloading", "", "")
	dir := filepath.Join(appDataDir(), "bin")
	os.MkdirAll(dir, 0o755)
	dst := filepath.Join(dir, cloudflaredFile)
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Get(cloudflaredBase + cloudflaredAsset())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("download failed: " + resp.Status)
	}
	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(dst + ".part")
		return "", err
	}
	return dst, os.Rename(dst+".part", dst)
}

// relayGen changes every time the relay is (re)started or stopped, so a start
// that is still downloading or connecting gives up instead of running a
// second tunnel next to the new one.
var (
	relayMu  sync.Mutex
	relayGen int64
	dlMu     sync.Mutex // one cloudflared download at a time
)

func relayCurrent(gen int64) bool {
	relayMu.Lock()
	defer relayMu.Unlock()
	return gen == relayGen
}

// setRelayFor updates the relay state only for the current start.
func setRelayFor(gen int64, state, u, e string) {
	relayMu.Lock()
	defer relayMu.Unlock()
	if gen == relayGen {
		setRelay(state, u, e)
	}
}

func startRelay() {
	stopRelay()
	relayMu.Lock()
	gen := relayGen
	relayMu.Unlock()
	go func() {
		dlMu.Lock()
		p := cloudflaredPath()
		if p == "" && relayCurrent(gen) {
			var err error
			if p, err = downloadCloudflared(); err != nil {
				dlMu.Unlock()
				setRelayFor(gen, "error", "", "Couldn't download the relay helper: "+err.Error())
				return
			}
		}
		dlMu.Unlock()
		if p == "" || !relayCurrent(gen) {
			return
		}
		setRelayFor(gen, "starting", "", "")
		cmd := exec.Command(p, "tunnel", "--no-autoupdate", "--url", "http://localhost:"+itoa(publicPort))
		hideCmd(cmd)
		stderr, _ := cmd.StderrPipe()
		relayMu.Lock()
		if gen != relayGen {
			relayMu.Unlock()
			return
		}
		if err := cmd.Start(); err != nil {
			relayMu.Unlock()
			setRelayFor(gen, "error", "", err.Error())
			return
		}
		relayCmd = cmd
		relayMu.Unlock()
		found := make(chan string, 1)
		ended := make(chan string, 1) // cloudflared quit before giving a link
		go func() {
			sc := bufio.NewScanner(stderr)
			last, limited, gotURL := "", false, false
			for sc.Scan() {
				line := sc.Text()
				if m := relayRe.FindString(line); m != "" {
					gotURL = true
					select {
					case found <- m:
					default:
					}
				}
				if strings.Contains(line, "status 429") || strings.Contains(line, "Too Many Requests") {
					limited = true
				}
				if strings.Contains(line, " ERR ") || strings.Contains(line, "failed") {
					last = line
				}
			}
			if gotURL {
				return
			}
			switch {
			case limited:
				ended <- "Cloudflare is limiting new relay links from your connection right now. Try again in a few minutes."
			case last != "":
				ended <- "The relay couldn't connect: " + relayErrText(last)
			default:
				ended <- "The relay helper stopped before it connected."
			}
		}()
		select {
		case u := <-found:
			// Publish the link only once public DNS knows the new address, so
			// receivers' ISPs don't cache "does not exist" for it.
			if h, err := url.Parse(u); err == nil {
				waitPublicDNS(h.Hostname(), 25*time.Second)
			}
			setRelayFor(gen, "on", u, "")
		case msg := <-ended:
			setRelayFor(gen, "error", "", msg)
		case <-time.After(40 * time.Second):
			if relayCurrent(gen) {
				setRelayFor(gen, "error", "", "The relay didn't start in time. Check your internet connection.")
				cmd.Process.Kill()
			}
		}
		cmd.Wait()
		relayMu.Lock()
		if gen == relayGen && relayCmd == cmd {
			relayCmd = nil
			if getNet().RelayState == "on" {
				setRelay("error", "", "Relay stopped unexpectedly.")
			}
		}
		relayMu.Unlock()
	}()
}

// relayErrText turns a cloudflared log line into a short reason:
// "2026-01-02T03:04:05Z ERR failed to ... error=..." -> "failed to ...".
func relayErrText(line string) string {
	if i := strings.Index(line, " ERR "); i >= 0 {
		line = line[i+5:]
	} else if f := strings.Fields(line); len(f) > 2 && strings.HasSuffix(f[0], "Z") {
		line = strings.Join(f[2:], " ")
	}
	line = strings.TrimSpace(line)
	if len(line) > 160 {
		line = line[:160] + "…"
	}
	return line
}

func stopRelay() {
	relayMu.Lock()
	defer relayMu.Unlock()
	relayGen++
	if relayCmd != nil && relayCmd.Process != nil {
		relayCmd.Process.Kill()
	}
	relayCmd = nil
	setRelay("off", "", "")
}
