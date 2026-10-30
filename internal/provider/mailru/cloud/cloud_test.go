package cloud

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"nascloud/internal/provider"
	"nascloud/internal/testutil"
)

func src(data []byte) provider.Source {
	return provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
}

func TestConformance(t *testing.T) {
	c, _ := newFake(t)
	testutil.Conformance(t, c)
}

func TestWrongPasswordIsAuthError(t *testing.T) {
	c, _ := newFake(t)
	bad, _ := New(fakeLogin, "wrong", "test/1")
	bad.apiBase, bad.dispatchBase, bad.oauthURL = c.apiBase, c.dispatchBase, c.oauthURL
	if _, err := bad.Check(context.Background()); !errors.Is(err, provider.ErrAuth) {
		t.Fatalf("ожидался ErrAuth, получено %v", err)
	}
}

func TestReloginAfterTokenRevoked(t *testing.T) {
	c, f := newFake(t)
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.revoke()
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatalf("после отзыва токена ожидался повторный вход: %v", err)
	}
}

func TestUploadDetectsCorruptShard(t *testing.T) {
	c, f := newFake(t)
	f.corrupt = true
	err := c.Upload(context.Background(), "/bad.bin", src([]byte("payload")))
	if err == nil {
		t.Fatal("ожидалась ошибка при неверном хеше шарда")
	}
	if _, ok := f.Mem.Get("/bad.bin"); ok {
		t.Fatal("повреждённый файл не должен попасть в облако")
	}
}

func TestUploadMissingParentIsNotFound(t *testing.T) {
	c, _ := newFake(t)
	err := c.Upload(context.Background(), "/no/such/dir/x.bin", src([]byte("x")))
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("ожидался ErrNotFound, получено %v", err)
	}
}

func TestNameEncodingRoundTrip(t *testing.T) {
	name := `a:b*c?"d"<e>|f\g.txt`
	enc := encodeName(name)
	if enc == name {
		t.Fatalf("запрещённые символы не заменены: %q", enc)
	}
	if got := decodeName(enc); got != name {
		t.Fatalf("декодирование: %q, ожидалось %q", got, name)
	}
}

func TestSpeedupSkipsTransfer(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("speedup"), 200)
	if err := provider.MkdirAll(ctx, c, "/s"); err != nil {
		t.Fatal(err)
	}
	if c.speedupEligible("/s/x.bin", 1000) {
		t.Fatal("файл .bin не должен проходить по шаблону по умолчанию")
	}
	for _, name := range []string{"/s/a.mkv", "/s/b.mkv"} {
		if err := c.Upload(ctx, name, src(data)); err != nil {
			t.Fatalf("Upload %s: %v", name, err)
		}
	}
	if f.shardPuts != 1 {
		t.Fatalf("шард принял %d блоков, ожидался 1: второй файл должен пройти по хешу", f.shardPuts)
	}
	if got, ok := f.Mem.Get("/s/b.mkv"); !ok || !bytes.Equal(got, data) {
		t.Fatal("файл, загруженный по хешу, отличается от источника")
	}
}

func TestDownloadDetectsCorruption(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	if err := c.Upload(ctx, "/c.bin", src(bytes.Repeat([]byte("x"), 600))); err != nil {
		t.Fatal(err)
	}
	f.corruptDownload = true
	rc, err := c.Download(ctx, "/c.bin", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("ожидалась ошибка контрольной суммы при испорченном скачивании")
	}
}

func TestPublishReturnsLink(t *testing.T) {
	c, _ := newFake(t)
	ctx := context.Background()
	if err := c.Upload(ctx, "/p.bin", src([]byte("public"))); err != nil {
		t.Fatal(err)
	}
	link, err := c.Publish(ctx, "/p.bin")
	if err != nil {
		t.Fatal(err)
	}
	if want := PublicLinkURL + "abc123"; link != want {
		t.Fatalf("ссылка %q, ожидалась %q", link, want)
	}
}

func TestUploadKeepsSourceModTime(t *testing.T) {
	c, _ := newFake(t)
	ctx := context.Background()
	mod := time.Unix(1700000000, 0)
	s := src([]byte("with time"))
	s.ModTime = mod
	if err := c.Upload(ctx, "/t.bin", s); err != nil {
		t.Fatal(err)
	}
	e, err := c.Stat(ctx, "/t.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !e.ModTime.Equal(mod) {
		t.Fatalf("время %v, ожидалось %v", e.ModTime, mod)
	}
}

func TestDownloadResumesAfterDrop(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("resume"), 5000)
	f.Mem.Put("/r.bin", data)
	f.dropDownloads = 1
	rc, err := c.Download(ctx, "/r.bin", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("после обрыва: err=%v, совпало=%v", err, bytes.Equal(got, data))
	}
}

func TestMoveOverwriteRestoresOldFileOnFailure(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	if err := provider.MkdirAll(ctx, c, "/t"); err != nil {
		t.Fatal(err)
	}
	f.Mem.Put("/t/dst.txt", []byte("old"))
	// источника нет: перенос падает после того, как старый файл отодвинут
	if err := c.Move(ctx, "/t/missing.txt", "/t/dst.txt", true); err == nil {
		t.Fatal("ожидалась ошибка переноса без источника")
	}
	if got, ok := f.Mem.Get("/t/dst.txt"); !ok || string(got) != "old" {
		t.Fatalf("старый файл потерян: %q, %v", got, ok)
	}
}

func TestMoveOverwriteReplacesAndCleansUp(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	if err := provider.MkdirAll(ctx, c, "/t"); err != nil {
		t.Fatal(err)
	}
	f.Mem.Put("/t/dst.txt", []byte("old"))
	f.Mem.Put("/t/src.txt", []byte("new"))
	if err := c.Move(ctx, "/t/src.txt", "/t/dst.txt", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Mem.Get("/t/dst.txt"); string(got) != "new" {
		t.Fatalf("файл не заменён: %q", got)
	}
	list, _ := f.Mem.List(ctx, "/t")
	if len(list) != 1 {
		t.Fatalf("в папке остались лишние файлы: %d", len(list))
	}
}

func TestUnknownKindIsErrorUnlessQuirk(t *testing.T) {
	c, f := newFake(t)
	ctx := context.Background()
	if err := provider.MkdirAll(ctx, c, "/q"); err != nil {
		t.Fatal(err)
	}
	f.Mem.Put("/q/link", []byte("x"))
	f.kinds = map[string]string{"/q/link": "symlink"}
	if _, err := c.List(ctx, "/q"); err == nil {
		t.Fatal("неизвестный тип должен быть ошибкой без quirk unknowndirs")
	}
	if err := c.SetQuirks("unknowndirs"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(ctx, "/q"); err != nil {
		t.Fatalf("с quirk unknowndirs список должен открыться: %v", err)
	}
}

func TestAtomicMkdirQuirk(t *testing.T) {
	c, _ := newFake(t)
	ctx := context.Background()
	if err := c.Mkdir(ctx, "/m"); err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir(ctx, "/m"); err != nil {
		t.Fatalf("без quirk повторный Mkdir не ошибка: %v", err)
	}
	if err := c.SetQuirks("atomicmkdir"); err != nil {
		t.Fatal(err)
	}
	if err := c.Mkdir(ctx, "/m"); !errors.Is(err, provider.ErrExists) {
		t.Fatalf("с atomicmkdir ожидался ErrExists, получено %v", err)
	}
}

func TestBinlistQuirkRejected(t *testing.T) {
	c, _ := newFake(t)
	if err := c.SetQuirks("binlist"); err == nil {
		t.Fatal("binlist должен отклоняться")
	}
}
