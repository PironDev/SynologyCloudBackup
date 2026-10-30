package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"nascloud/internal/telemetry"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"nascloud/internal/cron"
	"nascloud/internal/provider"
	"nascloud/internal/store"
)

// Статусы запуска.
const (
	StatusRunning   = "running"
	StatusSuccess   = "success"
	StatusPartial   = "partial"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

const keepRuns = 50

// ErrRunning — задание уже выполняется.
var ErrRunning = errors.New("задание уже выполняется")

// RunRecord — запись о запуске (runs/<id>.json).
type RunRecord struct {
	ID       string     `json:"id"`
	JobID    string     `json:"jobId"`
	JobName  string     `json:"jobName"`
	Trigger  string     `json:"trigger"` // manual | schedule
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Status   string     `json:"status"`
	Message  string     `json:"message,omitempty"`
	Stats    Stats      `json:"stats"`
}

type activeRun struct {
	rec    RunRecord
	cancel context.CancelFunc
}

// Manager запускает задания, хранит историю и планирует запуски.
type Manager struct {
	Store   *store.Store
	Pool    *Pool
	DataDir string

	mu     sync.Mutex
	active map[string]*activeRun
	fired  map[string]time.Time

	wg      sync.WaitGroup
	rootCtx context.Context
	stopAll context.CancelFunc
}

// NewManager создаёт менеджер и помечает «зависшие» запуски прошлой сессии.
func NewManager(st *store.Store, pool *Pool, dataDir string) (*Manager, error) {
	for _, d := range []string{"runs", "state"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o700); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{Store: st, Pool: pool, DataDir: dataDir, active: map[string]*activeRun{}, fired: map[string]time.Time{}, rootCtx: ctx, stopAll: cancel}
	for _, r := range m.readRecords() {
		if r.Status == StatusRunning {
			now := time.Now()
			r.Status, r.Finished, r.Message = StatusFailed, &now, "прервано перезапуском пакета"
			m.writeRecord(r)
		}
	}
	return m, nil
}

// TmpDir — каталог временных частей архивов.
func (m *Manager) TmpDir() string {
	if d := m.Store.Get().Settings.TmpDir; d != "" {
		return d
	}
	return filepath.Join(m.DataDir, "tmp")
}

func (m *Manager) runPath(id, ext string) string {
	return filepath.Join(m.DataDir, "runs", id+ext)
}

func (m *Manager) writeRecord(r RunRecord) {
	raw, _ := json.Marshal(r)
	tmp := m.runPath(r.ID, ".json.tmp")
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, m.runPath(r.ID, ".json"))
	}
}

func (m *Manager) readRecords() []RunRecord {
	files, _ := filepath.Glob(filepath.Join(m.DataDir, "runs", "*.json"))
	var out []RunRecord
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r RunRecord
		if json.Unmarshal(raw, &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// Start запускает задание в фоне.
func (m *Manager) Start(jobID, trigger string) (RunRecord, error) {
	cfg := m.Store.Get()
	job, ok := cfg.Job(jobID)
	if !ok {
		return RunRecord{}, errors.New("задание не найдено")
	}
	acc, ok := cfg.Account(job.AccountID)
	if !ok {
		return RunRecord{}, errors.New("аккаунт задания не найден")
	}
	prov, err := m.Pool.Get(acc)
	if err != nil {
		return RunRecord{}, fmt.Errorf("аккаунт %q: %w", acc.Name, err)
	}
	m.mu.Lock()
	if _, busy := m.active[jobID]; busy {
		m.mu.Unlock()
		return RunRecord{}, ErrRunning
	}
	now := time.Now()
	rec := RunRecord{ID: now.UTC().Format("20060102T150405") + "-" + store.NewID()[:4], JobID: job.ID, JobName: job.Name, Trigger: trigger, Started: now, Status: StatusRunning}
	rl, err := NewRunLog(m.runPath(rec.ID, ".log"))
	if err != nil {
		m.mu.Unlock()
		return RunRecord{}, err
	}
	ctx, cancel := context.WithCancel(m.rootCtx)
	ar := &activeRun{rec: rec, cancel: cancel}
	m.active[jobID] = ar
	m.mu.Unlock()

	m.writeRecord(rec)
	m.wg.Add(1)
	go m.execute(ctx, ar, job, acc.Name, prov, rl)
	return rec, nil
}

func (m *Manager) execute(ctx context.Context, ar *activeRun, job store.Job, accName string, prov provider.Provider, rl *RunLog) {
	defer m.wg.Done()
	defer rl.Close()
	rec := ar.rec
	var stats Stats
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("внутренняя ошибка: %v", p)
				log.Printf("panic в задании %s: %v", job.Name, p)
				telemetry.Capture("panic", fmt.Sprintf("panic в задании: %v | %s", p, telemetry.Stack()), map[string]string{"mode": job.Mode})
			}
		}()
		env := Env{Provider: prov, Log: rl, StateDir: filepath.Join(m.DataDir, "state"), TmpDir: m.TmpDir(), Verify: job.Verify}
		rl.Infof("запуск «%s» (%s, аккаунт «%s»)", job.Name, job.Mode, accName)
		if job.Mode == "archive" {
			stats, err = RunArchive(ctx, env, job)
		} else {
			stats, err = RunMirror(ctx, env, job)
		}
	}()

	fin := time.Now()
	rec.Finished, rec.Stats = &fin, stats
	switch {
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		rec.Status, rec.Message = StatusCancelled, "остановлено"
	case err != nil:
		rec.Status, rec.Message = StatusFailed, err.Error()
		telemetry.Capture("job", "задание не выполнено: "+err.Error(), map[string]string{"mode": job.Mode, "trigger": rec.Trigger})
	case stats.Errors > 0:
		rec.Status, rec.Message = StatusPartial, fmt.Sprintf("не выгружено файлов: %d (см. журнал)", stats.Errors)
		telemetry.Capture("job", "часть файлов не выгружена", map[string]string{"mode": job.Mode, "trigger": rec.Trigger, "errors": fmt.Sprint(stats.Errors)})
	default:
		rec.Status = StatusSuccess
	}
	rl.Infof("итог: %s — файлов %d, выгружено %d, пропущено %d, ошибок %d, передано %s, %s",
		rec.Status, stats.Files, stats.Uploaded, stats.Skipped, stats.Errors, humanSize(stats.Bytes), fin.Sub(rec.Started).Round(time.Second))
	if rec.Message != "" {
		rl.Errorf("%s", rec.Message)
	}
	m.writeRecord(rec)
	if job.Notify && rec.Status != StatusCancelled {
		if nerr := notifyDSM(rec.Status, job.Name, rec.Message); nerr != nil {
			rl.Warnf("уведомление DSM не отправлено: %v", nerr)
		}
	}
	m.mu.Lock()
	delete(m.active, job.ID)
	m.mu.Unlock()
	m.prune(job.ID)
}

