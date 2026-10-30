package web_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nascloud/internal/engine"
	"nascloud/internal/secret"
	"nascloud/internal/store"
	"nascloud/internal/testutil"
	"nascloud/internal/web"
)

type env struct {
	srv *httptest.Server
	c   *http.Client
	mem *testutil.Mem
	st  *store.Store
	src string
}

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	mem := testutil.NewMem()
	desc := testutil.Descriptor(mem)
	st, err := store.Open(dir, box, desc.SecretKeys())
	if err != nil {
		t.Fatal(err)
	}
	pool := engine.NewPool(desc)
	mgr, err := engine.NewManager(st, pool, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Shutdown(5 * time.Second) })
	s := web.New(web.Deps{Title: "Тест", Desc: desc, Store: st, Mgr: mgr, Pool: pool, GatewayAddr: "127.0.0.1:8766", Version: "test"})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	src := t.TempDir()
	for name, data := range map[string]string{"a.txt": "aaa", "sub/b.txt": "bbb"} {
		p := filepath.Join(src, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(data), 0o644)
	}
	return &env{srv: srv, c: &http.Client{Jar: jar}, mem: mem, st: st, src: src}
}

func (e *env) call(t *testing.T, method, path string, body any, csrf bool) (int, map[string]any, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, r)
	if csrf {
		req.Header.Set("X-Requested-With", "cloudbackup")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

func (e *env) ok(t *testing.T, method, path string, body any) (map[string]any, string) {
	t.Helper()
	code, m, raw := e.call(t, method, path, body, true)
	if code < 200 || code > 299 {
		t.Fatalf("%s %s → %d: %s", method, path, code, raw)
	}
	return m, raw
}

func TestAuthFlow(t *testing.T) {
	e := setup(t)
	if _, m, _ := e.call(t, "GET", "/api/state", nil, false); m["needSetup"] != true || m["loggedIn"] != false {
		t.Fatalf("начальное состояние: %v", m)
	}
	if code, _, _ := e.call(t, "POST", "/api/setup", map[string]string{"password": "longenough1"}, false); code != 403 {
		t.Fatalf("без X-Requested-With ожидался 403, получен %d", code)
	}
	if code, _, _ := e.call(t, "GET", "/api/accounts", nil, true); code != 401 {
		t.Fatalf("без входа ожидался 401, получен %d", code)
	}
	if code, _, _ := e.call(t, "POST", "/api/setup", map[string]string{"password": "short"}, true); code != 400 {
		t.Fatalf("короткий пароль: %d", code)
	}
	e.ok(t, "POST", "/api/setup", map[string]string{"password": "longenough1"})
	if code, _, _ := e.call(t, "POST", "/api/setup", map[string]string{"password": "another-pass"}, true); code != 409 {
		t.Fatalf("повторная установка пароля: %d", code)
	}
	if _, m, _ := e.call(t, "GET", "/api/state", nil, false); m["loggedIn"] != true || m["needSetup"] != false {
		t.Fatalf("после установки пароля: %v", m)
	}
	e.ok(t, "POST", "/api/logout", map[string]string{})
	if code, _, _ := e.call(t, "GET", "/api/jobs", nil, true); code != 401 {
		t.Fatalf("после выхода ожидался 401, получен %d", code)
	}
	if code, _, _ := e.call(t, "POST", "/api/login", map[string]string{"password": "wrong-password"}, true); code != 401 {
		t.Fatalf("неверный пароль: %d", code)
	}
	e.ok(t, "POST", "/api/login", map[string]string{"password": "longenough1"})
	if code, _, _ := e.call(t, "GET", "/api/jobs", nil, true); code != 200 {
		t.Fatalf("после входа: %d", code)
	}
	// смена пароля
	if code, _, _ := e.call(t, "POST", "/api/password", map[string]string{"old": "bad", "new": "newpassword1"}, true); code != 403 {
		t.Fatalf("смена с неверным текущим паролем: %d", code)
	}
	e.ok(t, "POST", "/api/password", map[string]string{"old": "longenough1", "new": "newpassword1"})
	e.ok(t, "POST", "/api/logout", map[string]string{})
	e.ok(t, "POST", "/api/login", map[string]string{"password": "newpassword1"})
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t)
	e.ok(t, "POST", "/api/setup", map[string]string{"password": "longenough1"})
	e.ok(t, "POST", "/api/logout", map[string]string{})
	var last int
	for i := 0; i < 6; i++ {
		last, _, _ = e.call(t, "POST", "/api/login", map[string]string{"password": "nope-nope"}, true)
	}
	if last != 429 {
		t.Fatalf("после серии неудач ожидалась блокировка 429, получен %d", last)
	}
	if code, _, _ := e.call(t, "POST", "/api/login", map[string]string{"password": "longenough1"}, true); code != 429 {
		t.Fatalf("во время блокировки даже верный пароль отклоняется: %d", code)
	}
}

