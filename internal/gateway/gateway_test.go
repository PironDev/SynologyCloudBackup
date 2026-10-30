package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nascloud/internal/engine"
	"nascloud/internal/gateway"
	"nascloud/internal/httpx"
	"nascloud/internal/provider/webdav"
	"nascloud/internal/secret"
	"nascloud/internal/store"
	"nascloud/internal/testutil"
)

func setup(t *testing.T) (*httptest.Server, *testutil.Mem, store.Config) {
	t.Helper()
	httpx.Backoff = time.Millisecond
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	st, err := store.Open(dir, box, []string{"token"})
	if err != nil {
		t.Fatal(err)
	}
	mem := testutil.NewMem()
	err = st.Update(func(c *store.Config) error {
		c.Accounts = []store.Account{{ID: "acc1", Name: "Тестовый", Slug: "test", Fields: map[string]string{"token": "t"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	gw := &gateway.Server{Store: st, Pool: engine.NewPool(testutil.Descriptor(mem)), TmpDir: func() string { return dir }}
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	return srv, mem, st.Get()
}

// Наш WebDAV-клиент (его же использует сборка для Mail.ru) против шлюза:
// проверяет протокол в обе стороны.
func TestWebDAVClientAgainstGateway(t *testing.T) {
	srv, _, cfg := setup(t)
	c, err := webdav.New(webdav.Config{BaseURL: srv.URL + "/test", User: "nas", Password: cfg.GatewayPassword})
	if err != nil {
		t.Fatal(err)
	}
	c.VerifyDelay = time.Millisecond
	testutil.Conformance(t, c)
}

func TestAuthRequired(t *testing.T) {
	srv, _, cfg := setup(t)
	resp, _ := http.Get(srv.URL + "/test/")
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("без пароля ожидался 401, получен %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/test/", nil)
	req.SetBasicAuth("x", "wrong")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Fatalf("с неверным паролем ожидался 401, получен %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("PROPFIND", srv.URL+"/test/", nil)
	req.Header.Set("Depth", "0")
	req.SetBasicAuth("x", cfg.GatewayPassword)
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 207 {
		t.Fatalf("с верным паролем ожидался 207, получен %d", resp.StatusCode)
	}
}

func do(t *testing.T, method, url, pw string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, body)
	req.SetBasicAuth("nas", pw)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRootListsAccountsAndRangeGet(t *testing.T) {
	srv, mem, cfg := setup(t)
	pw := cfg.GatewayPassword

	resp := do(t, "PROPFIND", srv.URL+"/", pw, nil, map[string]string{"Depth": "1"})
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 207 || !strings.Contains(string(b), "/test/") {
		t.Fatalf("корень шлюза должен перечислять аккаунты: %d %s", resp.StatusCode, b)
	}

	mem.Put("/docs/report.txt", []byte("0123456789"))
	resp = do(t, "GET", srv.URL+"/test/docs/report.txt", pw, nil, map[string]string{"Range": "bytes=2-5"})
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || string(b) != "2345" || resp.Header.Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("Range: %d %q %q", resp.StatusCode, b, resp.Header.Get("Content-Range"))
	}
	resp = do(t, "GET", srv.URL+"/test/docs/report.txt", pw, nil, map[string]string{"Range": "bytes=-3"})
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || string(b) != "789" {
		t.Fatalf("суффиксный Range: %d %q", resp.StatusCode, b)
	}
	resp = do(t, "GET", srv.URL+"/test/docs/report.txt", pw, nil, map[string]string{"Range": "bytes=50-60"})
	if resp.StatusCode != 416 {
		t.Fatalf("Range за пределами файла: ожидался 416, получен %d", resp.StatusCode)
	}
	resp = do(t, "HEAD", srv.URL+"/test/docs/report.txt", pw, nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Length") != "10" {
		t.Fatalf("HEAD: %d %s", resp.StatusCode, resp.Header.Get("Content-Length"))
	}
	if resp := do(t, "GET", srv.URL+"/unknown/x", pw, nil, nil); resp.StatusCode != 404 {
		t.Fatalf("неизвестный аккаунт: %d", resp.StatusCode)
	}
	if resp := do(t, "PUT", srv.URL+"/", pw, strings.NewReader("x"), nil); resp.StatusCode != 403 {
		t.Fatalf("запись в корень шлюза должна быть запрещена: %d", resp.StatusCode)
	}
	if resp := do(t, "PUT", srv.URL+"/x.txt", pw, strings.NewReader("x"), nil); resp.StatusCode != 404 {
		t.Fatalf("файл вне аккаунтов не должен создаваться: %d", resp.StatusCode)
	}
}

func TestChunkedPutAndMoveViaDestination(t *testing.T) {
	srv, mem, cfg := setup(t)
	pw := cfg.GatewayPassword
	mem.Put("/d/seed", nil)

	// тело неизвестной длины (chunked) уходит через временный файл
	req, _ := http.NewRequest("PUT", srv.URL+"/test/d/chunked.txt", io.NopCloser(strings.NewReader("chunked body")))
	req.ContentLength = -1
	req.SetBasicAuth("nas", pw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 201 {
		t.Fatalf("chunked PUT: %v %v", resp, err)
	}
	if got, _ := mem.Get("/d/chunked.txt"); string(got) != "chunked body" {
		t.Fatalf("содержимое: %q", got)
	}

	resp = do(t, "MOVE", srv.URL+"/test/d/chunked.txt", pw, nil, map[string]string{"Destination": srv.URL + "/test/d/moved.txt", "Overwrite": "F"})
	if resp.StatusCode != 201 {
		t.Fatalf("MOVE: %d", resp.StatusCode)
	}
	if _, ok := mem.Get("/d/moved.txt"); !ok {
		t.Fatal("файл не перемещён")
	}
	resp = do(t, "MOVE", srv.URL+"/test/d/moved.txt", pw, nil, map[string]string{"Destination": srv.URL + "/test/d/seed", "Overwrite": "F"})
	if resp.StatusCode != 412 {
		t.Fatalf("MOVE на существующий без Overwrite: ожидался 412, получен %d", resp.StatusCode)
	}
	resp = do(t, "MOVE", srv.URL+"/test/d/moved.txt", pw, nil, map[string]string{"Destination": srv.URL + "/other/d/x"})
	if resp.StatusCode != 502 {
		t.Fatalf("MOVE между аккаунтами: ожидался 502, получен %d", resp.StatusCode)
	}
	if resp := do(t, "LOCK", srv.URL+"/test/d/seed", pw, nil, nil); resp.StatusCode != 200 || resp.Header.Get("Lock-Token") == "" {
		t.Fatalf("LOCK: %d", resp.StatusCode)
	}
}
