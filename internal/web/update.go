package web

import (
	"log"
	"net/http"
	"sync"
	"time"

	"nascloud/internal/updater"
	"nascloud/internal/version"
)

// updCache хранит последнюю проверку обновлений: GitHub опрашивается не чаще раза в сутки.
type updCache struct {
	mu  sync.Mutex
	at  time.Time
	rel *updater.Release
}

// update отдаёт новую версию, если проверка включена в настройках и подпись релиза верна.
func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	if !s.Store.Get().Settings.UpdateCheck {
		writeJSON(w, 200, map[string]any{"available": false})
		return
	}
	c := &s.upd
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) > 24*time.Hour {
		rel, ok, err := updater.Check(r.Context(), &http.Client{Timeout: 15 * time.Second}, "https://api.github.com", version.Repo, version.PubKey, version.Pkg, version.Version)
		if err != nil {
			log.Printf("проверка обновлений: %v", err)
		}
		c.at, c.rel = time.Now(), nil
		if ok {
			c.rel = &rel
		}
	}
	if c.rel == nil {
		writeJSON(w, 200, map[string]any{"available": false})
		return
	}
	writeJSON(w, 200, map[string]any{"available": true, "version": c.rel.Version, "url": c.rel.URL})
}
