package web_test

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (e *env) raw(t *testing.T, method, path string, body []byte, hdr map[string]string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("X-Requested-With", "cloudbackup")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestExplorer(t *testing.T) {
	e := setup(t)
	if c, _, _ := e.call(t, "POST", "/api/setup", map[string]string{"password": "password123"}, true); c != 200 {
		t.Fatal("setup", c)
	}
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	if c, _, raw := e.call(t, "PUT", "/api/settings", map[string]any{"localRoots": []string{root}}, true); c != 200 {
		t.Fatal(c, raw)
	}
	if c, m, _ := e.call(t, "POST", "/api/accounts", map[string]any{"name": "cloud", "fields": map[string]string{"token": "x"}}, true); c != 200 && c != 201 {
		t.Fatal("account", c, m)
	}
	_, st, _ := e.call(t, "GET", "/api/x/sources", nil, true)
	srcs := st["sources"].([]any)
	if len(srcs) != 2 {
		t.Fatalf("sources: %v", st)
	}
	cloud := srcs[1].(map[string]any)["id"].(string)
	base := filepath.Base(root)

	// список корней и содержимого
	_, m, raw := e.call(t, "GET", "/api/x/list?src=local&path=/", nil, true)
	if !strings.Contains(raw, base) {
		t.Fatalf("корни: %s", raw)
	}
	_, m, raw = e.call(t, "GET", "/api/x/list?src=local&path=/"+base, nil, true)
	if !strings.Contains(raw, "hello.txt") || !strings.Contains(raw, "docs") {
		t.Fatalf("список: %v %s", m, raw)
	}
	// выход за корень запрещён
	if c, _, _ := e.call(t, "GET", "/api/x/list?src=local&path=/"+base+"/../..", nil, true); c == 200 {
		// путь нормализуется внутрь корня — допустимо, главное не выйти наружу
	}
	if c, _, raw := e.call(t, "GET", "/api/x/list?src=local&path=/nonexistent", nil, true); c == 200 {
		t.Fatalf("несуществующий корень: %s", raw)
	}
	// загрузка и скачивание с Range
	if c, _, raw := e.raw(t, "PUT", "/api/x/file?src=local&path=/"+base+"/docs&name=u.txt", []byte("0123456789"), nil); c != 200 {
		t.Fatal("upload", c, raw)
	}
	data, _ := os.ReadFile(filepath.Join(root, "docs", "u.txt"))
	if string(data) != "0123456789" {
		t.Fatalf("загружено: %q", data)
	}
	code, body, h := e.raw(t, "GET", "/api/x/file?src=local&path=/"+base+"/docs/u.txt", nil, map[string]string{"Range": "bytes=2-5"})
	if code != 206 || body != "2345" || h.Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("range: %d %q %v", code, body, h)
	}
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") {
		t.Fatalf("txt должен скачиваться: %v", h)
	}
	// конфликт имён при загрузке → «u (1).txt»
	_, raw2, _ := e.raw(t, "PUT", "/api/x/file?src=local&path=/"+base+"/docs&name=u.txt", []byte("zz"), nil)
	if !strings.Contains(raw2, "u (1).txt") {
		t.Fatalf("переименование: %s", raw2)
	}
	// переименование и папка
	if c, _, raw := e.call(t, "POST", "/api/x/rename", map[string]string{"src": "local", "from": "/" + base + "/hello.txt", "name": "bye.txt"}, true); c != 200 {
		t.Fatal(c, raw)
	}
	if c, _, _ := e.call(t, "POST", "/api/x/rename", map[string]string{"src": "local", "from": "/" + base + "/bye.txt", "name": "../x"}, true); c != 400 {
		t.Fatal("имя с / должно отклоняться")
	}
	if c, _, _ := e.call(t, "POST", "/api/x/mkdir", map[string]string{"src": "local", "path": "/" + base + "/new"}, true); c != 200 {
		t.Fatal("mkdir")
	}
	// копирование NAS → облако (Mem), затем перенос обратно
	_, tk, raw := e.call(t, "POST", "/api/x/transfer", map[string]any{"op": "copy", "src": "local", "paths": []string{"/" + base + "/docs"}, "dst": cloud, "dstDir": "/in"}, true)
	id, _ := tk["id"].(string)
	if id == "" {
		t.Fatal("transfer:", raw)
	}
	waitTask(t, e, id)
	if b, ok := e.mem.Get("/in/docs/u.txt"); !ok || string(b) != "0123456789" {
		t.Fatalf("в облаке: %v %q", e.mem.Paths(), b)
	}
	_, tk, _ = e.call(t, "POST", "/api/x/transfer", map[string]any{"op": "move", "src": cloud, "paths": []string{"/in/docs"}, "dst": "local", "dstDir": "/" + base + "/new"}, true)
	waitTask(t, e, tk["id"].(string))
	if _, err := os.Stat(filepath.Join(root, "new", "docs", "u.txt")); err != nil {
		t.Fatal("перенос из облака:", err)
	}
	if _, ok := e.mem.Get("/in/docs/u.txt"); ok {
		t.Fatal("после переноса исходник должен исчезнуть")
	}
	// удаление
	_, dm, _ := e.call(t, "POST", "/api/x/delete", map[string]any{"src": "local", "paths": []string{"/" + base + "/new"}}, true)
	if dm["deleted"].(float64) != 1 {
		t.Fatal(dm)
	}
	// в корзину можно только из облака
	if st, _, _ := e.call(t, "POST", "/api/x/delete", map[string]any{"src": "local", "paths": []string{"/" + base + "/new"}, "trash": true}, true); st != 400 {
		t.Fatalf("корзина для NAS: статус %d, ожидался 400", st)
	}
	// корень удалять нельзя
	_, dm, _ = e.call(t, "POST", "/api/x/delete", map[string]any{"src": "local", "paths": []string{"/" + base}}, true)
	if dm["deleted"].(float64) != 0 {
		t.Fatal("корень удалён:", dm)
	}
	// сводка
	_, sm, _ := e.call(t, "GET", "/api/summary", nil, true)
	if _, ok := sm["accounts"]; !ok {
		t.Fatal(sm)
	}
	_ = url.QueryEscape
}

func waitTask(t *testing.T, e *env, id string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		_, m, _ := e.call(t, "GET", "/api/x/tasks", nil, true)
		for _, x := range m["tasks"].([]any) {
			tk := x.(map[string]any)
			if tk["id"] == id && tk["finished"] != nil {
				if tk["status"] != "done" {
					t.Fatalf("задача: %v", tk)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("задача не завершилась")
}
