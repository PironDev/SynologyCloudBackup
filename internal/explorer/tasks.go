// Package explorer — фоновые операции проводника: копирование и перемещение между
// NAS и облаками с прогрессом и отменой.
package explorer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nascloud/internal/auth"
	"nascloud/internal/provider"
)

// Resolver возвращает драйвер источника ("local" или ID аккаунта).
type Resolver func(src string) (provider.Provider, error)

// Task — состояние операции (отдаётся интерфейсу как есть).
type Task struct {
	ID        string     `json:"id"`
	Op        string     `json:"op"` // copy | move
	Src       string     `json:"src"`
	Dst       string     `json:"dst"`
	DstDir    string     `json:"dstDir"`
	Names     []string   `json:"names"`
	Status    string     `json:"status"` // scanning | running | done | error | canceled
	Total     int64      `json:"total"`  // 0 — неизвестно (операция на стороне облака)
	Done      int64      `json:"done"`
	Files     int        `json:"files"`
	FilesDone int        `json:"filesDone"`
	Current   string     `json:"current"`
	Errors    []string   `json:"errors,omitempty"`
	Started   time.Time  `json:"started"`
	Finished  *time.Time `json:"finished,omitempty"`
}

type job struct {
	mu       sync.Mutex
	t        Task
	cancel   context.CancelFunc
	conflict string
}

// Manager хранит операции.
type Manager struct {
	res  Resolver
	mu   sync.Mutex
	jobs []*job
}

func New(res Resolver) *Manager { return &Manager{res: res} }

// Start запускает операцию. conflict: rename | overwrite | skip.
func (m *Manager) Start(op, src string, paths []string, dst, dstDir, conflict string) (string, error) {
	if op != "copy" && op != "move" {
		return "", errors.New("неизвестная операция")
	}
	if len(paths) == 0 {
		return "", errors.New("ничего не выбрано")
	}
	if conflict != "overwrite" && conflict != "skip" {
		conflict = "rename"
	}
	sp, err := m.res(src)
	if err != nil {
		return "", err
	}
	dp, err := m.res(dst)
	if err != nil {
		return "", err
	}
	dstDir = provider.Clean(dstDir)
	clean := make([]string, 0, len(paths))
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		p = provider.Clean(p)
		if p == "/" {
			return "", errors.New("нельзя выбрать корень")
		}
		if src == dst && (dstDir == p || strings.HasPrefix(dstDir, p+"/")) {
			return "", errors.New("нельзя копировать или переносить папку внутрь самой себя")
		}
		clean = append(clean, p)
		names = append(names, path.Base(p))
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{cancel: cancel, conflict: conflict, t: Task{ID: auth.RandomToken(6), Op: op, Src: src, Dst: dst, DstDir: dstDir,
		Names: names, Status: "scanning", Started: time.Now()}}
	m.mu.Lock()
	m.jobs = append(m.jobs, j)
	if len(m.jobs) > 40 { // забываем старые завершённые
		for i, o := range m.jobs {
			if o.snapshot().Finished != nil {
				m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()
	go func() {
		defer cancel()
		j.run(ctx, sp, dp, clean)
	}()
	return j.t.ID, nil
}

func (j *job) snapshot() Task {
	j.mu.Lock()
	defer j.mu.Unlock()
	t := j.t
	t.Errors = append([]string(nil), j.t.Errors...)
	return t
}

// List возвращает операции от новых к старым.
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(m.jobs))
	for i := len(m.jobs) - 1; i >= 0; i-- {
		out = append(out, m.jobs[i].snapshot())
	}
	return out
}

// Cancel отменяет операцию.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.t.ID == id {
			j.cancel()
			return true
		}
	}
	return false
}

// ClearFinished убирает завершённые операции из списка.
func (m *Manager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := m.jobs[:0]
	for _, j := range m.jobs {
		if j.snapshot().Finished == nil {
			keep = append(keep, j)
		}
	}
	m.jobs = keep
}

func (j *job) set(fn func(t *Task)) { j.mu.Lock(); fn(&j.t); j.mu.Unlock() }

