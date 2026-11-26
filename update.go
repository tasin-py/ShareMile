package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const repoSlug = "tasin-py/ShareMile"

type ReleaseInfo struct {
	Tag       string `json:"tag"`
	Notes     string `json:"notes"`
	ExeURL    string `json:"-"`
	SetupURL  string `json:"-"`
	Available bool   `json:"available"`
}

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

func newer(a, b string) bool { // a > b
	x, y := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

func checkUpdate() (*ReleaseInfo, error) {
	c := &http.Client{Timeout: 12 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+repoSlug+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	resp, err := c.Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach GitHub. Check your internet connection")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return &ReleaseInfo{Tag: "v" + appVersion}, nil
	}
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		return checkUpdateViaRedirect() // API rate limit: fall back to the website
	}
	if resp.StatusCode != 200 {
		return nil, errors.New("GitHub answered " + resp.Status + ". Try again later")
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, errors.New("unexpected reply from GitHub")
	}
	info := &ReleaseInfo{Tag: rel.Tag, Notes: rel.Body}
	for _, a := range rel.Assets {
		matchAsset(strings.ToLower(a.Name), a.URL, info)
	}
	info.Available = newer(rel.Tag, appVersion) && (info.ExeURL != "" || info.SetupURL != "")
	return info, nil
}

// checkUpdateViaRedirect reads the latest tag from github.com/<repo>/releases/latest,
// which redirects to /releases/tag/<tag> and isn't subject to API rate limits.
func checkUpdateViaRedirect() (*ReleaseInfo, error) {
	c := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", "https://github.com/"+repoSlug+"/releases/latest", nil)
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	resp, err := c.Do(req)
	if err != nil {
		return nil, errors.New("couldn't reach GitHub. Check your internet connection")
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if resp.StatusCode/100 != 3 || i < 0 {
		return &ReleaseInfo{Tag: "v" + appVersion}, nil
	}
	tag := loc[i+5:]
	base := "https://github.com/" + repoSlug + "/releases/download/" + tag + "/"
	info := &ReleaseInfo{Tag: tag}
	guessAssets(base, info)
	info.Available = newer(tag, appVersion)
	return info, nil
}

func download(url, dst string) error {
	c := &http.Client{Timeout: 15 * time.Minute}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", appName+"/"+appVersion)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("download failed: " + resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		return err
	}
	return nil
}

func dirWritable(dir string) bool {
	p := filepath.Join(dir, ".sm-write-test")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		return false
	}
	os.Remove(p)
	return true
}

func cleanupOldExe() {
	exe, _ := os.Executable()
	for i := 0; i < 10; i++ {
		if err := os.Remove(exe + ".old"); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func passthroughArgs() []string {
	var out []string
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if a == "--data" || a == "--port" {
			if i+1 < len(os.Args) {
				out = append(out, a, os.Args[i+1])
				i++
			}
		} else if strings.HasPrefix(a, "--data=") || strings.HasPrefix(a, "--port=") {
			out = append(out, a)
		}
	}
	return out
}
