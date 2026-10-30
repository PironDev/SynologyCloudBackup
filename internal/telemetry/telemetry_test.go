package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScrub(t *testing.T) {
	in := `open /volume1/homes/ivan/Фото/a.jpg: permission denied; user ivan@mail.ru token y0_AgAAAAAbcdefghijklmnop from 192.168.1.5 GET https://cloud-api.yandex.net/v1/disk?path=%2Fsecret задание «Мои фото»`
	out := Scrub(in)
	for _, bad := range []string{"/volume1", "ivan", "y0_", "192.168", "%2Fsecret", "Мои фото"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q осталось в %q", bad, out)
		}
	}
	if !strings.Contains(out, "permission denied") {
		t.Fatalf("потерян смысл: %q", out)
	}
}

func TestCapture(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.URL.Path + "|" + r.Header.Get("X-Sentry-Auth") + "|" + string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	on := false
	t.Setenv("CB_SENTRY_DSN", strings.Replace(srv.URL, "http://", "http://pubkey@", 1)+"/7")
	Init("yadisk", "1.0.0", func() bool { return on }, func() string { return "inst" })
	Capture("job", "boom /volume1/x/y", nil)
	Flush(time.Second)
	if len(got) != 0 {
		t.Fatal("отправлено без согласия")
	}
	on = true
	Capture("job", "boom /volume1/x/y", map[string]string{"mode": "mirror"})
	Capture("job", "boom /volume1/x/y", nil) // дубль
	Flush(2 * time.Second)
	if len(got) != 1 {
		t.Fatalf("ожидалась 1 отправка, получено %d", len(got))
	}
	s := <-got
	if !strings.HasPrefix(s, "/api/7/envelope/|") || !strings.Contains(s, "sentry_key=pubkey") || !strings.Contains(s, `\u003cpath`) || strings.Contains(s, "/volume1") {
		t.Fatalf("плохой запрос: %s", s)
	}
}
