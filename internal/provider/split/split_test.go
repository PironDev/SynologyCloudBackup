package split

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"testing"

	"nascloud/internal/provider"
	"nascloud/internal/testutil"
)

func TestSplit(t *testing.T) {
	ctx := context.Background()
	m := testutil.NewMem()
	m.WithMD5 = true
	f := Wrap(m, 100)
	data := make([]byte, 450)
	rand.Read(data)
	src := provider.Source{Size: 450, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
	if how, err := f.UploadVerified(ctx, "/big.bin", src, provider.VerifyHash); err != nil || how != "md5" {
		t.Fatalf("%q %v", how, err)
	}
	if n := len(m.Files()); n != 5 {
		t.Fatalf("частей %d: %v", n, m.Files())
	}
	es, _ := f.List(ctx, "/")
	if len(es) != 1 || es[0].Name != "big.bin" || es[0].Size != 450 {
		t.Fatalf("list %+v", es)
	}
	e, err := f.Stat(ctx, "/big.bin")
	if err != nil || e.Size != 450 {
		t.Fatalf("stat %+v %v", e, err)
	}
	for _, r := range [][2]int64{{0, -1}, {95, 20}, {250, 1000}, {449, 5}, {100, 100}} {
		rc, err := f.Download(ctx, "/big.bin", r[0], r[1])
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		end := int64(450)
		if r[1] >= 0 && r[0]+r[1] < end {
			end = r[0] + r[1]
		}
		if !bytes.Equal(got, data[r[0]:end]) {
			t.Fatalf("range %v: %d vs %d", r, len(got), end-r[0])
		}
	}
	// перезапись более короткой версией удаляет лишние части
	small := data[:150]
	src2 := provider.Source{Size: 150, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(small)), nil }}
	if _, err := f.UploadVerified(ctx, "/big.bin", src2, provider.VerifyHash); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Files()); n != 2 {
		t.Fatalf("после уменьшения частей %d: %v", n, m.Files())
	}
	// обычный файл маленького размера и обратно большой
	src3 := provider.Source{Size: 50, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data[:50])), nil }}
	f.UploadVerified(ctx, "/s.bin", src3, provider.VerifyHash)
	if _, ok := m.Get("/s.bin"); !ok {
		t.Fatal("маленький файл должен лежать целиком")
	}
	if err := f.Move(ctx, "/big.bin", "/moved.bin", true); err != nil {
		t.Fatal(err)
	}
	if es, _ := f.List(ctx, "/"); len(es) != 2 {
		t.Fatalf("после move %+v", es)
	}
	if err := f.Delete(ctx, "/moved.bin"); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Files()); n != 1 {
		t.Fatalf("после delete файлов %d: %v", n, m.Files())
	}
}
