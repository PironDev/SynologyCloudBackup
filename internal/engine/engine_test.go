package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nascloud/internal/provider"
	"nascloud/internal/store"
	"nascloud/internal/testutil"
)

func write(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newEnv(t *testing.T, m provider.Provider) Env {
	t.Helper()
	d := t.TempDir()
	return Env{Provider: m, StateDir: filepath.Join(d, "state"), TmpDir: filepath.Join(d, "tmp")}
}

func mirrorJob(src string) store.Job {
	return store.Job{ID: "job1", Name: "photos", Mode: "mirror", Sources: []string{src}, RemoteDir: "/Backups", Concurrency: 3, Excludes: DefaultExcludes}
}

func TestMirrorUploadsThenSkipsThenDetectsChanges(t *testing.T) {
	src := filepath.Join(t.TempDir(), "photo")
	write(t, filepath.Join(src, "a.txt"), []byte("aaa"))
	write(t, filepath.Join(src, "sub", "b.txt"), []byte("bbbb"))
	write(t, filepath.Join(src, "sub", "deep", "c.txt"), []byte("c"))
	write(t, filepath.Join(src, "@eaDir", "thumb.jpg"), []byte("junk"))
	write(t, filepath.Join(src, ".DS_Store"), []byte("junk"))
	if err := os.Symlink(filepath.Join(src, "a.txt"), filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	mem := testutil.NewMem()
	env := newEnv(t, mem)
	job := mirrorJob(src)

	st, err := RunMirror(context.Background(), env, job)
	if err != nil || st.Errors != 0 || st.Uploaded != 3 || st.Files != 3 {
		t.Fatalf("первый запуск: %+v, %v", st, err)
	}
	want := []string{"/Backups/photo/a.txt", "/Backups/photo/sub/b.txt", "/Backups/photo/sub/deep/c.txt"}
	if got := mem.Files(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("в облаке %v, ожидалось %v (исключения и симлинки не должны попадать)", got, want)
	}

	uploads := mem.Uploads()
	st, err = RunMirror(context.Background(), env, job)
	if err != nil || st.Uploaded != 0 || st.Skipped != 3 || mem.Uploads() != uploads {
		t.Fatalf("повторный запуск должен всё пропустить: %+v, %v", st, err)
	}

	// без манифеста срабатывает правило «ModTime на диске не старше локального»
	_ = os.RemoveAll(env.StateDir)
	st, _ = RunMirror(context.Background(), env, job)
	if st.Uploaded != 0 || st.Skipped != 3 {
		t.Fatalf("без манифеста: %+v", st)
	}

	// изменённый файл выгружается заново
	time.Sleep(20 * time.Millisecond)
	write(t, filepath.Join(src, "sub", "b.txt"), []byte("BBBB-changed"))
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(filepath.Join(src, "sub", "b.txt"), future, future)
	st, err = RunMirror(context.Background(), env, job)
	if err != nil || st.Uploaded != 1 || st.Skipped != 2 {
		t.Fatalf("после изменения: %+v, %v", st, err)
	}
	if got, _ := mem.Get("/Backups/photo/sub/b.txt"); string(got) != "BBBB-changed" {
		t.Fatalf("новое содержимое не выгружено: %q", got)
	}
}

func TestMirrorUsesMD5WhenAvailable(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "f.bin"), []byte("original!"))
	mem := testutil.NewMem()
	mem.WithMD5 = true
	env := newEnv(t, mem)
	job := mirrorJob(src)
	job.Sources = []string{filepath.Join(src, "f.bin")} // источник — одиночный файл
	if st, err := RunMirror(context.Background(), env, job); err != nil || st.Uploaded != 1 {
		t.Fatalf("первый запуск: %+v, %v", st, err)
	}
	// тот же размер, другое содержимое, mtime в прошлом; манифест удалён
	_ = os.RemoveAll(env.StateDir)
	write(t, filepath.Join(src, "f.bin"), []byte("different"))
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(src, "f.bin"), past, past)
	st, err := RunMirror(context.Background(), env, job)
	if err != nil || st.Uploaded != 1 {
		t.Fatalf("MD5 должен обнаружить различие: %+v, %v", st, err)
	}
	if got, _ := mem.Get("/Backups/f.bin"); string(got) != "different" {
		t.Fatalf("в облаке %q", got)
	}
	// одинаковое содержимое при удалённом манифесте — пропуск по MD5
	_ = os.RemoveAll(env.StateDir)
	if st, _ := RunMirror(context.Background(), env, job); st.Uploaded != 0 || st.Skipped != 1 {
		t.Fatalf("совпадающий MD5: %+v", st)
	}
}