// prune оставляет последние keepRuns запусков задания.
func (m *Manager) prune(jobID string) {
	n := 0
	for _, r := range m.readRecords() {
		if r.JobID != jobID || r.Status == StatusRunning {
			continue
		}
		if n++; n > keepRuns {
			os.Remove(m.runPath(r.ID, ".json"))
			os.Remove(m.runPath(r.ID, ".log"))
		}
	}
}

// Cancel останавливает выполняющееся задание.
func (m *Manager) Cancel(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ar, ok := m.active[jobID]; ok {
		ar.cancel()
		return true
	}
	return false
}

// Active возвращает выполняющиеся запуски по ID заданий.
func (m *Manager) Active() map[string]RunRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]RunRecord{}
	for id, ar := range m.active {
		out[id] = ar.rec
	}
	return out
}

// History возвращает последние запуски (jobID пусто — все).
func (m *Manager) History(jobID string, limit int) []RunRecord {
	var out []RunRecord
	for _, r := range m.readRecords() {
		if jobID == "" || r.JobID == jobID {
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

var runIDRe = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// ReadLog читает журнал запуска начиная со смещения; возвращает текст и новое смещение.
func (m *Manager) ReadLog(runID string, offset int64) (string, int64, error) {
	if !runIDRe.MatchString(runID) {
		return "", 0, errors.New("неверный идентификатор запуска")
	}
	f, err := os.Open(m.runPath(runID, ".log"))
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size := fi.Size()
	const maxChunk = 256 << 10
	if offset < 0 || offset > size {
		offset = 0
	}
	prefix := ""
	if size-offset > maxChunk {
		offset = size - maxChunk
		prefix = "… (начало журнала пропущено)\n"
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", 0, err
	}
	b, err := io.ReadAll(io.LimitReader(f, size-offset))
	if err != nil {
		return "", 0, err
	}
	return prefix + string(b), offset + int64(len(b)), nil
}

// NextRun возвращает следующий запуск по расписанию.
func (m *Manager) NextRun(job store.Job) (time.Time, bool) {
	if !job.Enabled || strings.TrimSpace(job.Schedule) == "" {
		return time.Time{}, false
	}
	if at, ok, err := cron.OnceAt(job.Schedule); ok {
		return at, err == nil && at.After(time.Now())
	}
	s, err := cron.Parse(job.Schedule)
	if err != nil {
		return time.Time{}, false
	}
	return s.Next(time.Now())
}

// RunScheduler проверяет расписания каждые 15 секунд.
func (m *Manager) RunScheduler(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.tick(now)
		}
	}
}

func (m *Manager) tick(now time.Time) {
	minute := now.Truncate(time.Minute)
	for _, job := range m.Store.Get().Jobs {
		if !job.Enabled || strings.TrimSpace(job.Schedule) == "" {
			continue
		}
		if at, ok, err := cron.OnceAt(job.Schedule); ok {
			if err == nil && !now.Before(at) {
				m.fireOnce(job)
			}
			continue
		}
		s, err := cron.Parse(job.Schedule)
		if err != nil || !s.Match(now) {
			continue
		}
		m.mu.Lock()
		dup := m.fired[job.ID].Equal(minute)
		m.fired[job.ID] = minute
		m.mu.Unlock()
		if dup {
			continue
		}
		if _, err := m.Start(job.ID, "schedule"); err != nil {
			log.Printf("расписание: задание %q не запущено: %v", job.Name, err)
		}
	}
}

// fireOnce запускает однократное задание и снимает расписание, чтобы оно не повторилось.
func (m *Manager) fireOnce(job store.Job) {
	if _, err := m.Start(job.ID, "schedule"); err != nil {
		log.Printf("расписание: задание %q не запущено: %v", job.Name, err)
		return
	}
	_ = m.Store.Update(func(c *store.Config) error {
		for i := range c.Jobs {
			if c.Jobs[i].ID == job.ID {
				c.Jobs[i].Schedule = ""
			}
		}
		return nil
	})
}

// Shutdown останавливает все запуски и ждёт их завершения.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.stopAll()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}
