// Package web — HTTP API и встроенный веб-интерфейс.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"nascloud/internal/telemetry"
	"net"
	"net/http"
	"net/url"
	"os"
	osuser "os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nascloud/internal/auth"
	"nascloud/internal/cron"
	"nascloud/internal/engine"
	"nascloud/internal/explorer"
	"nascloud/internal/provider"
	"nascloud/internal/store"
)

//go:embed static
var staticFS embed.FS

const cookieName = "cb_sid"

// Deps — зависимости сервера.
type Deps struct {
	Title       string
	Desc        provider.Descriptor
	Store       *store.Store
	Mgr         *engine.Manager
	Pool        *engine.Pool
	GatewayAddr string
	Version     string
}

// Server — веб-сервер.
type Server struct {
	Deps
	sessions *auth.Sessions
	limiter  *auth.Limiter
	mux      *http.ServeMux
	X        *explorer.Manager
	qc       *quotaCache
	caches   sync.Map // provider.Provider → *cache.FS (кэш списков для проводника)
	upd      updCache
}

// New создаёт сервер и регистрирует маршруты.
func New(d Deps) *Server {
	s := &Server{Deps: d, sessions: auth.NewSessions(12 * time.Hour), limiter: auth.NewLimiter(), mux: http.NewServeMux(), qc: &quotaCache{m: map[string]sumEntry{}, running: map[string]bool{}}}
	s.X = explorer.New(s.provFor)
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))
	index, _ := fs.ReadFile(sub, "index.html")
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			s.serveIndex(w, r, string(index))
			return
		}
		files.ServeHTTP(w, r)
	})

	s.mux.HandleFunc("GET /api/state", s.state)
	s.mux.HandleFunc("POST /api/setup", s.csrf(s.setup))
	s.mux.HandleFunc("POST /api/login", s.csrf(s.login))
	s.mux.HandleFunc("POST /api/logout", s.csrf(s.logout))

	s.mux.HandleFunc("GET /api/accounts", s.authed(s.listAccounts))
	s.mux.HandleFunc("POST /api/accounts", s.authed(s.saveAccount))
	s.mux.HandleFunc("PUT /api/accounts/{id}", s.authed(s.saveAccount))
	s.mux.HandleFunc("DELETE /api/accounts/{id}", s.authed(s.deleteAccount))
	s.mux.HandleFunc("POST /api/accounts/test", s.authed(s.testAccount))

	s.mux.HandleFunc("GET /api/jobs", s.authed(s.listJobs))
	s.mux.HandleFunc("POST /api/jobs", s.authed(s.saveJob))
	s.mux.HandleFunc("PUT /api/jobs/{id}", s.authed(s.saveJob))
	s.mux.HandleFunc("DELETE /api/jobs/{id}", s.authed(s.deleteJob))
	s.mux.HandleFunc("POST /api/jobs/{id}/run", s.authed(s.runJob))
	s.mux.HandleFunc("POST /api/jobs/{id}/cancel", s.authed(s.cancelJob))

	s.mux.HandleFunc("GET /api/runs", s.authed(s.listRuns))
	s.mux.HandleFunc("GET /api/runs/{id}/log", s.authed(s.runLog))

	s.mux.HandleFunc("GET /api/fs", s.authed(s.browseFS))
	s.mux.HandleFunc("GET /api/summary", s.authed(s.summary))
	s.mux.HandleFunc("POST /api/telemetry/client", s.csrf(s.authed(s.clientError)))
	s.mux.HandleFunc("GET /api/x/sources", s.authed(s.xSources))
	s.mux.HandleFunc("GET /api/x/list", s.authed(s.xList))
	s.mux.HandleFunc("POST /api/x/mkdir", s.authed(s.xMkdir))
	s.mux.HandleFunc("POST /api/x/rename", s.authed(s.xRename))
	s.mux.HandleFunc("POST /api/x/delete", s.authed(s.xDelete))
	s.mux.HandleFunc("POST /api/x/trash/empty", s.csrf(s.authed(s.xEmptyTrash)))
	s.mux.HandleFunc("POST /api/x/transfer", s.authed(s.xTransfer))
	s.mux.HandleFunc("GET /api/x/tasks", s.authed(s.xTasks))
	s.mux.HandleFunc("POST /api/x/tasks/clear", s.authed(s.xClear))
	s.mux.HandleFunc("POST /api/x/tasks/{id}/cancel", s.authed(s.xCancel))
	s.mux.HandleFunc("GET /api/x/info", s.authed(s.xInfo))
	s.mux.HandleFunc("POST /api/x/publish", s.csrf(s.authed(s.xPublish)))
	s.mux.HandleFunc("GET /api/x/file", s.authed(s.xDownload))
	s.mux.HandleFunc("HEAD /api/x/file", s.authed(s.xDownload))
	s.mux.HandleFunc("PUT /api/x/file", s.authed(s.xUpload))
	s.mux.HandleFunc("GET /api/settings", s.authed(s.getSettings))
	s.mux.HandleFunc("GET /api/update", s.authed(s.update))
	s.mux.HandleFunc("PUT /api/settings", s.authed(s.putSettings))
	s.mux.HandleFunc("POST /api/password", s.authed(s.changePassword))
	s.mux.HandleFunc("GET /api/gateway", s.authed(s.gatewayInfo))
	s.mux.HandleFunc("POST /api/gateway/regenerate", s.authed(s.gatewayRegenerate))
	return s
}

