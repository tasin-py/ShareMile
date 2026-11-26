package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// HostedNetCfg is a persisted share network that this PC hosts.
type HostedNetCfg struct {
	Key     string   `json:"key"` // hex, 10 bytes
	Name    string   `json:"name"`
	Roots   []string `json:"roots"`
	Active  bool     `json:"active"`
	Method  string   `json:"method,omitempty"`  // how members reach this PC: auto|relay|public|lan|ipv6
	Exclude []string `json:"exclude,omitempty"` // files (rel paths) the host removed from the network
	ChatID  string   `json:"chatId,omitempty"`  // name of the saved group chat file
}

// JoinedNetCfg is a persisted share network this PC has joined.
type JoinedNetCfg struct {
	Key       string            `json:"key"`
	Name      string            `json:"name"`
	Code      string            `json:"code"`
	Endpoints []string          `json:"endpoints"`
	Have      map[string]string `json:"have"` // fileID -> absolute path on disk
	Seed      bool              `json:"seed"`
}

type Config struct {
	Port          int             `json:"port"`
	DownloadDir   string          `json:"downloadDir"`
	Theme         string          `json:"theme"`
	UseUPnP       bool            `json:"useUpnp"`
	LinkMode      string          `json:"linkMode"` // auto|public|ipv6|lan|relay
	AutoRelay     *bool           `json:"autoRelay"`
	Networks      []*HostedNetCfg `json:"networks"`
	Joined        []*JoinedNetCfg `json:"joined"`
	Bookmarks     []*Bookmark     `json:"bookmarks"`
	Retired       []string        `json:"retired,omitempty"` // ids of network codes this PC replaced
	ChatName      string          `json:"chatName,omitempty"`
	Background    *bool           `json:"background,omitempty"` // keep running in the tray when the window closes (default on)
	TrayHintShown bool            `json:"trayHintShown,omitempty"`
	ChatKey       string          `json:"chatKey,omitempty"`    // private: proves this PC's chat identity
	PhoneOn       bool            `json:"phoneOn,omitempty"`    // Receive from phone is on
	PhoneToken    string          `json:"phoneToken,omitempty"` // the /p/<token>/ link phones send files to
}

var (
	cfg   *Config
	cfgMu sync.Mutex
)

func appDataDir() string {
	if dataDirOverride != "" {
		os.MkdirAll(dataDirOverride, 0o755)
		return dataDirOverride
	}
	d := ""
	if windowsNames {
		d = os.Getenv("APPDATA")
	}
	if d == "" {
		d, _ = os.UserConfigDir()
	}
	p := filepath.Join(d, "ShareMile")
	os.MkdirAll(p, 0o755)
	return p
}

func defaultDownloadDir() string {
	return filepath.Join(downloadsFolder(), "ShareMile")
}

func loadConfig() {
	c := &Config{Port: 8750, Theme: "pitch-black", UseUPnP: true, LinkMode: "auto", DownloadDir: defaultDownloadDir()}
	if b, err := os.ReadFile(filepath.Join(appDataDir(), "config.json")); err == nil {
		json.Unmarshal(b, c)
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = 8750
	}
	if c.DownloadDir == "" {
		c.DownloadDir = defaultDownloadDir()
	}
	if c.AutoRelay == nil {
		t := true
		c.AutoRelay = &t
	}
	for _, j := range c.Joined {
		if j.Have == nil {
			j.Have = map[string]string{}
		}
	}
	cfg = c
}

// saveConfig writes the config atomically. Callers must hold cfgMu.
func saveConfigLocked() {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	p := filepath.Join(appDataDir(), "config.json")
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, p)
	}
}

func saveConfig() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	saveConfigLocked()
}
