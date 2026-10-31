package dsm_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nascloud/internal/dsm"
	"nascloud/internal/engine"
	"nascloud/internal/secret"
	"nascloud/internal/store"
	"nascloud/internal/testutil"
	"nascloud/internal/web"
)

// fakeAuth имитирует authenticate.cgi: имя пользователя зависит от cookie.
func fakeAuth(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "authenticate.cgi")
	script := `#!/bin/sh
case "$HTTP_COOKIE" in
*id=admin*) printf 'Content-Type: text/plain\n\npavel\n' ;;
*id=user*) echo vasya ;;
*id=evil*) echo '../../etc;rm -rf' ;;
*) exit 1 ;;
esac
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func groupFile(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "group")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAuthUserAndAllowed(t *testing.T) {
	dir := t.TempDir()
	a := dsm.Auth{Program: fakeAuth(t, dir), GroupFile: groupFile(t, dir, "root:x:0:\nadministrators:x:101:admin,pavel\nusers:x:100:vasya\n")}

	if u, err := a.User([]string{"HTTP_COOKIE=id=admin"}); err != nil || u != "pavel" {
		t.Fatalf("admin: %q %v", u, err)
	}
	if u, err := a.User([]string{"HTTP_COOKIE=id=user"}); err != nil || u != "vasya" {
		t.Fatalf("user: %q %v", u, err)
	}
	if _, err := a.User([]string{"HTTP_COOKIE=id=none"}); !errors.Is(err, dsm.ErrNoSession) {
		t.Fatalf("без сессии ожидалась ErrNoSession, получено %v", err)
	}
	if _, err := a.User([]string{"HTTP_COOKIE=id=evil"}); !errors.Is(err, dsm.ErrNoSession) {
		t.Fatalf("подозрительное имя должно отклоняться: %v", err)
	}
	if _, err := (dsm.Auth{Program: filepath.Join(dir, "missing")}).User(nil); err == nil || errors.Is(err, dsm.ErrNoSession) {
		t.Fatalf("отсутствующая программа — отдельная ошибка, получено %v", err)
	}

	if ok, err := a.Allowed("pavel"); !ok || err != nil {
		t.Fatalf("pavel — администратор: %v %v", ok, err)
	}
	if ok, _ := a.Allowed("PAVEL"); !ok {
		t.Fatal("имена сравниваются без учёта регистра")
	}
	if ok, err := a.Allowed("vasya"); ok || err != nil {
		t.Fatalf("vasya не администратор: %v %v", ok, err)
	}
	a.Allow = []string{"vasya"}
	if ok, _ := a.Allowed("vasya"); !ok {
		t.Fatal("явно разрешённый пользователь должен проходить")
	}
	a.Allow = []string{"*"}
	if ok, _ := (dsm.Auth{Allow: []string{"*"}}).Allowed("anyone"); !ok {
		t.Fatal("* разрешает любого пользователя DSM без чтения групп")
	}
	if ok, err := (dsm.Auth{GroupFile: filepath.Join(dir, "nope")}).Allowed("pavel"); ok || err == nil {
		t.Fatalf("без файла групп доступ закрыт: %v %v", ok, err)
	}
	if ok, err := (dsm.Auth{GroupFile: groupFile(t, dir, "root:x:0:\n")}).Allowed("pavel"); ok || err == nil {
		t.Fatalf("без группы administrators доступ закрыт: %v %v", ok, err)
	}
}

type rig struct {
	cgi     dsm.CGI
	direct  *httptest.Server // режим с паролем (без моста)
	proxy   *httptest.Server
	dataDir string
}

func newRig(t *testing.T) *rig {
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
	srv := web.New(web.Deps{Title: "Тест", Store: st, Mgr: mgr, Pool: pool, GatewayAddr: "127.0.0.1:8766", Version: "test"})

	const sec = "s3cret"
	proxy := httptest.NewServer(srv.ProxyHandler(sec))
	t.Cleanup(proxy.Close)
	direct := httptest.NewServer(srv.Handler())
	t.Cleanup(direct.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(proxy.URL, "http://"))
	if err := dsm.WriteProxyInfo(dir, dsm.ProxyInfo{Addr: "127.0.0.1:" + port, Secret: sec}); err != nil {
		t.Fatal(err)
	}
	auth := dsm.Auth{Program: fakeAuth(t, dir), GroupFile: groupFile(t, dir, "administrators:x:101:admin,pavel\n")}
	return &rig{cgi: dsm.CGI{DataDir: dir, Auth: auth, Base: "index.cgi?r="}, direct: direct, proxy: proxy, dataDir: dir}
}

type cgiResult struct {
	status  int
	headers map[string]string
	body    string
}

func (r *rig) call(t *testing.T, method, query, cookie string, hdr map[string]string, body string) cgiResult {
	t.Helper()
	env := []string{"PATH=" + os.Getenv("PATH"), "REQUEST_METHOD=" + method, "QUERY_STRING=" + query}
	if cookie != "" {
		env = append(env, "HTTP_COOKIE="+cookie)
	}
	if body != "" {
		env = append(env, "CONTENT_LENGTH="+itoa(len(body)), "CONTENT_TYPE=application/json")
	}
	for k, v := range hdr {
		env = append(env, k+"="+v)
	}
	var out bytes.Buffer
	r.cgi.Run(env, strings.NewReader(body), &out)
	return parseCGI(t, out.String())
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func parseCGI(t *testing.T, raw string) cgiResult {
	t.Helper()
	head, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("неверный формат CGI-ответа: %q", raw)
	}
	res := cgiResult{headers: map[string]string{}, body: body}
	for i, line := range strings.Split(head, "\r\n") {
		k, v, _ := strings.Cut(line, ": ")
		if i == 0 && k == "Status" {
			var code int
			_ = json.Unmarshal([]byte(strings.Fields(v)[0]), &code)
			res.status = code
		}
		res.headers[k] = v
	}
	return res
}

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("не JSON: %q", s)
	}
	return m
}

func TestCGIEndToEnd(t *testing.T) {
	r := newRig(t)
	csrf := map[string]string{"HTTP_X_REQUESTED_WITH": "cloudbackup"}

	// страница и ресурсы идут через мост без проверки сессии
	page := r.call(t, "GET", "r=%2F", "", nil, "")
	if page.status != 200 || !strings.Contains(page.body, `src="index.cgi?r=%2Fapp.js"`) ||
		!strings.Contains(page.body, `content="index.cgi?r="`) || !strings.Contains(page.headers["Content-Type"], "text/html") {
		t.Fatalf("страница: %d %s", page.status, page.body)
	}
	if js := r.call(t, "GET", "r=%2Fapp.js", "", nil, ""); js.status != 200 || !strings.Contains(js.headers["Content-Type"], "javascript") {
		t.Fatalf("app.js: %d %v", js.status, js.headers)
	}

	// администратор DSM
	st := jsonOf(t, r.call(t, "GET", "r=%2Fapi%2Fstate", "id=admin", nil, "").body)
	if st["loggedIn"] != true || st["auth"] != "dsm" || st["user"] != "pavel" || st["needSetup"] != false {
		t.Fatalf("состояние администратора: %v", st)
	}
	// без сессии
	st = jsonOf(t, r.call(t, "GET", "r=%2Fapi%2Fstate", "id=none", nil, "").body)
	if st["loggedIn"] != false || st["auth"] != "dsm" || !strings.Contains(st["reason"].(string), "сессии DSM") {
		t.Fatalf("состояние без сессии: %v", st)
	}
	// не администратор
	st = jsonOf(t, r.call(t, "GET", "r=%2Fapi%2Fstate", "id=user", nil, "").body)
	if st["loggedIn"] != false || !strings.Contains(st["reason"].(string), "не входит в группу администраторов") {
		t.Fatalf("состояние не администратора: %v", st)
	}
	// Отказ за мостом приходит как 422 с текстом: код 401 DSM заменил бы своей страницей ошибки.
	if res := r.call(t, "GET", "r=%2Fapi%2Fjobs", "id=user", csrf, ""); res.status != 422 || !strings.Contains(res.body, `"error"`) {
		t.Fatalf("API для не администратора: %d", res.status)
	}
	// защищённый API без входа и с входом
	if res := r.call(t, "GET", "r=%2Fapi%2Faccounts", "", csrf, ""); res.status != 422 || !strings.Contains(res.body, `"error"`) {
		t.Fatalf("API без сессии: %d", res.status)
	}
	if res := r.call(t, "GET", "r=%2Fapi%2Faccounts", "id=admin", csrf, ""); res.status != 200 {
		t.Fatalf("API администратора: %d %s", res.status, res.body)
	}

	// запись: нужен CSRF-заголовок, тело и Content-Type доходят до службы
	acc := `{"name":"Личный","fields":{"token":"T"}}`
	if res := r.call(t, "POST", "r=%2Fapi%2Faccounts", "id=admin", nil, acc); res.status != 422 {
		t.Fatalf("без X-Requested-With ожидался 422 (403 за мостом), получен %d", res.status)
	}
	res := r.call(t, "POST", "r=%2Fapi%2Faccounts", "id=admin", csrf, acc)
	if res.status != 200 && res.status != 201 {
		t.Fatalf("создание аккаунта: %d %s", res.status, res.body)
	}
	if strings.Contains(res.body, `"T"`) || jsonOf(t, res.body)["set"].(map[string]any)["token"] != true {
		t.Fatalf("секрет не должен возвращаться: %s", res.body)
	}
	// query вложенного запроса сохраняется целиком
	if res := r.call(t, "GET", "r=%2Fapi%2Fruns%3Flimit%3D5", "id=admin", csrf, ""); res.status != 200 {
		t.Fatalf("вложенный query: %d %s", res.status, res.body)
	}
	// пароль-режим недоступен за мостом
	if res := r.call(t, "POST", "r=%2Fapi%2Flogin", "id=admin", csrf, `{"password":"x"}`); res.status != 400 {
		t.Fatalf("вход по паролю за мостом должен отклоняться: %d", res.status)
	}
}

func TestCGIRejectsBadTargets(t *testing.T) {
	r := newRig(t)
	for _, q := range []string{"r=%2F%2Fevil.example%2F", "r=http%3A%2F%2Fevil", "r=api%2Fstate", "r=%2Fapi%5C..", "r=%2Fx%0D%0AInjected%3A1"} {
		if res := r.call(t, "GET", q, "id=admin", nil, ""); res.status != 400 {
			t.Errorf("%q: ожидался 400, получен %d", q, res.status)
		}
	}
}

func TestCGIWithoutRunningService(t *testing.T) {
	r := newRig(t)
	_ = os.Remove(filepath.Join(r.dataDir, dsm.ProxyFile))
	res := r.call(t, "GET", "r=%2F", "", nil, "")
	if res.status != 502 || !strings.Contains(jsonOf(t, res.body)["error"].(string), "запустите пакет") {
		t.Fatalf("служба не запущена: %d %s", res.status, res.body)
	}
	// файл есть, но порт закрыт
	_ = dsm.WriteProxyInfo(r.dataDir, dsm.ProxyInfo{Addr: "127.0.0.1:1", Secret: "x"})
	if res := r.call(t, "GET", "r=%2F", "", nil, ""); res.status != 502 {
		t.Fatalf("порт закрыт: %d", res.status)
	}
}

// Внутренний интерфейс принимает только запросы с секретом, а прямой интерфейс с паролем
// игнорирует заголовок X-DSM-User, который может подделать кто угодно в сети.
func TestTrustBoundaries(t *testing.T) {
	r := newRig(t)
	do := func(url string, h map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", url, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	if resp, _ := do(r.proxy.URL+"/api/state", nil); resp.StatusCode != 403 {
		t.Fatalf("внутренний интерфейс без секрета: %d", resp.StatusCode)
	}
	if resp, _ := do(r.proxy.URL+"/api/state", map[string]string{"X-Proxy-Secret": "wrong", "X-DSM-User": "pavel"}); resp.StatusCode != 403 {
		t.Fatalf("внутренний интерфейс с неверным секретом: %d", resp.StatusCode)
	}
	_, body := do(r.direct.URL+"/api/state", map[string]string{"X-DSM-User": "pavel", "X-Proxy-Secret": "s3cret", "X-Api-Base": "x"})
	st := jsonOf(t, body)
	if st["loggedIn"] != false || st["auth"] != "password" {
		t.Fatalf("прямой интерфейс не должен доверять заголовкам: %v", st)
	}
	if resp, _ := do(r.direct.URL+"/api/accounts", map[string]string{"X-DSM-User": "pavel", "X-Requested-With": "cloudbackup"}); resp.StatusCode != 401 {
		t.Fatalf("подделка X-DSM-User на прямом интерфейсе: %d", resp.StatusCode)
	}
	// встроенная страница не принимает произвольный api-base
	_, page := do(r.proxy.URL+"/", map[string]string{"X-Proxy-Secret": "s3cret", "X-Api-Base": `"><script>alert(1)</script>`})
	if strings.Contains(page, "<script>alert") {
		t.Fatal("api-base должен фильтроваться")
	}
}