// Handler возвращает корневой обработчик с заголовками безопасности.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				telemetry.Capture("http", fmt.Sprintf("panic в обработчике %s %s: %v | %s", r.Method, r.URL.Path, p, telemetry.Stack()), nil)
				log.Printf("panic: %s %s: %v", r.Method, r.URL.Path, p)
				jerr(w, 500, "внутренняя ошибка")
			}
		}()
		s.mux.ServeHTTP(w, r)
	})
}

// ---------- помощники ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jerr(w http.ResponseWriter, code int, format string, a ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, a...)})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("неверный запрос: %w", err)
	}
	return nil
}

func (s *Server) loggedIn(r *http.Request) bool {
	if p := proxyOf(r); p != nil {
		return p.user != "" // за CGI-мостом доверяем только вошедшему в DSM пользователю
	}
	c, err := r.Cookie(cookieName)
	return err == nil && s.sessions.Valid(c.Value)
}

// csrf требует заголовок, который нельзя отправить с чужого сайта без CORS.
func (s *Server) csrf(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "cloudbackup" {
			jerr(w, http.StatusForbidden, "запрос отклонён")
			return
		}
		h(w, r)
	}
}

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return s.csrf(func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			jerr(w, http.StatusUnauthorized, "требуется вход")
			return
		}
		h(w, r)
	})
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.sessions.New(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", MaxAge: 12 * 3600,
	})
}

// ---------- состояние и вход ----------

func gatewayPort(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return ""
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"title": s.Title, "kind": s.Desc.Kind, "provider": s.Desc.Title, "version": s.Version,
		"fields": s.Desc.Fields, "help": s.Desc.Help, "steps": s.Desc.Steps, "authUrl": s.Desc.AuthURL, "globals": s.Store.Get().Settings.Globals, "runUser": runUser(), "telemetry": s.Store.Get().Settings.Telemetry,
		"needSetup":       proxyOf(r) == nil && s.Store.Get().PasswordHash == "",
		"loggedIn":        s.loggedIn(r),
		"auth":            authMode(r),
		"user":            userOf(r),
		"reason":          reasonOf(r),
		"defaultExcludes": engine.DefaultExcludes,
		"gatewayPort":     gatewayPort(s.GatewayAddr),
	})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if proxyOf(r) != nil {
		jerr(w, http.StatusBadRequest, "вход выполняется через учётную запись DSM")
		return
	}
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	if len([]rune(in.Password)) < 8 {
		jerr(w, 400, "пароль должен быть не короче 8 символов")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		jerr(w, 500, "%v", err)
		return
	}
	err = s.Store.Update(func(c *store.Config) error {
		if c.PasswordHash != "" {
			return errors.New("пароль уже задан")
		}
		c.PasswordHash = hash
		return nil
	})
	if err != nil {
		jerr(w, 409, "%v", err)
		return
	}
	s.startSession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if proxyOf(r) != nil {
		jerr(w, http.StatusBadRequest, "вход выполняется через учётную запись DSM")
		return
	}
	ip := clientIP(r)
	if ok, wait := s.limiter.Allowed(ip); !ok {
		jerr(w, 429, "слишком много попыток, подождите %d мин", int(wait.Minutes())+1)
		return
	}
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	hash := s.Store.Get().PasswordHash
	if hash == "" || !auth.CheckPassword(in.Password, hash) {
		s.limiter.Fail(ip)
		time.Sleep(500 * time.Millisecond)
		jerr(w, 401, "неверный пароль")
		return
	}
	s.limiter.Success(ip)
	s.startSession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if proxyOf(r) != nil {
		jerr(w, http.StatusBadRequest, "вход выполняется через учётную запись DSM")
		return
	}
	var in struct{ Old, New string }
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	if !auth.CheckPassword(in.Old, s.Store.Get().PasswordHash) {
		jerr(w, 403, "текущий пароль неверный")
		return
	}
	if len([]rune(in.New)) < 8 {
		jerr(w, 400, "новый пароль должен быть не короче 8 символов")
		return
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		jerr(w, 500, "%v", err)
		return
	}
	if err := s.Store.Update(func(c *store.Config) error { c.PasswordHash = hash; return nil }); err != nil {
		jerr(w, 500, "%v", err)
		return
	}
	s.sessions.Clear()
	s.startSession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- аккаунты ----------

