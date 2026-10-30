package provider_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"nascloud/internal/provider"
	"nascloud/internal/testutil"
)

// corrupting портит байты при первой загрузке (имитация порчи при передаче).
type corrupting struct {
	*testutil.Mem
	bad int
}

func (c *corrupting) Upload(ctx context.Context, p string, src provider.Source) error {
	if c.bad > 0 {
		c.bad--
		rc, err := src.Open()
		if err != nil {
			return err
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		b[0] ^= 0xff
		return c.Mem.Upload(ctx, p, provider.Source{Size: src.Size, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }})
	}
	return c.Mem.Upload(ctx, p, src)
}

func srcOf(b []byte) provider.Source {
	return provider.Source{Size: int64(len(b)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }}
}

func TestUploadVerified(t *testing.T) {
	ctx := context.Background()
	data := []byte("hello, verified world")

	m := testutil.NewMem()
	m.WithMD5 = true
	if how, err := provider.UploadVerified(ctx, m, "/a.txt", srcOf(data), provider.VerifyHash); err != nil || how != "md5" {
		t.Fatalf("how=%q err=%v", how, err)
	}
	// порча один раз — должна быть повторная загрузка и успех
	c := &corrupting{Mem: m, bad: 1}
	if how, err := provider.UploadVerified(ctx, c, "/b.txt", srcOf(data), provider.VerifyHash); err != nil || how != "md5" {
		t.Fatalf("после повтора how=%q err=%v", how, err)
	}
	if got, _ := m.Get("/b.txt"); !bytes.Equal(got, data) {
		t.Fatal("на диске осталась испорченная копия")
	}
	// порча всегда — ошибка
	c = &corrupting{Mem: m, bad: 99}
	if _, err := provider.UploadVerified(ctx, c, "/c.txt", srcOf(data), provider.VerifyHash); err == nil || !strings.Contains(err.Error(), "контрольная сумма") {
		t.Fatalf("ожидалась ошибка суммы, получено %v", err)
	}

	// облако без хешей: hash → размер, full → скачивание
	n := testutil.NewMem()
	if how, _ := provider.UploadVerified(ctx, n, "/d.txt", srcOf(data), provider.VerifyHash); how != "размер" {
		t.Fatalf("how=%q", how)
	}
	if how, err := provider.UploadVerified(ctx, n, "/e.txt", srcOf(data), provider.VerifyFull); err != nil || how != "md5+скачивание" {
		t.Fatalf("how=%q err=%v", how, err)
	}
	cn := &corrupting{Mem: n, bad: 99}
	if _, err := provider.UploadVerified(ctx, cn, "/f.txt", srcOf(data), provider.VerifyFull); err == nil {
		t.Fatal("полная сверка должна поймать порчу")
	}
	// пустой файл
	if how, err := provider.UploadVerified(ctx, m, "/empty", srcOf(nil), provider.VerifyHash); err != nil || how != "md5" {
		t.Fatalf("пустой: how=%q err=%v", how, err)
	}
}
