package main

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// "Pause sharing": every link and network goes offline and running downloads
// pause. Resume brings back exactly the downloads that were paused this way.

var (
	pausedAll   atomic.Bool
	pauseMu     sync.Mutex
	pausedRecv  []string            // receive task ids
	pausedSwarm map[string][]string // network id -> file ids
)

func isPausedAll() bool { return pausedAll.Load() }

func setPausedAll(on bool) {
	pauseMu.Lock()
	defer pauseMu.Unlock()
	if pausedAll.Load() == on {
		return
	}
	pausedAll.Store(on)
	if on {
		pausedRecv, pausedSwarm = nil, map[string][]string{}
		recvMu.Lock()
		ts := append([]*RecvTask(nil), recvTasks...)
		recvMu.Unlock()
		for _, t := range ts {
			t.mu.Lock()
			running := t.State == "downloading"
			t.mu.Unlock()
			if running {
				t.pause()
				pausedRecv = append(pausedRecv, t.ID)
			}
		}
		swarmMu.RLock()
		js := make([]*JoinNet, 0, len(joined))
		for _, j := range joined {
			js = append(js, j)
		}
		swarmMu.RUnlock()
		for _, j := range js {
			for _, tv := range j.taskList() {
				if tv.State == "queued" || tv.State == "preparing" || tv.State == "downloading" {
					j.pauseTask(tv.File.ID)
					pausedSwarm[j.id] = append(pausedSwarm[j.id], tv.File.ID)
				}
			}
		}
	} else {
		for _, id := range pausedRecv {
			if t := findRecv(id); t != nil {
				t.mu.Lock()
				still := t.State == "paused"
				t.mu.Unlock()
				if still {
					t.start()
				}
			}
		}
		for nid, ids := range pausedSwarm {
			swarmMu.RLock()
			j := joined[nid]
			swarmMu.RUnlock()
			if j != nil {
				j.startDownloads(ids)
			}
		}
		pausedRecv, pausedSwarm = nil, nil
	}
	if guiActive() {
		go syncTray()
	}
}

// pausedGate answers share and network requests with "paused" while sharing is paused.
func pausedGate(w http.ResponseWriter, r *http.Request) bool {
	if !pausedAll.Load() {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/n/") || strings.HasPrefix(r.URL.Path, "/p/") {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Sharing is paused by the sender. Try again later.", http.StatusServiceUnavailable)
		return true
	}
	return false
}