type accountOut struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Slug   string            `json:"slug"`
	Fields map[string]string `json:"fields"`
	Set    map[string]bool   `json:"set"` // какие секретные поля заполнены
}

func (s *Server) accountDTO(a store.Account) accountOut {
	out := accountOut{ID: a.ID, Name: a.Name, Slug: a.Slug, Fields: map[string]string{}, Set: map[string]bool{}}
	for _, f := range s.Desc.Fields {
		if f.Secret {
			out.Set[f.Key] = a.Fields[f.Key] != ""
		} else {
			out.Fields[f.Key] = a.Fields[f.Key]
		}
	}
	return out
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	out := []accountOut{}
	for _, a := range s.Store.Get().Accounts {
		out = append(out, s.accountDTO(a))
	}
	writeJSON(w, 200, out)
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func asciiSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	if out == "" {
		out = "account"
	}
	return out
}

func uniqueSlug(c store.Config, want, selfID string) string {
	slug := want
	for i := 2; ; i++ {
		clash := false
		for _, a := range c.Accounts {
			if a.Slug == slug && a.ID != selfID {
				clash = true
			}
		}
		if !clash {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", want, i)
	}
}

type accountIn struct {
	Name   string            `json:"name"`
	Slug   string            `json:"slug"`
	Fields map[string]string `json:"fields"`
}

// mergeFields сохраняет прежние значения секретных полей, если пришли пустыми.
func (s *Server) mergeFields(old map[string]string, in map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range s.Desc.Fields {
		if f.Global {
			continue // общие поля хранятся в настройках, не в аккаунте
		}
		v, given := in[f.Key]
		v = strings.TrimSpace(v)
		// Не пришло вовсе (проверка сохранённого аккаунта) — берём прежнее;
		// секрет, оставленный пустым в форме, тоже не затираем.
		if !given || (f.Secret && v == "") {
			v = old[f.Key]
		}
		out[f.Key] = v
	}
	return out
}

func (s *Server) saveAccount(w http.ResponseWriter, r *http.Request) {
	var in accountIn
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	id := r.PathValue("id")
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		jerr(w, 400, "укажите название аккаунта")
		return
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if in.Slug != "" && !slugRe.MatchString(in.Slug) {
		jerr(w, 400, "адрес для шлюза: латинские буквы, цифры и дефис, до 31 символа")
		return
	}
	var saved store.Account
	err := s.Store.Update(func(c *store.Config) error {
		idx := -1
		for i, a := range c.Accounts {
			if a.ID == id {
				idx = i
			}
		}
		if id != "" && idx < 0 {
			return errors.New("аккаунт не найден")
		}
		var old store.Account
		if idx >= 0 {
			old = c.Accounts[idx]
		}
		fields := s.mergeFields(old.Fields, in.Fields)
		if _, err := s.Pool.New(fields); err != nil {
			return err
		}
		slug := in.Slug
		if slug == "" {
			slug = old.Slug
		}
		if slug == "" {
			slug = asciiSlug(in.Name)
		}
		for _, a := range c.Accounts {
			if a.Slug == slug && a.ID != old.ID && in.Slug != "" {
				return fmt.Errorf("адрес %q уже занят аккаунтом «%s»", slug, a.Name)
			}
		}
		slug = uniqueSlug(*c, slug, old.ID)
		saved = store.Account{ID: old.ID, Name: in.Name, Slug: slug, Fields: fields}
		if idx >= 0 {
			c.Accounts[idx] = saved
		} else {
			saved.ID = store.NewID()
			c.Accounts = append(c.Accounts, saved)
		}
		return nil
	})
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, s.accountDTO(saved))
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Store.Update(func(c *store.Config) error {
		for _, j := range c.Jobs {
			if j.AccountID == id {
				return fmt.Errorf("аккаунт используется заданием «%s»: сначала удалите или измените задание", j.Name)
			}
		}
		for i, a := range c.Accounts {
			if a.ID == id {
				c.Accounts = append(c.Accounts[:i], c.Accounts[i+1:]...)
				return nil
			}
		}
		return errors.New("аккаунт не найден")
	})
	if err != nil {
		jerr(w, 409, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) testAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID     string            `json:"id"`
		Fields map[string]string `json:"fields"`
	}
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	var old store.Account
	if in.ID != "" {
		old, _ = s.Store.Get().Account(in.ID)
	}
	prov, err := s.Pool.New(s.mergeFields(old.Fields, in.Fields))
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	info, err := prov.Check(ctx)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "info": info})
}

