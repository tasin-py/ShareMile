package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func matchAsset(n, url string, info *ReleaseInfo) {
	switch {
	case n == "sharemile-portable.exe" || n == "sharemile.exe": // portable build (old releases: ShareMile.exe)
		info.ExeURL = url
	case strings.HasPrefix(n, "sharemile-setup") && strings.HasSuffix(n, ".exe"):
		info.SetupURL = url
	}
}

func guessAssets(base string, info *ReleaseInfo) {
	info.ExeURL, info.SetupURL = base+"ShareMile-Portable.exe", base+"ShareMile-Setup.exe"
}

// checkExe makes sure a download is a Windows program, not an error page.
func checkExe(p string) error {
	b := make([]byte, 2)
	if fh, err := os.Open(p); err == nil {
		fh.Read(b)
		fh.Close()
	}
	if string(b) != "MZ" {
		os.Remove(p)
		return errors.New("the downloaded update is not a valid program")
	}
	return nil
}

func downloadExe(url, dst string) error {
	if err := download(url, dst); err != nil {
		return err
	}
	return checkExe(dst)
}

// applyUpdate downloads the new version, swaps it in and relaunches. The
// returned function must be called to quit the current process.
func applyUpdate() error {
	info, err := checkUpdate()
	if err != nil {
		return err
	}
	if !info.Available {
		return errors.New("you already have the latest version")
	}
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	if info.ExeURL != "" && dirWritable(dir) {
		nw := exe + ".new"
		err := downloadExe(info.ExeURL, nw)
		if err != nil && strings.HasSuffix(info.ExeURL, "/ShareMile-Portable.exe") {
			// releases before v1.2.0 named the portable file ShareMile.exe
			err = downloadExe(strings.TrimSuffix(info.ExeURL, "ShareMile-Portable.exe")+"ShareMile.exe", nw)
		}
		if err != nil {
			return err
		}
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(nw)
			return errors.New("couldn't replace the running program: " + err.Error())
		}
		if err := os.Rename(nw, exe); err != nil {
			os.Rename(old, exe)
			return err
		}
		cmd := exec.Command(exe, append([]string{"--updated"}, passthroughArgs()...)...)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { time.Sleep(300 * time.Millisecond); quitApp() }()
		return nil
	}
	if info.SetupURL == "" {
		return errors.New("this release has no installer for your setup")
	}
	setup := filepath.Join(os.TempDir(), "ShareMile-Setup-"+info.Tag+".exe")
	if err := downloadExe(info.SetupURL, setup); err != nil {
		return err
	}
	// The installer closes this app, installs silently and relaunches it.
	if err := shellExec("runas", setup, "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /CLOSEAPPLICATIONS /RELAUNCH=1"); err != nil {
		return err
	}
	go func() { time.Sleep(300 * time.Millisecond); quitApp() }()
	return nil
}
