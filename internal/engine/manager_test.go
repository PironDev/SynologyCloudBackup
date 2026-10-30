package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nascloud/internal/secret"
	"nascloud/internal/store"
	"nascloud/internal/testutil"
)

func newManager(t *testing.T) (*Manager, *testutil.Mem, string) {
	t.Helper()
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	mem := testutil.NewMem()
	desc := testutil.Descriptor(mem)
	st, err := store.Open(dir, box, desc.SecretKeys())
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = st.Update(func(c *store.Config) error {
		c.Accounts = []store.Account{{ID: "acc", Name: "A", Slug: "a", Fields: map[string]string{"token": "t"}}}
		c.Jobs = []store.Job{{ID: "j1", Name: "nightly", AccountID: "acc", Enabled: true, Mode: "mirror", Sources: []string{src}, RemoteDir: "/B", Schedule: "0 3 * * *"}}
		return nil
	})
	m, err := NewManager(st, NewPool(desc), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Shutdown(5 * time.Second) })
	return m, mem, dir
}

func waitDone(t *testing.T, m *Manager, jobID string) RunRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h := m.History(jobID, 1); len(h) == 1 && h[0].Status != StatusRunning {
			return h[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("запуск не завершился")
	return RunRecord{}
}

func TestSchedulerFiresOncePerMinute(t *testing.T) {
	m, mem, _ := newManager(t)
	at := time.Date(2026, 10, 31, 3, 0, 20, 0, time.Local)

	m.tick(time.Date(2026, 10, 31, 2, 59, 50, 0, time.Local)) // не время
	if len(m.History("", 0)) != 0 {
		t.Fatal("до 03:00 запуска быть не должно")
	}
	m.tick(at)
	m.tick(at.Add(15 * time.Second)) // тот же минутный слот: повторно не запускается
	rec := waitDone(t, m, "j1")
	if rec.Status != StatusSuccess || rec.Trigger != "schedule" || rec.Stats.Uploaded != 1 {
		t.Fatalf("запись: %+v", rec)
	}
	if n := len(m.History("j1", 0)); n != 1 {
		t.Fatalf("за минуту должен быть один запуск, было %d", n)
	}
	if got, _ := mem.Get("/B/" + filepath.Base(m.Store.Get().Jobs[0].Sources[0]) + "/f.txt"); string(got) != "data" {
		t.Fatalf("в облаке: %v", mem.Files())
	}
}

func TestDisabledJobIsNotScheduled(t *testing.T) {
	m, _, _ := newManager(t)
	_ = m.Store.Update(func(c *store.Config) error { c.Jobs[0].Enabled = false; return nil })
	m.tick(time.Date(2026, 10, 31, 3, 0, 5, 0, time.Local))
	time.Sleep(100 * time.Millisecond)
	if len(m.History("", 0)) != 0 {
		t.Fatal("выключенное задание не должно запускаться")
	}
	if _, ok := m.NextRun(m.Store.Get().Jobs[0]); ok {
		t.Fatal("у выключенного задания нет следующего запуска")
	}
}

func TestStartTwiceAndCancel(t *testing.T) {
	m, mem, _ := newManager(t)
	release := make(chan struct{})
	mem.FailUpload = func(p string, n int) error { <-release; return nil } // загрузка «зависает»
	if _, err := m.Start("j1", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("j1", "manual"); err != ErrRunning {
		t.Fatalf("повторный запуск работающего задания: %v", err)
	}
	if !m.Cancel("j1") {
		t.Fatal("Cancel должен найти работающее задание")
	}
	close(release)
	if rec := waitDone(t, m, "j1"); rec.Status != StatusCancelled {
		t.Fatalf("статус после отмены: %+v", rec)
	}
	if m.Cancel("j1") {
		t.Fatal("отменять уже нечего")
	}
}

func TestStaleRunningRecordsAreMarkedFailed(t *testing.T) {
	m, _, dir := newManager(t)
	m.writeRecord(RunRecord{ID: "20260101T000000-dead", JobID: "j1", JobName: "nightly", Started: time.Now(), Status: StatusRunning})
	m2, err := NewManager(m.Store, m.Pool, dir)
	if err != nil {
		t.Fatal(err)
	}
	h := m2.History("j1", 0)
	if len(h) != 1 || h[0].Status != StatusFailed || !strings.Contains(h[0].Message, "перезапуск") {
		t.Fatalf("зависшая запись: %+v", h)
	}
}

func TestReadLogOffsetsAndValidation(t *testing.T) {
	m, _, _ := newManager(t)
	if _, err := m.Start("j1", "manual"); err != nil {
		t.Fatal(err)
	}
	rec := waitDone(t, m, "j1")
	text, next, err := m.ReadLog(rec.ID, 0)
	if err != nil || !strings.Contains(text, "запуск «nightly»") || next == 0 {
		t.Fatalf("ReadLog: %q %d %v", text, next, err)
	}
	if more, n2, _ := m.ReadLog(rec.ID, next); more != "" || n2 != next {
		t.Fatalf("повторное чтение с конца должно быть пустым: %q", more)
	}
	if _, _, err := m.ReadLog("../../etc/passwd", 0); err == nil {
		t.Fatal("идентификатор с путём должен отклоняться")
	}
}

func TestHistoryPruned(t *testing.T) {
	m, _, _ := newManager(t)
	for i := 0; i < keepRuns+5; i++ {
		id := time.Unix(1_700_000_000+int64(i), 0).UTC().Format("20060102T150405") + "-abcd"
		m.writeRecord(RunRecord{ID: id, JobID: "j1", JobName: "x", Started: time.Unix(1_700_000_000+int64(i), 0), Status: StatusSuccess})
	}
	m.prune("j1")
	if n := len(m.History("j1", 0)); n != keepRuns {
		t.Fatalf("после очистки осталось %d, ожидалось %d", n, keepRuns)
	}
}