// ---------- задания ----------

type jobOut struct {
	store.Job
	AccountName string            `json:"accountName"`
	Next        *time.Time        `json:"next,omitempty"`
	Active      *engine.RunRecord `json:"active,omitempty"`
	Last        *engine.RunRecord `json:"last,omitempty"`
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Get()
	active := s.Mgr.Active()
	last := map[string]engine.RunRecord{}
	for _, rec := range s.Mgr.History("", 0) { // от новых к старым
		if _, ok := last[rec.JobID]; !ok && rec.Status != engine.StatusRunning {
			last[rec.JobID] = rec
		}
	}
	out := []jobOut{}
	for _, j := range cfg.Jobs {
		o := jobOut{Job: j}
		if a, ok := cfg.Account(j.AccountID); ok {
			o.AccountName = a.Name
		}
		if t, ok := s.Mgr.NextRun(j); ok {
			o.Next = &t
		}
		if a, ok := active[j.ID]; ok {
			a := a
			o.Active = &a
		}
		if l, ok := last[j.ID]; ok {
			l := l
			o.Last = &l
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, k int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[k].Name) })
	writeJSON(w, 200, out)
}

func normalizeJob(c store.Config, j *store.Job) error {
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" {
		return errors.New("укажите название задания")
	}
	if _, ok := c.Account(j.AccountID); !ok {
		return errors.New("выберите аккаунт")
	}
	switch j.Mode {
	case "mirror", "archive":
	case "":
		j.Mode = "mirror"
	default:
		return errors.New("неизвестный режим задания")
	}
	var srcs []string
	for _, p := range j.Sources {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) {
			return fmt.Errorf("путь источника должен быть абсолютным: %s", p)
		}
		srcs = append(srcs, p)
	}
	if len(srcs) == 0 {
		return errors.New("добавьте хотя бы одну папку или файл-источник")
	}
	j.Sources = srcs
	var ex []string
	for _, e := range j.Excludes {
		if e = strings.TrimSpace(e); e != "" {
			ex = append(ex, e)
		}
	}
	j.Excludes = ex
	j.RemoteDir = provider.Clean(strings.TrimSpace(j.RemoteDir))
	j.Schedule = strings.TrimSpace(j.Schedule)
	if j.Schedule != "" {
		if _, ok, err := cron.OnceAt(j.Schedule); ok {
			if err != nil {
				return fmt.Errorf("расписание: однократный запуск пишется как «@at ГГГГ-ММ-ДД ЧЧ:ММ»")
			}
		} else if _, err := cron.Parse(j.Schedule); err != nil {
			return fmt.Errorf("расписание: %w", err)
		}
	}
	j.Verify = provider.NormVerify(j.Verify)
	if j.Concurrency < 1 {
		j.Concurrency = 4
	}
	if j.Concurrency > 16 {
		j.Concurrency = 16
	}
	if j.Archive.PartSizeMB < 0 || (j.Archive.PartSizeMB > 0 && j.Archive.PartSizeMB < 16) {
		return errors.New("размер части архива: 0 (не разбивать) или не меньше 16 МБ")
	}
	if j.Archive.Keep < 0 {
		return errors.New("«хранить последние» не может быть отрицательным")
	}
	return nil
}

