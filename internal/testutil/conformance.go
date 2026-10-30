package testutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"sort"
	"testing"

	"nascloud/internal/provider"
)

// Conformance проверяет, что драйвер ведёт себя по контракту provider.Provider.
func Conformance(t *testing.T, p provider.Provider) {
	t.Helper()
	ctx := context.Background()

	info, err := p.Check(ctx)
	if err != nil || info.Title == "" {
		t.Fatalf("Check: %+v, %v", info, err)
	}

	// папки
	if err := provider.MkdirAll(ctx, p, "/a/b/c"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := p.Mkdir(ctx, "/a/b/c"); err != nil {
		t.Fatalf("повторный Mkdir должен быть идемпотентным: %v", err)
	}
	if err := p.Mkdir(ctx, "/nope/x"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("Mkdir без родителя: ожидался ErrNotFound, получено %v", err)
	}

	// загрузка и перезапись
	up := func(path string, data []byte) {
		t.Helper()
		src := provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
		if err := p.Upload(ctx, path, src); err != nil {
			t.Fatalf("Upload %s: %v", path, err)
		}
	}
	up("/a/b/c/f.txt", []byte("hello world"))
	up("/a/b/c/f.txt", []byte("hi"))
	e, err := p.Stat(ctx, "/a/b/c/f.txt")
	if err != nil || e.IsDir || e.Size != 2 {
		t.Fatalf("Stat после перезаписи: %+v, %v", e, err)
	}
	up("/a/b/c/empty.txt", nil)
	up("/a/b/c/файл с пробелом #1.txt", []byte("юникод"))
	if err := p.Upload(ctx, "/missing/dir/x.txt", provider.Source{Size: 1, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader([]byte("x"))), nil }}); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("Upload без родительской папки: ожидался ErrNotFound, получено %v", err)
	}

	// список
	list, err := p.List(ctx, "/a/b/c")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	want := []string{"empty.txt", "f.txt", "файл с пробелом #1.txt"}
	if len(names) != len(want) {
		t.Fatalf("List: %v, ожидалось %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("List: %v, ожидалось %v", names, want)
		}
	}
	top, err := p.List(ctx, "/a/b")
	if err != nil || len(top) != 1 || !top[0].IsDir || top[0].Name != "c" {
		t.Fatalf("List папки с подпапкой: %+v, %v", top, err)
	}
	if _, err := p.List(ctx, "/zzz"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("List несуществующей: %v", err)
	}

	// скачивание с диапазонами
	data := make([]byte, 300_000)
	rand.New(rand.NewSource(1)).Read(data)
	up("/a/b/big.bin", data)
	read := func(off, n int64) []byte {
		t.Helper()
		rc, err := p.Download(ctx, "/a/b/big.bin", off, n)
		if err != nil {
			t.Fatalf("Download(%d,%d): %v", off, n, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("чтение: %v", err)
		}
		return b
	}
	if !bytes.Equal(read(0, -1), data) {
		t.Fatal("полное скачивание не совпало")
	}
	if !bytes.Equal(read(1000, 500), data[1000:1500]) {
		t.Fatal("диапазон из середины не совпал")
	}
	if !bytes.Equal(read(299_000, -1), data[299_000:]) {
		t.Fatal("хвост не совпал")
	}
	if _, err := p.Download(ctx, "/a/b/none.bin", 0, -1); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("Download несуществующего: %v", err)
	}

	// копирование, перемещение, удаление
	if err := p.Copy(ctx, "/a/b/c/f.txt", "/a/b/g.txt", false); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if err := p.Move(ctx, "/a/b/g.txt", "/a/b/h.txt", false); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := p.Stat(ctx, "/a/b/g.txt"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("после Move исходник должен исчезнуть: %v", err)
	}
	if e, err := p.Stat(ctx, "/a/b/h.txt"); err != nil || e.Size != 2 {
		t.Fatalf("Stat после Move: %+v, %v", e, err)
	}
	if err := p.Copy(ctx, "/a/b/c/f.txt", "/a/b/h.txt", false); !errors.Is(err, provider.ErrExists) {
		t.Fatalf("Copy без overwrite поверх существующего: ожидался ErrExists, получено %v", err)
	}
	if err := p.Delete(ctx, "/a/b/h.txt"); err != nil {
		t.Fatalf("Delete файла: %v", err)
	}
	if err := p.Delete(ctx, "/a"); err != nil {
		t.Fatalf("Delete папки: %v", err)
	}
	if _, err := p.Stat(ctx, "/a/b/c/f.txt"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("после рекурсивного Delete файл должен исчезнуть: %v", err)
	}
	if err := p.Delete(ctx, "/a"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("повторный Delete: ожидался ErrNotFound, получено %v", err)
	}
}
