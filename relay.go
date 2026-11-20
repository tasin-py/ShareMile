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
	"syscall"
	"time"
)

// The relay is an optional Cloudflare quick tunnel ("cloudflared") that makes
// links work even behind CGNAT. Direct links are always preferred.

var (
	relayAuto bool // relay was started automatically (and may be stopped automatically)
	relayCmd  *exec.Cmd
	relayRe   = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
)

const cloudflaredURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe"

func cloudflaredPath() string {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	exe, _ := os.Executable()
	if p := filepath.Join(filepath.Dir(exe), "cloudflared.exe"); fileExists(p) {
		return p
	}
	p := filepath.Join(appDataDir(), "bin", "cloudflared.exe")
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
	dst := filepath.Join(dir, "cloudflared.exe")
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Get(cloudflaredURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("download failed: " + resp.Status)
	}
	f, err := os.Create(dst + ".part")
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

func startRelay() {
	stopRelay()
	go func() {
		p := cloudflaredPath()
		if p == "" {
			var err error
			if p, err = downloadCloudflared(); err != nil {
				setRelay("error", "", "Couldn't download the relay helper: "+err.Error())
				return
			}
		}
		setRelay("starting", "", "")
		cmd := exec.Command(p, "tunnel", "--no-autoupdate", "--url", "http://localhost:"+itoa(publicPort))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		stderr, _ := cmd.StderrPipe()
		if err := cmd.Start(); err != nil {
			setRelay("error", "", err.Error())
			return
		}
		relayCmd = cmd
		found := make(chan string, 1)
		go func() {
			sc := bufio.NewScanner(stderr)
			for sc.Scan() {
				if m := relayRe.FindString(sc.Text()); m != "" {
					select {
					case found <- m:
					default:
					}
				}
			}
		}()
		select {
		case u := <-found:
			// Publish the link only once public DNS knows the new address, so
			// receivers' ISPs don't cache "does not exist" for it.
			if h, err := url.Parse(u); err == nil {
				waitPublicDNS(h.Hostname(), 25*time.Second)
			}
			if relayCmd != cmd {
				return // stopped meanwhile
			}
			setRelay("on", u, "")
		case <-time.After(40 * time.Second):
			setRelay("error", "", "The relay didn't start in time. Check your internet connection.")
			stopRelay()
			return
		}
		cmd.Wait()
		if relayCmd == cmd {
			setRelay("error", "", "Relay stopped unexpectedly.")
			relayCmd = nil
		}
	}()
}

func stopRelay() {
	if relayCmd != nil && relayCmd.Process != nil {
		c := relayCmd
		relayCmd = nil
		c.Process.Kill()
	}
	setRelay("off", "", "")
}