func TestMirrorFileErrorIsPartialNotFatal(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "ok1.txt"), []byte("1"))
	write(t, filepath.Join(src, "bad.txt"), []byte("2"))
	write(t, filepath.Join(src, "ok2.txt"), []byte("3"))
	mem := testutil.NewMem()
	mem.FailUpload = func(p string, n int) error {
		if strings.HasSuffix(p, "bad.txt") {
			return errors.New("сбой диска")
		}
		return nil
	}
	st, err := RunMirror(context.Background(), newEnv(t, mem), mirrorJob(src))
	if err != nil || st.Errors != 1 || st.Uploaded != 2 {
		t.Fatalf("%+v, %v", st, err)
	}
}

func TestMirrorStopsOnInsufficientStorage(t *testing.T) {
	src := t.TempDir()
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(src, fmt.Sprintf("f%02d.txt", i)), []byte("x"))
	}
	mem := testutil.NewMem()
	var calls atomic.Int32
	mem.FailUpload = func(p string, n int) error {
		if calls.Add(1) >= 2 {
			return provider.ErrInsufficientStorage
		}
		return nil
	}
	job := mirrorJob(src)
	job.Concurrency = 1
	_, err := RunMirror(context.Background(), newEnv(t, mem), job)
	if !errors.Is(err, provider.ErrInsufficientStorage) {
		t.Fatalf("ожидалась остановка с ErrInsufficientStorage, получено %v", err)
	}
	if calls.Load() > 4 {
		t.Fatalf("после нехватки места загрузка должна прекратиться, было %d попыток", calls.Load())
	}
}

func TestMirrorCancel(t *testing.T) {
	src := t.TempDir()
	for i := 0; i < 10; i++ {
		write(t, filepath.Join(src, fmt.Sprintf("f%d.txt", i)), []byte("x"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunMirror(ctx, newEnv(t, testutil.NewMem()), mirrorJob(src))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ожидалась context.Canceled, получено %v", err)
	}
}

// readArchive склеивает части по порядку имён и разворачивает tar.gz.
func readArchive(t *testing.T, mem *testutil.Mem, dir, prefix string) map[string]string {
	t.Helper()
	var names []string
	for _, p := range mem.Files() {
		if strings.HasPrefix(p, dir+"/"+prefix) {
			names = append(names, p)
		}
	}
	sort.Strings(names)
	var joined bytes.Buffer
	for _, n := range names {
		b, _ := mem.Get(n)
		joined.Write(b)
	}
	gz, err := gzip.NewReader(&joined)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			out[h.Name] = string(b)
		}
	}
	return out
}

func TestArchiveSplitsIntoPartsAndRestores(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	rnd := rand.New(rand.NewSource(7))
	files := map[string][]byte{}
	for _, n := range []string{"one.bin", "two.bin", "dir/three.bin"} {
		b := make([]byte, 700_000) // несжимаемые данные → архив ≈ 2 МБ → 2–3 части по 1 МБ
		rnd.Read(b)
		files["data/"+n] = b
		write(t, filepath.Join(src, n), b)
	}
	write(t, filepath.Join(src, "@eaDir", "x"), []byte("excluded"))

	mem := testutil.NewMem()
	env := newEnv(t, mem)
	job := store.Job{ID: "j", Name: "Мой архив", Mode: "archive", Sources: []string{src}, RemoteDir: "/B", Excludes: DefaultExcludes, Archive: store.ArchiveOpts{PartSizeMB: 1}}
	st, err := RunArchive(context.Background(), env, job)
	if err != nil || st.Errors != 0 || st.Files != 3 || st.Parts < 2 {
		t.Fatalf("%+v, %v", st, err)
	}
	var parts []string
	for _, p := range mem.Files() {
		parts = append(parts, filepath.Base(p))
	}
	if len(parts) != st.Parts || !strings.HasSuffix(parts[0], ".tar.gz.001") || !strings.HasPrefix(parts[0], "Мой_архив-") {
		t.Fatalf("имена частей: %v", parts)
	}
	got := readArchive(t, mem, "/B/Мой_архив", "Мой_архив-")
	if len(got) != 3 {
		t.Fatalf("в архиве %d файлов, ожидалось 3: %v", len(got), got)
	}
	for name, want := range files {
		if got[name] != string(want) {
			t.Fatalf("содержимое %s не совпало", name)
		}
	}
	if entries, _ := os.ReadDir(env.TmpDir); len(entries) != 0 {
		t.Fatalf("временные файлы не удалены: %v", entries)
	}
}

