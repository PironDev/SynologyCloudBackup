package engine

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"nascloud/internal/provider"
	"nascloud/internal/provider/crypt"
	"nascloud/internal/provider/split"
	"nascloud/internal/testutil"
)

// Зеркало поверх «шифрование + нарезка»: в облаке лежат зашифрованные части,
// повторный запуск всё пропускает, содержимое расшифровывается.
func TestMirrorEncryptedAndSplit(t *testing.T) {
	src := filepath.Join(t.TempDir(), "docs")
	big := []byte(strings.Repeat("0123456789", 40)) // 400 байт
	write(t, filepath.Join(src, "big.txt"), big)
	write(t, filepath.Join(src, "small.txt"), []byte("hi"))

	mem := testutil.NewMem()
	mem.WithMD5 = true
	enc, err := crypt.Wrap(split.Wrap(mem, 128), "secret-pass")
	if err != nil {
		t.Fatal(err)
	}
	env := newEnv(t, enc)
	env.Verify = provider.VerifyHash
	job := mirrorJob(src)

	st, err := RunMirror(context.Background(), env, job)
	if err != nil || st.Errors != 0 || st.Uploaded != 2 {
		t.Fatalf("запуск: %+v, %v", st, err)
	}
	for _, f := range mem.Files() {
		if !strings.Contains(f, ".cbenc") {
			t.Fatalf("незашифрованный файл в облаке: %s", f)
		}
	}
	if n := len(mem.Files()); n < 4 {
		t.Fatalf("ожидались части большого файла, файлов %d: %v", n, mem.Files())
	}
	up := mem.Uploads()
	st, err = RunMirror(context.Background(), env, job)
	if err != nil || st.Uploaded != 0 || mem.Uploads() != up {
		t.Fatalf("повторный запуск должен пропустить всё: %+v, %v (загрузок %d → %d)", st, err, up, mem.Uploads())
	}
	rc, err := enc.Download(context.Background(), "/Backups/docs/big.txt", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("расшифровано %d байт, err=%v, совпадает=%v", len(got), err, bytes.Equal(got, big))
	}
}