func TestAccountsJobsRunLifecycle(t *testing.T) {
	e := setup(t)
	e.ok(t, "POST", "/api/setup", map[string]string{"password": "longenough1"})

	// аккаунты: секрет не возвращается, слаг уникален, пустой секрет при правке сохраняет старый
	a1, raw := e.ok(t, "POST", "/api/accounts", map[string]any{"name": "Личный", "fields": map[string]string{"token": "SECRET-TOKEN", "login": "me"}})
	if strings.Contains(raw, "SECRET-TOKEN") || a1["set"].(map[string]any)["token"] != true || a1["slug"] != "account" {
		t.Fatalf("аккаунт: %s", raw)
	}
	a2, _ := e.ok(t, "POST", "/api/accounts", map[string]any{"name": "Рабочий", "fields": map[string]string{"token": "T2"}})
	if a2["slug"] != "account-2" {
		t.Fatalf("слаг должен быть уникальным: %v", a2["slug"])
	}
	if code, _, _ := e.call(t, "POST", "/api/accounts", map[string]any{"name": "Без токена", "fields": map[string]string{}}, true); code != 400 {
		t.Fatalf("аккаунт без токена: %d", code)
	}
	if code, _, _ := e.call(t, "POST", "/api/accounts", map[string]any{"name": "X", "slug": "Bad Slug!", "fields": map[string]string{"token": "t"}}, true); code != 400 {
		t.Fatalf("неверный слаг: %d", code)
	}
	e.ok(t, "PUT", "/api/accounts/"+a1["id"].(string), map[string]any{"name": "Личный", "fields": map[string]string{"token": "", "login": "me2"}})
	if acc, _ := e.st.Get().Account(a1["id"].(string)); acc.Fields["token"] != "SECRET-TOKEN" || acc.Fields["login"] != "me2" {
		t.Fatalf("пустой секрет должен сохранить прежнее значение: %+v", acc.Fields)
	}
	if r, _ := e.ok(t, "POST", "/api/accounts/test", map[string]any{"id": a1["id"], "fields": map[string]string{}}); r["ok"] != true {
		t.Fatalf("проверка сохранённого аккаунта: %v", r)
	}
	if r, _ := e.ok(t, "POST", "/api/accounts/test", map[string]any{"fields": map[string]string{"token": ""}}); r["ok"] != false {
		t.Fatalf("проверка без токена должна вернуть ok=false: %v", r)
	}

	// задания: валидация
	job := map[string]any{"name": "Фото", "accountId": a1["id"], "enabled": true, "mode": "mirror", "sources": []string{e.src}, "remoteDir": "/Backups", "schedule": "0 3 * * *", "concurrency": 2}
	for name, mut := range map[string]func(map[string]any){
		"плохой cron":        func(j map[string]any) { j["schedule"] = "каждый день" },
		"нет источников":     func(j map[string]any) { j["sources"] = []string{} },
		"относительный путь": func(j map[string]any) { j["sources"] = []string{"relative/path"} },
		"часть 5 МБ":         func(j map[string]any) { j["archive"] = map[string]int{"partSizeMB": 5} },
		"нет аккаунта":       func(j map[string]any) { j["accountId"] = "nope" },
	} {
		bad := map[string]any{}
		for k, v := range job {
			bad[k] = v
		}
		mut(bad)
		if code, _, raw := e.call(t, "POST", "/api/jobs", bad, true); code != 400 {
			t.Fatalf("%s: ожидался 400, получен %d %s", name, code, raw)
		}
	}
	created, _ := e.ok(t, "POST", "/api/jobs", job)
	id := created["id"].(string)

	if code, _, _ := e.call(t, "DELETE", "/api/accounts/"+a1["id"].(string), nil, true); code != 409 {
		t.Fatalf("аккаунт с заданием удалять нельзя: %d", code)
	}

	// запуск
	e.ok(t, "POST", "/api/jobs/"+id+"/run", map[string]string{})
	var rec map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, _ := e.ok(t, "GET", "/api/runs?job="+id, nil)
		_ = resp
		_, raw := e.ok(t, "GET", "/api/runs?job="+id, nil)
		var arr []map[string]any
		_ = json.Unmarshal([]byte(raw), &arr)
		if len(arr) > 0 && arr[0]["status"] != "running" {
			rec = arr[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if rec == nil || rec["status"] != "success" {
		t.Fatalf("запуск не завершился успешно: %v", rec)
	}
	if got, _ := e.mem.Get("/Backups/" + filepath.Base(e.src) + "/sub/b.txt"); string(got) != "bbb" {
		t.Fatalf("файл не выгружен, в облаке: %v", e.mem.Files())
	}
	stats := rec["stats"].(map[string]any)
	if stats["uploaded"] != float64(2) {
		t.Fatalf("статистика: %v", stats)
	}
	_, logRaw := e.ok(t, "GET", "/api/runs/"+rec["id"].(string)+"/log?offset=0", nil)
	if !strings.Contains(logRaw, "итог: success") || !strings.Contains(logRaw, "выгружено:") {
		t.Fatalf("журнал: %s", logRaw)
	}
	if code, _, _ := e.call(t, "GET", "/api/runs/..%2F..%2Fetc%2Fpasswd/log", nil, true); code == 200 {
		t.Fatal("путь в идентификаторе журнала не должен читаться")
	}

	// список заданий содержит последний запуск и время следующего
	_, raw = e.ok(t, "GET", "/api/jobs", nil)
	var jobs []map[string]any
	_ = json.Unmarshal([]byte(raw), &jobs)
	if len(jobs) != 1 || jobs[0]["last"] == nil || jobs[0]["next"] == nil || jobs[0]["accountName"] != "Личный" {
		t.Fatalf("список заданий: %s", raw)
	}

	// шлюз, обзор папок, настройки, статика
	g, _ := e.ok(t, "GET", "/api/gateway", nil)
	accs := g["accounts"].([]any)
	if g["password"] == "" || len(accs) != 2 || !strings.HasPrefix(accs[0].(map[string]any)["url"].(string), "http://127.0.0.1:8766/") {
		t.Fatalf("шлюз: %v", g)
	}
	old := g["password"]
	if n, _ := e.ok(t, "POST", "/api/gateway/regenerate", map[string]string{}); n["password"] == old || n["password"] == "" {
		t.Fatal("пароль шлюза не сменился")
	}
	fsr, _ := e.ok(t, "GET", "/api/fs?path="+e.src, nil)
	if dirs := fsr["dirs"].([]any); len(dirs) != 1 || dirs[0].(map[string]any)["name"] != "sub" {
		t.Fatalf("обзор папок: %v", fsr)
	}
	tmp := filepath.Join(t.TempDir(), "mytmp")
	e.ok(t, "PUT", "/api/settings", map[string]string{"tmpDir": tmp})
	if fi, err := os.Stat(tmp); err != nil || !fi.IsDir() {
		t.Fatalf("временная папка не создана: %v", err)
	}
	if code, _, _ := e.call(t, "PUT", "/api/settings", map[string]string{"tmpDir": "relative"}, true); code != 400 {
		t.Fatalf("относительная временная папка: %d", code)
	}
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "app.js") || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("статика: %d", resp.StatusCode)
	}

	// удаление
	e.ok(t, "DELETE", "/api/jobs/"+id, nil)
	e.ok(t, "DELETE", "/api/accounts/"+a1["id"].(string), nil)
}