func (s *Server) saveJob(w http.ResponseWriter, r *http.Request) {
	var in store.Job
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	id := r.PathValue("id")
	var saved store.Job
	err := s.Store.Update(func(c *store.Config) error {
		if err := normalizeJob(*c, &in); err != nil {
			return err
		}
		in.ID = id
		idx := -1
		for i, j := range c.Jobs {
			if j.ID == id {
				idx = i
			}
		}
		if id != "" && idx < 0 {
			return errors.New("задание не найдено")
		}
		if idx >= 0 {
			c.Jobs[idx] = in
		} else {
			in.ID = store.NewID()
			c.Jobs = append(c.Jobs, in)
		}
		saved = in
		return nil
	})
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, saved)
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, busy := s.Mgr.Active()[id]; busy {
		jerr(w, 409, "задание выполняется: сначала остановите его")
		return
	}
	err := s.Store.Update(func(c *store.Config) error {
		for i, j := range c.Jobs {
			if j.ID == id {
				c.Jobs = append(c.Jobs[:i], c.Jobs[i+1:]...)
				return nil
			}
		}
		return errors.New("задание не найдено")
	})
	if err != nil {
		jerr(w, 404, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) runJob(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Mgr.Start(r.PathValue("id"), "manual")
	switch {
	case errors.Is(err, engine.ErrRunning):
		jerr(w, 409, "%v", err)
	case err != nil:
		jerr(w, 400, "%v", err)
	default:
		writeJSON(w, 202, rec)
	}
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"ok": s.Mgr.Cancel(r.PathValue("id"))})
}

// ---------- журнал ----------

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	runs := s.Mgr.History(r.URL.Query().Get("job"), limit)
	if runs == nil {
		runs = []engine.RunRecord{}
	}
	writeJSON(w, 200, runs)
}

func (s *Server) runLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	text, next, err := s.Mgr.ReadLog(id, off)
	if err != nil {
		jerr(w, 404, "журнал не найден")
		return
	}
	running := false
	for _, a := range s.Mgr.Active() {
		if a.ID == id {
			running = true
		}
	}
	writeJSON(w, 200, map[string]any{"text": text, "next": next, "running": running})
}

// ---------- файловая система NAS ----------

func (s *Server) browseFS(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
		if st, err := os.Stat("/volume1"); err == nil && st.IsDir() {
			p = "/volume1"
		}
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		jerr(w, 400, "нужен абсолютный путь")
		return
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		jerr(w, 400, "нет доступа к %s: %v (выдайте пользователю пакета право чтения на общую папку)", p, err)
		return
	}
	type dir struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := []dir{}
	files := 0
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, dir{e.Name(), filepath.Join(p, e.Name())})
		} else {
			files++
		}
	}
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	writeJSON(w, 200, map[string]any{"path": p, "parent": parent, "dirs": dirs, "files": files})
}

// ---------- настройки и шлюз ----------

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Store.Get().Settings)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TmpDir      *string           `json:"tmpDir"`
		Globals     map[string]string `json:"globals"`
		LocalRoots  *[]string         `json:"localRoots"`
		Telemetry   *bool             `json:"telemetry"`
		UpdateCheck *bool             `json:"updateCheck"`
	}
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	cur := s.Store.Get().Settings
	if in.TmpDir != nil {
		t := strings.TrimSpace(*in.TmpDir)
		if t != "" {
			t = filepath.Clean(t)
			if !filepath.IsAbs(t) {
				jerr(w, 400, "временная папка должна быть абсолютным путём")
				return
			}
			if err := os.MkdirAll(t, 0o700); err != nil {
				jerr(w, 400, "не удалось создать %s: %v", t, err)
				return
			}
		}
		cur.TmpDir = t
	}
	if in.Globals != nil {
		allowed := map[string]bool{}
		for _, f := range s.Desc.Fields {
			if f.Global {
				allowed[f.Key] = true
			}
		}
		g := map[string]string{}
		for k, v := range cur.Globals {
			g[k] = v
		}
		for k, v := range in.Globals {
			if !allowed[k] {
				continue
			}
			if v = strings.TrimSpace(v); v == "" {
				delete(g, k)
			} else {
				g[k] = v
			}
		}
		cur.Globals = g
	}
	if in.LocalRoots != nil {
		var roots []string
		for _, p := range *in.LocalRoots {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			p = filepath.Clean(p)
			if !filepath.IsAbs(p) || p == "/" {
				jerr(w, 400, "папка NAS должна быть абсолютным путём и не быть корнем системы: %s", p)
				return
			}
			roots = append(roots, p)
		}
		cur.LocalRoots = roots
	}
	if in.Telemetry != nil {
		cur.Telemetry = in.Telemetry
	}
	if in.UpdateCheck != nil {
		cur.UpdateCheck = *in.UpdateCheck
		s.upd.mu.Lock()
		s.upd.at = time.Time{} // новая настройка — сразу новая проверка
		s.upd.mu.Unlock()
	}
	if err := s.Store.Update(func(c *store.Config) error { c.Settings = cur; return nil }); err != nil {
		jerr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, cur)
}

