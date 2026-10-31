package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.10.0", "1.9.0", true},
		{"v2.0.0", "1.9.9", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.1", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// release поднимает фейковый API GitHub: релиз 1.0.1 с пакетом, суммами и подписью.
func release(t *testing.T, sign func(data []byte) []byte, pkg string) *httptest.Server {
	t.Helper()
	sums := []byte("abc  YaDiskBackup-1.0.1.spk\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": "v1.0.1", "html_url": "https://example/rel",
				"assets": []map[string]string{
					{"name": pkg + "-1.0.1.spk", "browser_download_url": "http://" + r.Host + "/spk"},
					{"name": "SHA256SUMS", "browser_download_url": "http://" + r.Host + "/sums"},
					{"name": "SHA256SUMS.sig", "browser_download_url": "http://" + r.Host + "/sig"},
				},
			})
		case "/sums":
			_, _ = w.Write(sums)
		case "/sig":
			_, _ = w.Write(sign(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func keys(t *testing.T) (pub string, sign func([]byte) []byte) {
	t.Helper()
	p, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(p), func(d []byte) []byte { return ed25519.Sign(priv, d) }
}

func TestCheckAcceptsSignedRelease(t *testing.T) {
	pub, sign := keys(t)
	srv := release(t, sign, "YaDiskBackup")
	rel, ok, err := Check(context.Background(), srv.Client(), srv.URL, "o/r", pub, "YaDiskBackup", "1.0.0")
	if err != nil || !ok || rel.Version != "1.0.1" || rel.URL != "https://example/rel" {
		t.Fatalf("Check: %+v %v %v", rel, ok, err)
	}
}

func TestCheckRejectsBadSignature(t *testing.T) {
	pub, _ := keys(t)
	_, other := keys(t) // подпись другим ключом
	srv := release(t, other, "YaDiskBackup")
	if _, ok, err := Check(context.Background(), srv.Client(), srv.URL, "o/r", pub, "YaDiskBackup", "1.0.0"); ok || err == nil {
		t.Fatalf("подпись чужим ключом должна отклоняться: ok=%v err=%v", ok, err)
	}
}

func TestCheckSkipsWithoutRepoAndWithoutNewerVersion(t *testing.T) {
	pub, sign := keys(t)
	srv := release(t, sign, "YaDiskBackup")
	if _, ok, _ := Check(context.Background(), srv.Client(), srv.URL, "", pub, "YaDiskBackup", "1.0.0"); ok {
		t.Fatal("без репозитория проверка должна быть выключена")
	}
	if _, ok, _ := Check(context.Background(), srv.Client(), srv.URL, "o/r", pub, "YaDiskBackup", "1.0.1"); ok {
		t.Fatal("установлена та же версия — обновления нет")
	}
}

func TestCheckNoReleaseIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	if _, ok, err := Check(context.Background(), srv.Client(), srv.URL, "o/r", "", "YaDiskBackup", "1.0.0"); ok || err != nil {
		t.Fatalf("404 должен быть «обновлений нет»: ok=%v err=%v", ok, err)
	}
}
