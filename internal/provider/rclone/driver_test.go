package rclone

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"nascloud/internal/provider"
)

func newLocal(t *testing.T) provider.Provider {
	t.Helper()
	p, err := New("local", map[string]string{"root": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDriverFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := newLocal(t)
	data := []byte("hello rclone")
	if err := p.Mkdir(ctx, "/docs"); err != nil {
		t.Fatal(err)
	}
	if err := p.Upload(ctx, "/docs/a.txt", provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}); err != nil {
		t.Fatal(err)
	}
	st, err := p.Stat(ctx, "/docs/a.txt")
	if err != nil || st.Size != int64(len(data)) || st.MD5 == "" {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
	if d, err := p.Stat(ctx, "/docs"); err != nil || !d.IsDir {
		t.Fatalf("Stat dir = %+v, %v", d, err)
	}
	rc, err := p.Download(ctx, "/docs/a.txt", 6, 6)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "rclone" {
		t.Fatalf("range read = %q, want %q", got, "rclone")
	}
	if _, err := p.Stat(ctx, "/missing"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("Stat missing = %v, want ErrNotFound", err)
	}
}

func TestDriverMoveCopyDelete(t *testing.T) {
	ctx := context.Background()
	p := newLocal(t)
	body := []byte("x")
	up := func(name string) {
		if err := p.Upload(ctx, name, provider.Source{Size: 1, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }}); err != nil {
			t.Fatal(err)
		}
	}
	up("/a.txt")
	up("/b.txt")
	if err := p.Copy(ctx, "/a.txt", "/b.txt", false); !errors.Is(err, provider.ErrExists) {
		t.Fatalf("Copy без перезаписи = %v, want ErrExists", err)
	}
	if err := p.Move(ctx, "/a.txt", "/c.txt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "/a.txt"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("исходный файл остался после переноса: %v", err)
	}
	if err := p.Mkdir(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	up("/dir/f.txt")
	if err := p.Move(ctx, "/dir", "/moved", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "/moved/f.txt"); err != nil {
		t.Fatalf("папка не перенеслась: %v", err)
	}
	if err := p.Delete(ctx, "/moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "/moved"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("папка не удалилась: %v", err)
	}
	if err := p.Delete(ctx, "/"); err == nil {
		t.Fatal("удаление корня должно быть ошибкой")
	}
}
