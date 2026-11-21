package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Chat for Quick Shares (sharer + everyone with the link) and Share Networks
// (group chat for the host and members). The chat lives on the sharer's /
// host's PC; others poll it. Each person proves who they are with a private
// random key: their public id is a hash of it, so nobody can post or react as
// someone else.

const (
	chatKeep    = 300  // messages kept per chat
	chatMaxText = 2000 // characters per message
	chatOnline  = 30 * time.Second
)

var chatEmojis = []string{"👍", "❤️", "😂", "😮", "😢", "🙏", "🔥", "🎉"}

type ChatMsg struct {
	ID       int64               `json:"id"`
	From     string              `json:"from"`
	FromID   string              `json:"fromId"`
	Host     bool                `json:"host,omitempty"`
	Text     string              `json:"text"`
	Reply    int64               `json:"reply,omitempty"`
	Mentions []string            `json:"mentions,omitempty"`
	T        int64               `json:"t"`
	Reacts   map[string][]string `json:"reacts,omitempty"`
}

type chatPerson struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Host bool      `json:"host,omitempty"`
	Seen time.Time `json:"seen"`
}

type Chat struct {
	mu     sync.Mutex
	Msgs   []*ChatMsg             `json:"msgs"`
	Seq    int64                  `json:"seq"`
	Rev    int64                  `json:"rev"`
	People map[string]*chatPerson `json:"people"`
	path   string                 // where it's saved ("" = memory only)
	last   map[string]time.Time   // anti-flood: last post per person
}

func newChat(path string) *Chat {
	c := &Chat{People: map[string]*chatPerson{}, path: path, last: map[string]time.Time{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			json.Unmarshal(b, c)
			if c.People == nil {
				c.People = map[string]*chatPerson{}
			}
		}
	}
	c.Rev++ // a fresh start always differs from what pollers last saw
	return c
}

func chatPath(netID string) string {
	d := filepath.Join(appDataDir(), "chat")
	os.MkdirAll(d, 0o755)
	return filepath.Join(d, netID+".json")
}

func (c *Chat) saveLocked() {
	if c.path == "" {
		return
	}
	b, _ := json.Marshal(c)
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, c.path)
	}
}

// chatID turns a private key into the public id shown to others.
func chatID(key string) string {
	if len(key) < 16 || len(key) > 128 {
		return ""
	}
	h := sha256.Sum256([]byte("sm-chat:" + key))
	return base64.RawURLEncoding.EncodeToString(h[:9])
}

func cleanChatName(n string) string {
	n = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 32 || r == 0x7f {
			return -1
		}
		return r
	}, n)), " ")
	if utf8.RuneCountInString(n) > 32 {
		n = string([]rune(n)[:32])
	}
	return n
}

