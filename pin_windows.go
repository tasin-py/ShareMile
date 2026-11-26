package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Pinning is used by the installer's finish page. Windows has no public
// pinning API, so we invoke the shell's own pin verbs on the Start-menu
// shortcut. Exit code 0 = pinned.

const pinScript = `
$ErrorActionPreference = 'Stop'
$target = $args[0]; $mode = $args[1]
$clsid = if ($mode -eq 'taskbar') { '{90AA3A4E-1CBA-4233-B8BB-535773D48449}' } else { '{a2a9545d-a0c2-42b4-9708-a0b2badd77c8}' }
$key = 'HKCU:\Software\Classes\*\shell\sharemilepin'
try {
  New-Item -Path $key -Force | Out-Null
  Set-ItemProperty -Path $key -Name ExplorerCommandHandler -Value $clsid
  $shell = New-Object -ComObject Shell.Application
  $folder = $shell.Namespace((Split-Path $target))
  $item = $folder.ParseName((Split-Path $target -Leaf))
  $item.InvokeVerb('sharemilepin')
} finally {
  Remove-Item -Path 'HKCU:\Software\Classes\*\shell\sharemilepin' -Recurse -Force -ErrorAction SilentlyContinue
}
`

// makeDesktopShortcut creates "ShareMile.lnk" on the user's desktop.
func makeDesktopShortcut() error {
	exe, _ := os.Executable()
	script := `$s=(New-Object -ComObject WScript.Shell).CreateShortcut([IO.Path]::Combine([Environment]::GetFolderPath('Desktop'),'ShareMile.lnk'));$s.TargetPath=$args[0];$s.WorkingDirectory=(Split-Path $args[0]);$s.IconLocation=$args[0]+',0';$s.Description='ShareMile - fast direct file sharing';$s.Save()`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "& {"+script+"}", exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}

// pinShortcut tries the shell pin verb (with a hard timeout, since Windows 11
// may block or ignore it). If the pin can't be confirmed, it falls back to
// opening the shortcut's folder with a one-line how-to, so the box always
// leads somewhere useful.
func pinShortcut(mode string) error {
	lnk := findShortcut()
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "& {"+pinScript+"}", lnk, mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	err := cmd.Start()
	if err == nil {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-time.After(15 * time.Second):
			cmd.Process.Kill()
			err = errors.New("timed out")
		}
	}
	exec.Command("reg", "delete", `HKCU\Software\Classes\*\shell\sharemilepin`, "/f").Run()
	if err == nil && (mode != "taskbar" || isPinnedToTaskbar()) {
		return nil
	}
	where := "taskbar"
	if mode != "taskbar" {
		where = "Start"
	}
	revealPath(lnk)
	messageBox(appName, "Windows didn't allow ShareMile to pin itself to "+where+".\n\nIn the folder that just opened, right-click \"ShareMile\" and choose \"Pin to "+where+"\" (on Windows 11: Show more options first).", 0x40)
	return nil
}

func isPinnedToTaskbar() bool {
	dir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Internet Explorer", "Quick Launch", "User Pinned", "TaskBar")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name()), "sharemile") {
			return true
		}
	}
	return false
}

// findShortcut returns the Start-menu shortcut created by the installer, or the exe itself.
func findShortcut() string {
	for _, base := range []string{os.Getenv("APPDATA"), os.Getenv("PROGRAMDATA")} {
		p := filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", "ShareMile", "ShareMile.lnk")
		if fileExists(p) {
			return p
		}
		p = filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs", "ShareMile.lnk")
		if fileExists(p) {
			return p
		}
	}
	exe, _ := os.Executable()
	return exe
}
