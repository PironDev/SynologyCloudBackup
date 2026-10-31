package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"nascloud/internal/engine"
	"nascloud/internal/secret"
	"nascloud/internal/store"
	"nascloud/internal/testutil"
	"nascloud/internal/web"
)

// За CGI-мостом ошибки 401/403/404/500… уходят как 422: иначе DSM подменяет ответ своей
// страницей «не найдена», и текст ошибки до интерфейса не доходит.
func TestProxyHidesDSMInterceptedCodes(t *testing.T) {
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	desc := testutil.Descriptor(testutil.NewMem())
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
	s := web.New(web.Deps{Title: "Тест", Store: st, Mgr: mgr, Pool: pool, GatewayAddr: "127.0.0.1:8766", Version: "test"})

	req := httptest.NewRequest(http.MethodGet, "/api/runs/nope/log", nil)
	req.Header.Set("X-Proxy-Secret", "sec")
	req.Header.Set("X-DSM-User", "admin")
	rec := httptest.NewRecorder()
	s.ProxyHandler("sec").ServeHTTP(rec, req)

	var body struct{ Error string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("X-Cloud-Status") != "404" || body.Error == "" {
		t.Fatalf("proxy: status %d, X-Cloud-Status %q, body %s; want 422, 404 and error text",
			rec.Code, rec.Header().Get("X-Cloud-Status"), rec.Body.String())
	}

	// Прямой интерфейс получает код как есть: фронт по 401 понимает, что сессия кончилась.
	direct := httptest.NewRecorder()
	s.Handler().ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "/api/runs/nope/log", nil))
	if direct.Code != http.StatusUnauthorized {
		t.Fatalf("direct: status %d, want 401", direct.Code)
	}
}
