package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Linux releases ship as ShareMile-linux-<arch>.tar.gz. The updater takes the
// program out of it and swaps it in place of the running one.

func linuxAsset() string { return "ShareMile-linux-" + runtime.GOARCH + ".tar.gz" }

func matchAsset(n, url string, info *ReleaseInfo) {
	if n == strings.ToLower(linuxAsset()) {
		info.ExeURL = url
	}
}

func guessAssets(base string, info *ReleaseInfo) { info.ExeURL = base + linuxAsset() }

// extractProgram copies the sharemile program out of a release tarball.
func extractProgram(tgz, dst string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("the downloaded update is damaged")
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return errors.New("the update doesn't contain the ShareMile program")
		}
		if err != nil {
			return errors.New("the downloaded update is damaged")
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "sharemile" {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, 512<<20))
		out.Close()
		if err != nil {
			os.Remove(dst)
			return err
		}
		b := make([]byte, 4)
		if fh, err := os.Open(dst); err == nil {
			fh.Read(b)
			fh.Close()
		}
		if string(b) != "\x7fELF" {
			os.Remove(dst)
			return errors.New("the downloaded update is not a valid program")
		}
		return nil
	}
}

// applyUpdate downloads the new version, swaps it in and relaunches.
func applyUpdate() error {
	info, err := checkUpdate()
	if err != nil {
		return err
	}
	if !info.Available {
		return errors.New("you already have the latest version")
	}
	if info.ExeURL == "" {
		return errors.New("this release has no Linux version yet")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	dir := filepath.Dir(exe)
	if !dirWritable(dir) {
		return errors.New("ShareMile is installed in " + dir + ", which only your package manager (or root) can change. Update it there, or reinstall with the install.sh from " + info.Tag)
	}
	tgz := filepath.Join(os.TempDir(), "sharemile-"+info.Tag+".tar.gz")
	defer os.Remove(tgz)
	if err := download(info.ExeURL, tgz); err != nil {
		return err
	}
	nw := exe + ".new"
	if err := extractProgram(tgz, nw); err != nil {
		return err
	}
	// Renaming over the running program is fine on Linux: it keeps running
	// from the old file until it exits.
	if err := os.Rename(nw, exe); err != nil {
		os.Remove(nw)
		return errors.New("couldn't replace the program: " + err.Error())
	}
	cmd := exec.Command(exe, append([]string{"--updated"}, passthroughArgs()...)...)
	if headless {
		cmd.Args = append(cmd.Args, "--headless")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Process.Release()
	go func() { time.Sleep(300 * time.Millisecond); quitApp() }()
	return nil
}
