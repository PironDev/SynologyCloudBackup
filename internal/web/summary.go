package web

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"nascloud/internal/engine"
	"nascloud/internal/provider"
	"nascloud/internal/provider/local"
)

type sumEntry struct {
	at   time.Time
	info provider.AccountInfo
	err  string
}

type quotaCache struct {
	mu      sync.Mutex
	m       map[string]sumEntry
	running map[string]bool
}

// kick запускает фоновую проверку аккаунта (не более одной одновременно).
func (s *Server) kick(id string) {
	s.qc.mu.Lock()
	if s.qc.running[id] {
		s.qc.mu.Unlock()
		return
	}
	s.qc.running[id] = true
	s.qc.mu.Unlock()
	go func() {
		e := sumEntry{at: time.Now()}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		p, err := s.provFor(id)
		if err == nil {
			e.info, err = p.Check(ctx)
		}
		if err != nil {
			e.err = err.Error()
		}
		s.qc.mu.Lock()
		s.qc.m[id] = e
		delete(s.qc.running, id)
		s.qc.mu.Unlock()
	}()
}

type accSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Title string `json:"title,omitempty"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
	Free  int64  `json:"free"`
	At    int64  `json:"at"`
	// Pending — данные ещё запрашиваются (облако отвечает медленно); интерфейс повторит запрос.
	Pending bool `json:"pending,omitempty"`
}

// summary: объём по аккаунтам (кэш 60 с), тома NAS и статистика выгрузок за 14 дней.
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Get()
	refresh := r.URL.Query().Get("refresh") == "1"
	accs := make([]accSummary, len(cfg.Accounts))
	for i, a := range cfg.Accounts {
		o := &accs[i]
		*o = accSummary{ID: a.ID, Name: a.Name, Slug: a.Slug}
		s.qc.mu.Lock()
		e, have := s.qc.m[a.ID]
		busy := s.qc.running[a.ID]
		s.qc.mu.Unlock()
		if !have || refresh || time.Since(e.at) > 60*time.Second {
			s.kick(a.ID)
			busy = true
		}
		if !have {
			o.Pending, o.OK = true, true
			continue
		}
		o.Pending = busy && refresh
		o.OK, o.Error, o.Title, o.At = e.err == "", e.err, e.info.Title, e.at.Unix()
		o.Total, o.Used = e.info.Total, e.info.Used
		if o.Total > 0 {
			o.Free = max(o.Total-o.Used, 0)
		}
	}

	var total, used int64
	known := 0
	for _, a := range accs {
		if a.OK && !a.Pending && a.Total > 0 {
			total += a.Total
			used += a.Used
			known++
		}
	}

	type vol struct {
		Path  string `json:"path"`
		Total int64  `json:"total"`
		Free  int64  `json:"free"`
	}
	vols := []vol{}
	if roots, _ := s.localRoots(); len(roots) > 0 {
		for _, v := range local.New(roots).Volumes() {
			vols = append(vols, vol{v.Path, v.Total, v.Free})
		}
	}

	type day struct {
		Date  string `json:"date"`
		Bytes int64  `json:"bytes"`
		Files int    `json:"files"`
		OK    int    `json:"ok"`
		Fail  int    `json:"fail"`
	}
	const n = 14
	days := make([]day, n)
	idx := map[string]int{}
	now := time.Now()
	for i := 0; i < n; i++ {
		d := now.AddDate(0, 0, -(n - 1 - i)).Format("2006-01-02")
		days[i].Date = d
		idx[d] = i
	}
	var runsOK, runsFail int
	var bytesAll int64
	runs := s.Mgr.History("", 200)
	for _, rr := range runs {
		i, ok := idx[rr.Started.Format("2006-01-02")]
		if !ok {
			continue
		}
		days[i].Bytes += rr.Stats.Bytes
		days[i].Files += rr.Stats.Uploaded
		bytesAll += rr.Stats.Bytes
		switch rr.Status {
		case engine.StatusSuccess:
			days[i].OK++
			runsOK++
		case engine.StatusFailed, engine.StatusPartial:
			days[i].Fail++
			runsFail++
		}
	}
	enabled := 0
	for _, j := range cfg.Jobs {
		if j.Enabled {
			enabled++
		}
	}
	sort.Slice(accs, func(a, b int) bool { return accs[a].Name < accs[b].Name })
	writeJSON(w, 200, map[string]any{
		"accounts": accs, "volumes": vols, "days": days,
		"totals": map[string]any{"total": total, "used": used, "free": max(total-used, 0), "known": known, "accounts": len(accs)},
		"jobs":   map[string]any{"count": len(cfg.Jobs), "enabled": enabled, "runsOk": runsOK, "runsFail": runsFail, "bytes": bytesAll},
	})
}
