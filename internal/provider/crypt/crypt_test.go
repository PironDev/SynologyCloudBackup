package crypt

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"strings"
	"testing"

	"nascloud/internal/provider"
	"nascloud/internal/testutil"
)

func srcOf(b []byte) provider.Source {
	return provider.Source{Size: int64(len(b)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	m := testutil.NewMem()
	m.WithMD5 = true
	m.Mkdir(ctx, "/dir")
	f, err := Wrap(m, "pass")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 100, chunk - 1, chunk, chunk + 1, 3*chunk + 17} {
		data := make([]byte, n)
		rand.Read(data)
		name := "/dir/f.bin"
		if _, err := f.UploadVerified(ctx, name, srcOf(data), provider.VerifyHash); err != nil {
			t.Fatalf("n=%d upload: %v", n, err)
		}
		raw, ok := m.Get(name + Suffix)
		if !ok || int64(len(raw)) != EncSize(int64(n)) {
			t.Fatalf("n=%d: сырой размер %d, ожидалось %d", n, len(raw), EncSize(int64(n)))
		}
		if n > 100 && bytes.Contains(raw, data[:64]) {
			t.Fatal("открытый текст в облаке")
		}
		e, err := f.Stat(ctx, name)
		if err != nil || e.Size != int64(n) {
			t.Fatalf("n=%d stat: %v %+v", n, err, e)
		}
		rc, err := f.Download(ctx, name, 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("n=%d download: %v equal=%v", n, err, bytes.Equal(got, data))
		}
		if n > 10 {
			for _, r := range [][2]int64{{3, 5}, {int64(n) / 2, 7}, {int64(n) - 4, -1}} {
				rc, err := f.Download(ctx, name, r[0], r[1])
				if err != nil {
					t.Fatal(err)
				}
				got, _ := io.ReadAll(rc)
				rc.Close()
				end := int64(n)
				if r[1] >= 0 && r[0]+r[1] < end {
					end = r[0] + r[1]
				}
				if !bytes.Equal(got, data[r[0]:end]) {
					t.Fatalf("n=%d range %v: %d байт, ожидалось %d", n, r, len(got), end-r[0])
				}
			}
		}
	}
	// список скрывает суффикс
	es, _ := f.List(ctx, "/dir")
	if len(es) != 1 || es[0].Name != "f.bin" || strings.HasSuffix(es[0].Path, Suffix) {
		t.Fatalf("list: %+v", es)
	}
	// неверный пароль и порча
	bad, _ := Wrap(m, "other")
	rc, err := bad.Download(ctx, "/dir/f.bin", 0, -1)
	if err == nil {
		_, err = io.ReadAll(rc)
	}
	if err == nil {
		t.Fatal("неверный пароль не замечен")
	}
	raw, _ := m.Get("/dir/f.bin" + Suffix)
	raw2 := append([]byte(nil), raw...)
	raw2[len(raw2)-1] ^= 1
	m.Put("/dir/f.bin"+Suffix, raw2)
	rc, _ = f.Download(ctx, "/dir/f.bin", 0, -1)
	if _, err := io.ReadAll(rc); err == nil {
		t.Fatal("порча не замечена")
	}
	// move/delete работают по имени без суффикса
	if err := f.Move(ctx, "/dir/f.bin", "/dir/g.bin", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("/dir/g.bin" + Suffix); !ok {
		t.Fatal("move не переименовал")
	}
	if err := f.Delete(ctx, "/dir/g.bin"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("/dir/g.bin" + Suffix); ok {
		t.Fatal("delete не удалил")
	}
}
