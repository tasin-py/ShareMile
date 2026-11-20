package main

import (
	"encoding/hex"
	"errors"
	"strings"
)

// ---- New link for a Quick Share ------------------------------------------------

// refreshShareLink gives a share a new secret token. The old link stops working.
func refreshShareLink(id string) error {
	sharesMu.Lock()
	defer sharesMu.Unlock()
	for tok, s := range shares {
		if s.ID == id {
			delete(shares, tok)
			s.Token = randToken(12)
			shares[s.Token] = s
			return nil
		}
	}
	return errors.New("share not found")
}

// ---- New code for a hosted network -----------------------------------------------

// renewHostedNet replaces a network's secret key. Everyone who had the old code
// loses access until they get the new one.
func renewHostedNet(id string) (string, error) {
	key := newKey()
	swarmMu.Lock()
	h := hosted[id]
	if h == nil {
		swarmMu.Unlock()
		return "", errors.New("network not found")
	}
	delete(hosted, id)
	retireNet(id)
	h.mu.Lock()
	h.key = key
	h.id = netID(key)
	h.peers = map[string]*PeerInfo{}
	h.mu.Unlock()
	hosted[h.id] = h
	swarmMu.Unlock()
	cfgMu.Lock()
	h.cfg.Key = hex.EncodeToString(key)
	saveConfigLocked()
	cfgMu.Unlock()
	return h.id, nil
}

// retired network ids (old codes) so old members get a clear "code replaced"
// answer instead of counting as wrong guesses. Kept in config, capped.
func retireNet(id string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg.Retired = append(cfg.Retired, id)
	if len(cfg.Retired) > 200 {
		cfg.Retired = cfg.Retired[len(cfg.Retired)-200:]
	}
	saveConfigLocked()
}

func isRetiredNet(id string) bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	for _, r := range cfg.Retired {
		if r == id {
			return true
		}
	}
	return false
}

// ---- Bookmarks for networks you join -------------------------------------------------

type Bookmark struct {
	ID   string `json:"id"` // network id (from the key)
	Name string `json:"name"`
	Code string `json:"code"`
}

func bookmarkIndexLocked(id string) int {
	for i, b := range cfg.Bookmarks {
		if b.ID == id {
			return i
		}
	}
	return -1
}

// toggleBookmark saves (or removes) a network code so it stays in the app.
func toggleBookmark(code string, on bool) (bool, error) {
	c, err := decodeCode(code)
	if err != nil {
		return false, err
	}
	id := netID(c.Key)
	name := ""
	swarmMu.RLock()
	if j := joined[id]; j != nil {
		j.mu.Lock()
		if j.man != nil {
			name = j.man.Name
		}
		j.mu.Unlock()
	}
	swarmMu.RUnlock()
	cfgMu.Lock()
	defer cfgMu.Unlock()
	i := bookmarkIndexLocked(id)
	switch {
	case on && i < 0:
		if name == "" {
			name = "Network " + strings.ToUpper(id[:6])
		}
		cfg.Bookmarks = append(cfg.Bookmarks, &Bookmark{ID: id, Name: name, Code: strings.TrimSpace(code)})
	case on:
		cfg.Bookmarks[i].Code = strings.TrimSpace(code)
	case i >= 0:
		cfg.Bookmarks = append(cfg.Bookmarks[:i], cfg.Bookmarks[i+1:]...)
	}
	saveConfigLocked()
	return on, nil
}

// syncBookmark keeps a bookmark's name and code current while the network is joined.
func syncBookmark(id, name, code string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	i := bookmarkIndexLocked(id)
	if i < 0 {
		return
	}
	b := cfg.Bookmarks[i]
	changed := false
	if name != "" && b.Name != name {
		b.Name, changed = name, true
	}
	if code != "" && b.Code != code {
		b.Code, changed = code, true
	}
	if changed {
		saveConfigLocked()
	}
}

func bookmarksView() []map[string]any {
	cfgMu.Lock()
	bs := append([]*Bookmark(nil), cfg.Bookmarks...)
	cfgMu.Unlock()
	out := []map[string]any{}
	swarmMu.RLock()
	defer swarmMu.RUnlock()
	for _, b := range bs {
		out = append(out, map[string]any{"id": b.ID, "name": b.Name, "code": b.Code, "joined": joined[b.ID] != nil})
	}
	return out
}