func (j *job) fail(format string, a ...any) {
	j.set(func(t *Task) {
		if len(t.Errors) < 30 {
			t.Errors = append(t.Errors, fmt.Sprintf(format, a...))
		}
	})
}

type node struct {
	from, rel string
	isDir     bool
	size      int64
}

func (j *job) finish(ctx context.Context) {
	j.set(func(t *Task) {
		now := time.Now()
		t.Finished = &now
		t.Current = ""
		switch {
		case ctx.Err() != nil:
			t.Status = "canceled"
		case len(t.Errors) > 0:
			t.Status = "error"
		default:
			t.Status = "done"
		}
	})
}

func (j *job) run(ctx context.Context, sp, dp provider.Provider, paths []string) {
	defer j.finish(ctx)
	t := j.snapshot()
	if err := provider.MkdirAll(ctx, dp, t.DstDir); err != nil {
		j.fail("папка назначения %s: %v", t.DstDir, err)
		return
	}
	same := t.Src == t.Dst
	if same { // быстрый путь: операция выполняется самим хранилищем
		j.set(func(t *Task) { t.Status = "running"; t.Files = len(paths) })
		for _, p := range paths {
			if ctx.Err() != nil {
				return
			}
			j.set(func(t *Task) { t.Current = path.Base(p) })
			inPlace := p == provider.Join(t.DstDir, path.Base(p))
			if inPlace && t.Op == "move" {
				j.set(func(t *Task) { t.FilesDone++ })
				continue
			}
			to, skip, err := j.target(ctx, dp, t.DstDir, path.Base(p), inPlace)
			if err != nil {
				j.fail("%s: %v", p, err)
				continue
			}
			if skip {
				j.set(func(t *Task) { t.FilesDone++ })
				continue
			}
			if t.Op == "move" {
				err = sp.Move(ctx, p, to, j.conflict == "overwrite")
			} else {
				err = sp.Copy(ctx, p, to, j.conflict == "overwrite")
			}
			if err != nil {
				j.fail("%s: %v", p, err)
				continue
			}
			j.set(func(t *Task) { t.FilesDone++ })
		}
		return
	}

	// Между разными источниками: обходим дерево, затем передаём файлы потоком.
	type item struct {
		top   string
		nodes []node
	}
	var items []item
	var total int64
	var files int
	for _, p := range paths {
		if ctx.Err() != nil {
			return
		}
		e, err := sp.Stat(ctx, p)
		if err != nil {
			j.fail("%s: %v", p, err)
			continue
		}
		it := item{top: p}
		if e.IsDir {
			if err := walk(ctx, sp, p, "", &it.nodes); err != nil {
				j.fail("%s: %v", p, err)
				continue
			}
		} else {
			it.nodes = []node{{from: p, rel: "", size: e.Size}}
		}
		for _, n := range it.nodes {
			if !n.isDir {
				total += n.size
				files++
			}
		}
		items = append(items, it)
	}
	j.set(func(t *Task) { t.Status = "running"; t.Total = total; t.Files = files })
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		name := path.Base(it.top)
		top, skip, err := j.target(ctx, dp, t.DstDir, name, false)
		if err != nil {
			j.fail("%s: %v", it.top, err)
			continue
		}
		if skip {
			n := 0
			for _, nd := range it.nodes {
				if !nd.isDir {
					n++
				}
			}
			j.set(func(t *Task) { t.FilesDone += n })
			continue
		}
		failed := false
		for _, nd := range it.nodes {
			if ctx.Err() != nil {
				return
			}
			to := top
			if nd.rel != "" {
				to = provider.Join(top, nd.rel)
			}
			if nd.isDir {
				if err := provider.MkdirAll(ctx, dp, to); err != nil {
					j.fail("%s: %v", to, err)
					failed = true
				}
				continue
			}
			if j.conflict == "skip" {
				if _, err := dp.Stat(ctx, to); err == nil {
					j.set(func(t *Task) { t.FilesDone++; t.Done += nd.size })
					continue
				}
			}
			if err := j.transfer(ctx, sp, dp, nd, to); err != nil {
				if ctx.Err() == nil {
					j.fail("%s: %v", nd.from, err)
				}
				failed = true
				continue
			}
			j.set(func(t *Task) { t.FilesDone++ })
		}
		if t.Op == "move" && !failed && ctx.Err() == nil {
			if err := sp.Delete(ctx, it.top); err != nil {
				j.fail("скопировано, но не удалено %s: %v", it.top, err)
			}
		}
	}
}

