package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// desktopEntry is ShareMile's .desktop file (app menu and desktop icon).
func desktopEntry() string {
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return "[Desktop Entry]\nType=Application\nName=ShareMile\nGenericName=File Sharing\n" +
		"Comment=Share files over the internet at full line speed\n" +
		"Exec=" + quoteExec(exe) + "\nIcon=sharemile\nTerminal=false\n" +
		"Categories=Network;FileTransfer;\nKeywords=share;send;receive;transfer;qr;\nStartupWMClass=sharemile\n"
}

func quoteExec(p string) string {
	if !strings.ContainsAny(p, " \t\"'\\$`") {
		return p
	}
	// Inside quotes, \ " ` $ need a backslash, and the file format then
	// doubles every backslash: a quote becomes \\" in the file.
	r := strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", `$`, `\\$`)
	return `"` + r.Replace(p) + `"`
}

// makeDesktopShortcut puts a ShareMile launcher on the desktop.
func makeDesktopShortcut() error {
	desk := ""
	if out, err := exec.Command("xdg-user-dir", "DESKTOP").Output(); err == nil {
		desk = strings.TrimSpace(string(out))
	}
	if desk == "" {
		home, _ := os.UserHomeDir()
		desk = filepath.Join(home, "Desktop")
	}
	if err := os.MkdirAll(desk, 0o755); err != nil {
		return err
	}
	p := filepath.Join(desk, "sharemile.desktop")
	if err := os.WriteFile(p, []byte(desktopEntry()), 0o755); err != nil {
		return err
	}
	// GNOME only starts desktop launchers marked as trusted.
	exec.Command("gio", "set", p, "metadata::trusted", "true").Run()
	return nil
}

// pinShortcut: Linux desktops have no pinning API; install.sh adds ShareMile
// to the app menu (and to GNOME's dock favourites when asked).
func pinShortcut(mode string) error { return nil }
