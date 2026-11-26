package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func checkFirewallRule() {
	cmd := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name=ShareMile")
	hideCmd(cmd)
	err := cmd.Run()
	fwMu.Lock()
	if err == nil {
		fwState = "allowed"
	} else {
		fwState = "missing"
	}
	fwMu.Unlock()
}

// allowFirewall adds an inbound rule for ShareMile (asks for admin rights).
func allowFirewall() error {
	exe, _ := os.Executable()
	args := `advfirewall firewall add rule name="ShareMile" dir=in action=allow program="` + exe + `" enable=yes profile=any`
	if err := shellExec("runas", "netsh.exe", args); err != nil {
		return errors.New("firewall change was cancelled")
	}
	return nil
}

// hideCmd keeps a helper program's console window from flashing up.
func hideCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

const (
	fwLong  = "Windows Firewall"
	fwShort = "Windows Firewall"
)
