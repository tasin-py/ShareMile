package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Linux firewalls: firewalld (Fedora, openSUSE…) and ufw (Ubuntu, Mint…).
// Most desktops ship with neither turned on, or with one that already lets
// the port in. "allowed" also covers "no firewall in the way".

func runOut(timeout time.Duration, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", err
	}
	cmd := exec.Command(name, args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return "", errors.New("timed out")
	}
	return strings.TrimSpace(string(out)), err
}

// portIn tells whether a firewalld port list ("8750/tcp 1025-65535/udp …") lets port in over TCP.
func portIn(list string, port int) bool {
	for _, p := range strings.Fields(list) {
		spec, proto, _ := strings.Cut(p, "/")
		if proto != "tcp" {
			continue
		}
		lo, hi, isRange := strings.Cut(spec, "-")
		a, err1 := strconv.Atoi(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(hi)
		}
		if err1 == nil && err2 == nil && port >= a && port <= b {
			return true
		}
	}
	return false
}

func firewalldOn() bool {
	out, err := runOut(5*time.Second, "firewall-cmd", "--state")
	return err == nil && out == "running"
}

func ufwOn() bool {
	b, err := os.ReadFile("/etc/ufw/ufw.conf")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(strings.ToLower(l)) == "enabled=yes" {
			_, err := exec.LookPath("ufw")
			return err == nil
		}
	}
	return false
}

func checkFirewallRule() {
	state := "allowed"
	port := publicPort
	switch {
	case firewalldOn():
		ports, _ := runOut(5*time.Second, "firewall-cmd", "--list-ports")
		svcs, _ := runOut(5*time.Second, "firewall-cmd", "--list-services")
		if !portIn(ports, port) && !strings.Contains(" "+svcs+" ", " sharemile ") {
			state = "missing"
		}
	case ufwOn():
		// ufw's rules are only readable by root; ShareMile's own rule file says
		// whether "Allow" was already used for this port.
		if b, err := os.ReadFile("/etc/ufw/user.rules"); err == nil {
			if !strings.Contains(string(b), "--dport "+strconv.Itoa(port)+" ") {
				state = "missing"
			}
		} else if !fwAllowedBefore(port) {
			state = "missing"
		}
	}
	fwMu.Lock()
	fwState = state
	fwMu.Unlock()
}

func fwAllowedBefore(port int) bool {
	b, _ := os.ReadFile(appDataDir() + "/.fw-allowed")
	return strings.TrimSpace(string(b)) == strconv.Itoa(port)
}

// allowFirewall opens ShareMile's port. pkexec asks for the admin password in
// the desktop's usual dialog.
func allowFirewall() error {
	if _, err := exec.LookPath("pkexec"); err != nil {
		return errors.New("pkexec isn't installed. Open TCP port " + strconv.Itoa(publicPort) + " in your firewall yourself")
	}
	p := strconv.Itoa(publicPort)
	var cmd *exec.Cmd
	switch {
	case firewalldOn():
		cmd = exec.Command("pkexec", "sh", "-c", "firewall-cmd --permanent --add-port="+p+"/tcp && firewall-cmd --reload")
	case ufwOn():
		cmd = exec.Command("pkexec", "ufw", "allow", p+"/tcp")
	default:
		return nil // nothing to change
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && (ee.ExitCode() == 126 || ee.ExitCode() == 127) {
			return errors.New("firewall change was cancelled")
		}
		return errors.New("the firewall didn't accept the change: " + strings.TrimSpace(string(out)))
	}
	os.WriteFile(appDataDir()+"/.fw-allowed", []byte(p), 0o600)
	return nil
}

const (
	fwLong  = "Your firewall"
	fwShort = "the firewall"
)