func (s *Server) gatewayInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Get()
	port := gatewayPort(s.GatewayAddr)
	type acc struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
		URL  string `json:"url"`
	}
	accs := []acc{}
	for _, a := range cfg.Accounts {
		accs = append(accs, acc{a.Name, a.Slug, fmt.Sprintf("http://127.0.0.1:%s/%s/", port, a.Slug)})
	}
	writeJSON(w, 200, map[string]any{"port": port, "listen": s.GatewayAddr, "password": cfg.GatewayPassword, "accounts": accs})
}

func (s *Server) gatewayRegenerate(w http.ResponseWriter, r *http.Request) {
	pw := auth.RandomToken(12)
	if err := s.Store.Update(func(c *store.Config) error { c.GatewayPassword = pw; return nil }); err != nil {
		jerr(w, 500, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]string{"password": pw})
}

// ---------- вход через DSM (за CGI-мостом) ----------

type ctxKey int

const ctxProxy ctxKey = 1

type proxyInfo struct{ user, base, reason string }

func proxyOf(r *http.Request) *proxyInfo {
	p, _ := r.Context().Value(ctxProxy).(*proxyInfo)
	return p
}

func authMode(r *http.Request) string {
	if proxyOf(r) != nil {
		return "dsm"
	}
	return "password"
}

func userOf(r *http.Request) string {
	if p := proxyOf(r); p != nil {
		return p.user
	}
	return ""
}

func reasonOf(r *http.Request) string {
	if p := proxyOf(r); p != nil {
		v, err := url.PathUnescape(p.reason)
		if err != nil {
			return ""
		}
		return v
	}
	return ""
}

// ProxyHandler — обработчик внутреннего интерфейса для CGI-моста DSM. Принимает только
// запросы с верным секретом; имя пользователя берёт из X-DSM-User (его выставляет мост
// после проверки сессии DSM). Cookie-сессии и пароль в этом режиме не используются.
func (s *Server) ProxyHandler(secret string) http.Handler {
	inner := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Proxy-Secret")), []byte(secret)) != 1 {
			jerr(w, http.StatusForbidden, "доступ только через DSM")
			return
		}
		p := &proxyInfo{user: r.Header.Get("X-DSM-User"), base: r.Header.Get("X-Api-Base"), reason: r.Header.Get("X-DSM-Reason")}
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxProxy, p)))
	})
}

var baseRe = regexp.MustCompile(`^[A-Za-z0-9_./?=%-]{0,128}$`)

// serveIndex подставляет в страницу адреса ресурсов: напрямую — относительные, за мостом —
// через index.cgi?r=…, чтобы всё шло по одному адресу DSM.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, tpl string) {
	base := ""
	if p := proxyOf(r); p != nil && baseRe.MatchString(p.base) {
		base = p.base
	}
	asset := func(name string) string {
		if base == "" {
			return name
		}
		return base + url.QueryEscape("/"+name)
	}
	out := strings.NewReplacer(
		"{{BASE}}", html.EscapeString(base),
		"{{ASSET:style.css}}", html.EscapeString(asset("style.css")),
		"{{ASSET:app.js}}", html.EscapeString(asset("app.js")),
		"{{ASSET:explorer.js}}", html.EscapeString(asset("explorer.js")),
	).Replace(tpl)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, out)
}

// runUser — имя пользователя ОС, от которого работает служба (в DSM это sc-<пакет>);
// ему нужно выдавать права на общие папки.
func runUser() string {
	if u, err := osuser.Current(); err == nil {
		return u.Username
	}
	return ""
}

// clientError принимает ошибку из браузера и (если есть согласие) пересылает её в Sentry.
func (s *Server) clientError(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Msg  string `json:"msg"`
		View string `json:"view"`
	}
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	if len(in.Msg) > 2000 {
		in.Msg = in.Msg[:2000]
	}
	if len(in.View) > 30 {
		in.View = in.View[:30]
	}
	telemetry.Capture("ui", in.Msg, map[string]string{"view": in.View})
	w.WriteHeader(204)
}
