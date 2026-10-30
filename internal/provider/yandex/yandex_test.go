package yandex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"nascloud/internal/httpx"
	"nascloud/internal/provider"
	"nascloud/internal/testutil"
)

func setup(t *testing.T) (*Client, *testutil.YandexFake) {
	t.Helper()
	httpx.Backoff = time.Millisecond
	fake := testutil.NewYandexFake(testutil.NewMem(), "good-token")
	t.Cleanup(fake.Close)
	c, err := New("good-token", fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.PollDelay = time.Millisecond
	c.pace = httpx.NewPacer(time.Microsecond, time.Millisecond)
	return c, fake
}

func TestConformance(t *testing.T) {
	c, _ := setup(t)
	testutil.Conformance(t, c)
}

func TestAuthError(t *testing.T) {
	_, fake := setup(t)
	bad, _ := New("wrong", fake.URL)
	if _, err := bad.Check(context.Background()); !errors.Is(err, provider.ErrAuth) {
		t.Fatalf("ожидался ErrAuth, получено %v", err)
	}
}

func TestRetryOn503(t *testing.T) {
	c, fake := setup(t)
	fake.Fail503.Store(2)
	info, err := c.Check(context.Background())
	if err != nil || info.Title != "yauser" || info.Total != 1000 {
		t.Fatalf("Check после повторов: %+v, %v", info, err)
	}
}

func TestInsufficientStorage(t *testing.T) {
	c, fake := setup(t)
	fake.StorageFull.Store(true)
	data := []byte("data")
	err := c.Upload(context.Background(), "/x.bin", provider.Source{Size: 4, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }})
	if !errors.Is(err, provider.ErrInsufficientStorage) {
		t.Fatalf("ожидался ErrInsufficientStorage, получено %v", err)
	}
}

func TestUploadAcceptedAsync(t *testing.T) {
	c, fake := setup(t)
	fake.UploadStatus = 202
	data := []byte("async")
	if err := c.Upload(context.Background(), "/y.bin", provider.Source{Size: 5, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}); err != nil {
		t.Fatalf("Upload с ответом 202: %v", err)
	}
	if got, ok := fake.Mem.Get("/y.bin"); !ok || string(got) != "async" {
		t.Fatalf("файл не сохранён: %q", got)
	}
}

func TestListPaginates(t *testing.T) {
	c, fake := setup(t)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		fake.Mem.Put("/dir/"+n, []byte(n))
	}
	list, err := c.List(context.Background(), "/dir")
	if err != nil || len(list) != 5 {
		t.Fatalf("List по страницам вернул %d записей, %v", len(list), err)
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New("  ", ""); err == nil {
		t.Fatal("пустой токен должен быть отклонён")
	}
}

func TestUploadVerifiesMD5(t *testing.T) {
	c, fake := setup(t)
	fake.Mem.WithMD5 = true
	data := []byte("checksum body")
	if err := c.Upload(context.Background(), "/md5.bin", provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}); err != nil {
		t.Fatalf("Upload с проверкой MD5: %v", err)
	}
}

func TestUploadStoresSourceModTime(t *testing.T) {
	c, _ := setup(t)
	mod := time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.UTC)
	data := []byte("mod time")
	s := provider.Source{Size: int64(len(data)), ModTime: mod, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
	if err := c.Upload(context.Background(), "/mt.bin", s); err != nil {
		t.Fatal(err)
	}
	e, err := c.Stat(context.Background(), "/mt.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !e.ModTime.Equal(mod) {
		t.Fatalf("время %v, ожидалось %v", e.ModTime, mod)
	}
}

func TestTrashKeepsFileInTrash(t *testing.T) {
	c, fake := setup(t)
	ctx := context.Background()
	data := []byte("trash me")
	if err := c.Upload(ctx, "/tr.bin", provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}); err != nil {
		t.Fatal(err)
	}
	if err := c.Trash(ctx, "/tr.bin"); err != nil {
		t.Fatalf("Trash: %v", err)
	}
	if _, ok := fake.Mem.Get("/tr.bin"); ok {
		t.Fatal("файл остался на месте")
	}
	if got := fake.Trashed(); len(got) != 1 || got[0] != "/tr.bin" {
		t.Fatalf("в корзине %v, ожидался /tr.bin", got)
	}
}

func TestDeleteIsPermanent(t *testing.T) {
	c, fake := setup(t)
	ctx := context.Background()
	data := []byte("gone")
	if err := c.Upload(ctx, "/gone.bin", provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "/gone.bin"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Trashed(); len(got) != 0 {
		t.Fatalf("Delete попал в корзину: %v", got)
	}
}

func TestModTimeRetriesUntilStored(t *testing.T) {
	c, fake := setup(t)
	ctx := context.Background()
	mod := time.Date(2024, 5, 6, 7, 8, 9, 42, time.UTC)
	fake.DropPatches.Store(2) // первые две записи не сохраняются
	data := []byte("retry time")
	s := provider.Source{Size: int64(len(data)), ModTime: mod, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
	if err := c.Upload(ctx, "/rt.bin", s); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	e, err := c.Stat(ctx, "/rt.bin")
	if err != nil || !e.ModTime.Equal(mod) {
		t.Fatalf("время %v (%v), ожидалось %v", e.ModTime, err, mod)
	}
}

func TestModTimeFailsAfterThreeTries(t *testing.T) {
	c, fake := setup(t)
	ctx := context.Background()
	fake.DropPatches.Store(10)
	data := []byte("never stored")
	s := provider.Source{Size: int64(len(data)), ModTime: time.Now(), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
	if err := c.Upload(ctx, "/nt.bin", s); err == nil {
		t.Fatal("ожидалась ошибка: время не сохранилось за 3 попытки")
	}
}
