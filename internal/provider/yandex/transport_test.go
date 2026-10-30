package yandex

import (
	"bytes"
	"context"
	"io"
	"nascloud/internal/provider"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebDAVTransport(t *testing.T) {
	var auth, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, method = r.Header.Get("Authorization"), r.Method
		w.WriteHeader(207)
		w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
	}))
	defer srv.Close()
	p, err := Descriptor().New(map[string]string{"token": "tok123", "transport": "webdav", "base_url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Check(context.Background()); err != nil {
		t.Logf("check: %v", err)
	}
	if auth != "OAuth tok123" || method != "PROPFIND" {
		t.Fatalf("auth=%q method=%q", auth, method)
	}
	if _, ok := mustAPI(t); !ok {
		t.Fatal("api transport должен остаться по умолчанию")
	}
}

func mustAPI(t *testing.T) (*Client, bool) {
	p, err := Descriptor().New(map[string]string{"token": "x"})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := p.(*Client)
	return c, ok
}

func TestUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.WriteHeader(200)
		w.Write([]byte(`{"total_space":10,"used_space":1,"user":{"display_name":"x"}}`))
	}))
	defer srv.Close()
	for _, tc := range []struct{ set, want string }{{"", "Yandex.Disk "}, {"MyAgent/9", "MyAgent/9"}} {
		p, _ := Descriptor().New(map[string]string{"token": "t", "base_url": srv.URL, "user_agent": tc.set})
		p.Check(context.Background())
		if tc.set == "" && !strings.HasPrefix(ua, tc.want) || tc.set != "" && ua != tc.want {
			t.Fatalf("ua=%q want %q", ua, tc.want)
		}
	}
}

func TestPublish(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/disk/resources/publish", "/v1/disk/resources/unpublish":
			w.Write([]byte(`{"href":"x","method":"GET"}`))
		default:
			w.Write([]byte(`{"public_url":"https://yadi.sk/d/abc"}`))
		}
	}))
	defer srv.Close()
	c, _ := New("t", srv.URL)
	u, err := c.Publish(context.Background(), "/a/b.txt")
	if err != nil || u != "https://yadi.sk/d/abc" {
		t.Fatalf("%q %v", u, err)
	}
	if err := c.Unpublish(context.Background(), "/a/b.txt"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0] != "PUT /v1/disk/resources/publish" || calls[2] != "PUT /v1/disk/resources/unpublish" {
		t.Fatalf("%v", calls)
	}
}

func TestEmptyTrashClearsTrash(t *testing.T) {
	c, fake := setup(t)
	ctx := context.Background()
	data := []byte("bin")
	if err := c.Upload(ctx, "/bin.bin", provider.Source{Size: 3, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}); err != nil {
		t.Fatal(err)
	}
	if err := c.Trash(ctx, "/bin.bin"); err != nil {
		t.Fatal(err)
	}
	if err := c.EmptyTrash(ctx); err != nil {
		t.Fatalf("EmptyTrash: %v", err)
	}
	if got := fake.Trashed(); len(got) != 0 {
		t.Fatalf("корзина не очищена: %v", got)
	}
}