// Файлы проводника идут через мост потоком: загрузка PUT и скачивание с Range.
func TestCGIFileTransfer(t *testing.T) {
	r := newRig(t)
	csrf := map[string]string{"HTTP_X_REQUESTED_WITH": "cloudbackup"}
	root := t.TempDir()
	name := filepath.Base(root)
	if res := r.call(t, "PUT", "r=%2Fapi%2Fsettings", "id=admin", csrf, `{"localRoots":["`+root+`"]}`); res.status != 200 {
		t.Fatalf("настройки: %d %s", res.status, res.body)
	}
	data := strings.Repeat("0123456789", 5000) // 50 КБ — больше, чем лимит обычных запросов не нужен, но проверяем целостность
	q := "r=" + url.QueryEscape("/api/x/file?src=local&path=/"+name+"&name=a.bin")
	if res := r.call(t, "PUT", q, "id=admin", csrf, data); res.status != 200 {
		t.Fatalf("загрузка: %d %s", res.status, res.body)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.bin")); string(b) != data {
		t.Fatalf("файл на диске повреждён (%d байт)", len(b))
	}
	dl := "r=" + url.QueryEscape("/api/x/file?src=local&path=/"+name+"/a.bin&dl=1")
	full := r.call(t, "GET", dl, "id=admin", nil, "")
	if full.status != 200 || full.body != data || full.headers["Content-Length"] != "50000" {
		t.Fatalf("скачивание: %d len=%d %v", full.status, len(full.body), full.headers)
	}
	part := r.call(t, "GET", dl, "id=admin", map[string]string{"HTTP_RANGE": "bytes=10-19"}, "")
	if part.status != 206 || part.body != "0123456789" || part.headers["Content-Range"] != "bytes 10-19/50000" {
		t.Fatalf("диапазон: %d %q %v", part.status, part.body, part.headers)
	}
	// без сессии файл не отдаётся
	if res := r.call(t, "GET", dl, "id=none", nil, ""); res.status != 422 {
		t.Fatalf("без сессии: %d", res.status)
	}
}