func TestArchiveSingleFileNameAndRetention(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "f.txt"), []byte("hello"))
	mem := testutil.NewMem()
	env := newEnv(t, mem)
	job := store.Job{ID: "j", Name: "daily", Mode: "archive", Sources: []string{src}, RemoteDir: "/B", Archive: store.ArchiveOpts{Keep: 2}}

	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local)
	defer func(f func() time.Time) { nowFunc = f }(nowFunc)
	for day := 0; day < 4; day++ {
		d := base.AddDate(0, 0, day)
		nowFunc = func() time.Time { return d }
		if _, err := RunArchive(context.Background(), env, job); err != nil {
			t.Fatal(err)
		}
	}
	// посторонний файл, похожий на архив, но не нашего шаблона, ротация не трогает
	mem.Put("/B/daily/daily-notes.tar.gz", []byte("keep me"))
	mem.Put("/B/daily/readme.txt", []byte("keep me"))
	nowFunc = func() time.Time { return base.AddDate(0, 0, 4) }
	if _, err := RunArchive(context.Background(), env, job); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range mem.Files() {
		names = append(names, filepath.Base(p))
	}
	sort.Strings(names)
	want := []string{"daily-2026-10-04_030000.tar.gz", "daily-2026-10-05_030000.tar.gz", "daily-notes.tar.gz", "readme.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("после ротации %v, ожидалось %v", names, want)
	}
}

func TestArchiveFailureCleansUpUploadedParts(t *testing.T) {
	src := t.TempDir()
	b := make([]byte, 2_500_000)
	rand.New(rand.NewSource(3)).Read(b)
	write(t, filepath.Join(src, "big.bin"), b)
	mem := testutil.NewMem()
	mem.FailUpload = func(p string, n int) error {
		if n == 2 {
			return errors.New("обрыв сети")
		}
		return nil
	}
	job := store.Job{ID: "j", Name: "x", Mode: "archive", Sources: []string{src}, RemoteDir: "/B", Archive: store.ArchiveOpts{PartSizeMB: 1}}
	env := newEnv(t, mem)
	_, err := RunArchive(context.Background(), env, job)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if files := mem.Files(); len(files) != 0 {
		t.Fatalf("неполный снимок должен быть удалён, остались: %v", files)
	}
	if entries, _ := os.ReadDir(env.TmpDir); len(entries) != 0 {
		t.Fatalf("временные файлы не удалены: %v", entries)
	}
}

func TestArchiveEmptySourceFails(t *testing.T) {
	mem := testutil.NewMem()
	job := store.Job{ID: "j", Name: "x", Mode: "archive", Sources: []string{t.TempDir()}, RemoteDir: "/B"}
	if _, err := RunArchive(context.Background(), newEnv(t, mem), job); err == nil || len(mem.Files()) != 0 {
		t.Fatalf("пустой источник: err=%v files=%v", err, mem.Files())
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"Мой архив": "Мой_архив", "a/b\\c": "a_b_c", "  ": "backup", "photo-2026": "photo-2026"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestExcluded(t *testing.T) {
	pats := []string{"@eaDir", "*.tmp", "cache/*"}
	for _, c := range []struct {
		name, rel string
		want      bool
	}{{"@eaDir", "x/@eaDir", true}, {"a.tmp", "x/a.tmp", true}, {"b.txt", "cache/b.txt", true}, {"b.txt", "other/b.txt", false}} {
		if got := excluded(pats, c.name, c.rel); got != c.want {
			t.Errorf("excluded(%q,%q) = %v", c.name, c.rel, got)
		}
	}
}

func TestArchiveWithoutCompressionWritesPlainTar(t *testing.T) {
	src := t.TempDir()
	write(t, filepath.Join(src, "f.txt"), []byte("plain"))
	mem := testutil.NewMem()
	env := newEnv(t, mem)
	job := store.Job{ID: "j", Name: "raw", Mode: "archive", Sources: []string{src}, RemoteDir: "/R", Archive: store.ArchiveOpts{NoCompress: true}}
	if _, err := RunArchive(context.Background(), env, job); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range mem.Files() {
		if strings.HasPrefix(p, "/R/raw/") {
			names = append(names, p)
		}
	}
	if len(names) != 1 || !strings.HasSuffix(names[0], ".tar") {
		t.Fatalf("ожидался один файл .tar, получено %v", names)
	}
	b, _ := mem.Get(names[0])
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			t.Fatal("в tar нет f.txt")
		}
		if err != nil {
			t.Fatalf("tar без gzip не читается: %v", err)
		}
		if strings.HasSuffix(h.Name, "f.txt") {
			break
		}
	}
}