// seen records that someone has the chat open.
func (c *Chat) seen(id, name string, host bool) {
	if id == "" {
		return
	}
	name = cleanChatName(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.People[id]
	if p == nil {
		if len(c.People) > 500 {
			return
		}
		p = &chatPerson{ID: id}
		c.People[id] = p
	}
	if (name != "" && p.Name != name) || p.Host != host {
		if name != "" {
			p.Name = name
		}
		p.Host = host
		c.Rev++
	}
	wasOnline := time.Since(p.Seen) < chatOnline
	p.Seen = time.Now()
	if !wasOnline {
		c.Rev++ // someone came online
	}
}

type chatView struct {
	Rev    int64            `json:"rev"`
	Seq    int64            `json:"seq"`
	Me     string           `json:"me"`
	Msgs   []*ChatMsg       `json:"msgs,omitempty"`
	People []map[string]any `json:"people,omitempty"`
	Emojis []string         `json:"emojis,omitempty"`
	Same   bool             `json:"same,omitempty"`
}

// view returns the chat, or only {rev, same:true} when nothing changed since `since`.
func (c *Chat) view(since int64, me string) chatView {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The rev also reflects who is online, so pollers notice people leaving.
	var on uint32 = 2166136261
	for id, p := range c.People {
		if time.Since(p.Seen) < chatOnline {
			for i := 0; i < len(id); i++ {
				on += uint32(id[i]) * 16777619
			}
		}
	}
	rev := c.Rev<<8 | int64(on&0xff)
	v := chatView{Rev: rev, Seq: c.Seq, Me: me}
	if since == rev {
		v.Same = true
		return v
	}
	v.Msgs = c.Msgs
	v.Emojis = chatEmojis
	for _, p := range c.People {
		if p.Name == "" {
			continue
		}
		v.People = append(v.People, map[string]any{"id": p.ID, "name": p.Name, "host": p.Host, "online": time.Since(p.Seen) < chatOnline})
	}
	return v
}

type chatPost struct {
	Op       string   `json:"op"` // send|react
	Name     string   `json:"name"`
	Text     string   `json:"text"`
	Reply    int64    `json:"reply"`
	Mentions []string `json:"mentions"`
	Msg      int64    `json:"msg"`
	Emoji    string   `json:"emoji"`
}

func (c *Chat) post(id string, host bool, p chatPost) error {
	if id == "" {
		return errors.New("chat key missing")
	}
	name := cleanChatName(p.Name)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch p.Op {
	case "send":
		text := strings.TrimSpace(strings.ToValidUTF8(p.Text, ""))
		if text == "" {
			return errors.New("type a message first")
		}
		if name == "" {
			return errors.New("pick a name first")
		}
		if utf8.RuneCountInString(text) > chatMaxText {
			return errors.New("message is too long")
		}
		if t, ok := c.last[id]; ok && time.Since(t) < 400*time.Millisecond {
			return errors.New("slow down a little")
		}
		c.last[id] = time.Now()
		if len(c.last) > 1000 {
			c.last = map[string]time.Time{id: time.Now()}
		}
		m := &ChatMsg{From: name, FromID: id, Host: host, Text: text, T: time.Now().UnixMilli()}
		if p.Reply > 0 && c.find(p.Reply) != nil {
			m.Reply = p.Reply
		}
		for _, mid := range p.Mentions {
			if c.People[mid] != nil && !contains(m.Mentions, mid) && len(m.Mentions) < 20 {
				m.Mentions = append(m.Mentions, mid)
			}
		}
		c.Seq++
		m.ID = c.Seq
		c.Msgs = append(c.Msgs, m)
		if len(c.Msgs) > chatKeep {
			c.Msgs = append([]*ChatMsg(nil), c.Msgs[len(c.Msgs)-chatKeep:]...)
		}
		if pp := c.People[id]; pp == nil {
			c.People[id] = &chatPerson{ID: id, Name: name, Host: host, Seen: time.Now()}
		} else {
			pp.Name, pp.Seen = name, time.Now()
		}
	case "react":
		if !contains(chatEmojis, p.Emoji) {
			return errors.New("unknown reaction")
		}
		m := c.find(p.Msg)
		if m == nil {
			return errors.New("message not found")
		}
		if m.Reacts == nil {
			m.Reacts = map[string][]string{}
		}
		who := m.Reacts[p.Emoji]
		if i := indexOf(who, id); i >= 0 {
			who = append(who[:i], who[i+1:]...)
		} else {
			who = append(who, id)
		}
		if len(who) == 0 {
			delete(m.Reacts, p.Emoji)
		} else {
			m.Reacts[p.Emoji] = who
		}
	default:
		return errors.New("unknown chat action")
	}
	c.Rev++
	c.saveLocked()
	return nil
}

func (c *Chat) find(id int64) *ChatMsg {
	for _, m := range c.Msgs {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// serveChat is the HTTP endpoint other people use: GET polls, POST sends.
// hostID is the owner's own id: nobody else may post with it.
func serveChat(w http.ResponseWriter, r *http.Request, c *Chat, hostID string) {
	id := chatID(r.Header.Get("X-SM-Chat"))
	if id == hostID {
		id = ""
	}
	switch r.Method {
	case http.MethodGet:
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		c.seen(id, r.URL.Query().Get("name"), false)
		writeJSON(w, c.view(since, id))
	case http.MethodPost:
		var p chatPost
		if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&p) != nil {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		if err := c.post(id, false, p); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, c.view(-1, id))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// ---- this PC's own identity -------------------------------------------------

func selfChat() (key, id, name string) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfg.ChatKey == "" {
		cfg.ChatKey = randToken(24)
		saveConfigLocked()
	}
	return cfg.ChatKey, chatID(cfg.ChatKey), cfg.ChatName
}

// ---- the app's own access to every chat it can see -------------------------

var chatHTTP = &http.Client{Timeout: 12 * time.Second, Transport: recvClient.Transport}

// chatCall reads (post == nil) or posts to a chat. kind: share|hosted|joined|recv.
func chatCall(kind, id string, since int64, post *chatPost) (any, error) {
	key, me, name := selfChat()
	if post != nil {
		post.Name = name
	}
	local := func(c *Chat) (any, error) {
		if post != nil {
			if err := c.post(me, true, *post); err != nil {
				return nil, err
			}
			return c.view(-1, me), nil
		}
		c.seen(me, name, true)
		return c.view(since, me), nil
	}
	switch kind {
	case "share":
		sharesMu.RLock()
		var s *Share
		for _, x := range shares {
			if x.ID == id {
				s = x
			}
		}
		sharesMu.RUnlock()
		if s == nil {
			return nil, errors.New("this share has ended")
		}
		return local(s.chat)
	case "hosted":
		swarmMu.RLock()
		h := hosted[id]
		swarmMu.RUnlock()
		if h == nil {
			return nil, errors.New("network not found")
		}
		return local(h.chat)
	case "joined":
		swarmMu.RLock()
		j := joined[id]
		swarmMu.RUnlock()
		if j == nil {
			return nil, errors.New("network not found")
		}
		j.mu.Lock()
		host := j.host
		j.mu.Unlock()
		if host == "" {
			return nil, errors.New("not connected to the host right now")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		path, method, body := "/chat?since="+strconv.FormatInt(since, 10)+"&name="+url.QueryEscape(name), "GET", io.Reader(nil)
		if post != nil {
			b, _ := json.Marshal(post)
			path, method, body = "/chat", "POST", strings.NewReader(string(b))
		}
		resp, err := j.reqH(ctx, method, host, path, body, map[string]string{"X-SM-Chat": key, "Content-Type": "application/json"})
		if err != nil {
			return nil, errors.New("can't reach the host's chat")
		}
		return readChatResp(resp, j)
	case "recv":
		base := ""
		recvMu.Lock()
		if m := recvCache[id]; m != nil {
			base = m.Base
		}
		for _, t := range recvTasks {
			if t.Token == id && base == "" {
				base = t.Base
			}
		}
		recvMu.Unlock()
		if base == "" {
			return nil, errors.New("open the link again first")
		}
		u := base + "/s/" + id + "/chat"
		var req *http.Request
		if post != nil {
			b, _ := json.Marshal(post)
			req, _ = http.NewRequest("POST", u, strings.NewReader(string(b)))
		} else {
			req, _ = http.NewRequest("GET", u+"?since="+strconv.FormatInt(since, 10)+"&name="+url.QueryEscape(name), nil)
		}
		req.Header.Set("X-SM-Chat", key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := chatHTTP.Do(req)
		if err != nil {
			return nil, errors.New("can't reach the sender's chat")
		}
		return readChatResp(resp, nil)
	}
	return nil, errors.New("unknown chat")
}

func readChatResp(resp *http.Response, j *JoinNet) (any, error) {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("chat isn't available (the share ended, or the sender's app is older)")
	}
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, errors.New("chat error: " + resp.Status)
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return nil, errors.New("bad chat reply")
	}
	if j != nil {
		if s, ok := v["seq"].(float64); ok {
			j.mu.Lock()
			j.chatSeq = int64(s)
			j.mu.Unlock()
		}
	}
	return v, nil
}