// target подбирает путь назначения с учётом политики конфликтов имён.
func (j *job) target(ctx context.Context, dp provider.Provider, dir, name string, sameSpot bool) (to string, skip bool, err error) {
	to = provider.Join(dir, name)
	_, serr := dp.Stat(ctx, to)
	if serr != nil {
		if errors.Is(serr, provider.ErrNotFound) {
			return to, false, nil
		}
		return "", false, serr
	}
	switch j.conflict {
	case "skip":
		return to, true, nil
	case "overwrite":
		if sameSpot {
			return to, true, nil
		}
		return to, false, nil
	}
	free, err := FreeName(ctx, dp, dir, name)
	return free, false, err
}

// FreeName подбирает свободное имя вида «имя (1).расширение».
func FreeName(ctx context.Context, dp provider.Provider, dir, name string) (string, error) {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		cand := provider.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		if _, err := dp.Stat(ctx, cand); errors.Is(err, provider.ErrNotFound) {
			return cand, nil
		}
	}
	return "", errors.New("не удалось подобрать свободное имя")
}

func walk(ctx context.Context, p provider.Provider, dir, rel string, out *[]node) error {
	*out = append(*out, node{from: dir, rel: rel, isDir: true})
	ents, err := p.List(ctx, dir)
	if err != nil {
		return err
	}
	sort.Slice(ents, func(a, b int) bool { return ents[a].Name < ents[b].Name })
	for _, e := range ents {
		r := path.Join(rel, e.Name)
		if e.IsDir {
			if err := walk(ctx, p, e.Path, r, out); err != nil {
				return err
			}
		} else {
			*out = append(*out, node{from: e.Path, rel: r, size: e.Size})
		}
	}
	return nil
}

type countRC struct {
	io.ReadCloser
	j       *job
	counted *int64
}

func (c countRC) Read(b []byte) (int, error) {
	n, err := c.ReadCloser.Read(b)
	if n > 0 {
		atomic.AddInt64(c.counted, int64(n))
		c.j.mu.Lock()
		c.j.t.Done += int64(n)
		c.j.mu.Unlock()
	}
	return n, err
}

func (j *job) transfer(ctx context.Context, sp, dp provider.Provider, nd node, to string) error {
	j.set(func(t *Task) { t.Current = path.Base(nd.from) })
	var counted int64
	src := provider.Source{Size: nd.size, Open: func() (io.ReadCloser, error) {
		// повторная попытка начинается с нуля — возвращаем прогресс назад
		if c := atomic.SwapInt64(&counted, 0); c > 0 {
			j.set(func(t *Task) { t.Done -= c })
		}
		rc, err := sp.Download(ctx, nd.from, 0, -1)
		if err != nil {
			return nil, err
		}
		return countRC{rc, j, &counted}, nil
	}}
	_, err := provider.UploadVerified(ctx, dp, to, src, provider.VerifyHash)
	return err
}

// DirStats считает размер и число объектов в папке (не более limit записей).
func DirStats(ctx context.Context, p provider.Provider, dir string, limit int) (files, dirs int, size int64, truncated bool, err error) {
	var rec func(d string) error
	n := 0
	rec = func(d string) error {
		ents, err := p.List(ctx, d)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if n++; n > limit {
				truncated = true
				return nil
			}
			if e.IsDir {
				dirs++
				if err := rec(e.Path); err != nil {
					return err
				}
				if truncated {
					return nil
				}
			} else {
				files++
				size += e.Size
			}
		}
		return nil
	}
	err = rec(dir)
	return
}
