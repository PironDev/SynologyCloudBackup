package local

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nascloud/internal/provider"
)

func TestRootsAndEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "share")
	outside := filepath.Join(base, "secret")
	_ = os.MkdirAll(root, 0o755)
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "pwd.txt"), []byte("x"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlink:", err)
	}
	f := New([]string{root})
	ctx := context.Background()

	if ents, err := f.List(ctx, "/"); err != nil || len(ents) != 1 || ents[0].Name != "share" {
		t.Fatalf("корни: %v %v", ents, err)
	}
	// ссылка наружу не должна открываться
	if _, err := f.List(ctx, "/share/link"); err == nil {
		t.Fatal("символическая ссылка вывела за корень")
	}
	if _, err := f.Stat(ctx, "/share/link/pwd.txt"); err == nil {
		t.Fatal("чтение через ссылку")
	}
	// «..» нормализуется внутрь корня
	if _, err := f.Stat(ctx, "/share/../../etc/passwd"); err == nil || !strings.Contains(err.Error(), "не найдено") {
		t.Fatalf("ожидалось «не найдено», получили %v", err)
	}
	// корень удалять нельзя
	if err := f.Delete(ctx, "/share"); err == nil {
		t.Fatal("корень удалён")
	}
	// запись, перемещение, копирование
	if err := f.Mkdir(ctx, "/share/a"); err != nil {
		t.Fatal(err)
	}
	src := func(data string) provider.Source {
		return provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(data)), nil }}
	}
	if err := f.Upload(ctx, "/share/a/x.txt", src("hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.Copy(ctx, "/share/a", "/share/b", false); err != nil {
		t.Fatal(err)
	}
	if err := f.Copy(ctx, "/share/a", "/share/b", false); err == nil {
		t.Fatal("копирование поверх без overwrite")
	}
	if err := f.Move(ctx, "/share/b", "/share/c", false); err != nil {
		t.Fatal(err)
	}
	rc, err := f.Download(ctx, "/share/c/x.txt", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "ell" {
		t.Fatalf("диапазон: %q", b)
	}
	if err := f.Move(ctx, "/share/a", "/share/a/inner", false); err == nil {
		t.Fatal("папку нельзя переместить в себя")
	}
	// временные файлы не остаются
	ents, _ := os.ReadDir(filepath.Join(root, "a"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatal("остался временный файл")
		}
	}
}
