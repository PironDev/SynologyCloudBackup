package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"nascloud/internal/provider"
	"nascloud/internal/store"
)

// Env — окружение запуска задания.
type Env struct {
	Provider provider.Provider
	Log      *RunLog
	StateDir string // манифесты зеркал
	TmpDir   string // временные части архивов
	Verify   string // режим сверки после загрузки (provider.Verify*)
}

type manEntry struct {
	Size  int64 `json:"s"`
	MTime int64 `json:"m"`
}

type manifest struct {
	mu    sync.Mutex
	path  string
	Files map[string]manEntry `json:"files"`
}

func loadManifest(p string) *manifest {
	m := &manifest{path: p, Files: map[string]manEntry{}}
	if raw, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(raw, m)
		if m.Files == nil {
			m.Files = map[string]manEntry{}
		}
	}
	return m
}

func (m *manifest) get(k string) (manEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.Files[k]
	return e, ok
}

func (m *manifest) set(k string, size, mtime int64) {
	m.mu.Lock()
	m.Files[k] = manEntry{Size: size, MTime: mtime}
	m.mu.Unlock()
}

func (m *manifest) save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func isFatal(err error) bool {
	return errors.Is(err, provider.ErrInsufficientStorage) || errors.Is(err, provider.ErrAuth)
}

type mirror struct {
	ctx    context.Context
	cancel context.CancelFunc
	env    Env
	job    store.Job
	man    *manifest
	sem    chan struct{}
	wg     sync.WaitGroup

	mu    sync.Mutex
	stats Stats
	fatal error

	dirMu sync.Mutex
	made  map[string]bool
}

// RunMirror выгружает новые и изменённые файлы источников в RemoteDir.
// Возвращает ошибку только при остановке запуска (отмена, нет места, авторизация).
func RunMirror(parent context.Context, env Env, job store.Job) (Stats, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	conc := job.Concurrency
	if conc < 1 {
		conc = 4
	}
	if conc > 16 {
		conc = 16
	}
	m := &mirror{
		ctx: ctx, cancel: cancel, env: env, job: job,
		man:  loadManifest(filepath.Join(env.StateDir, job.ID+".json")),
		sem:  make(chan struct{}, conc),
		made: map[string]bool{},
	}
	seen := map[string]int{}
	for _, src := range job.Sources {
		if ctx.Err() != nil {
			break
		}
		st, err := os.Stat(src)
		if err != nil {
			env.Log.Errorf("источник %s недоступен: %v", src, err)
			m.addErr()
			continue
		}
		if st.IsDir() {
			remoteRoot := provider.Join(job.RemoteDir, uniqueBase(src, seen))
			env.Log.Infof("зеркало %s → %s (потоков: %d)", src, remoteRoot, conc)
			m.processDir(src, remoteRoot, "")
		} else {
			remoteDir := provider.Clean(job.RemoteDir)
			if err := m.ensureDir(remoteDir); err != nil {
				m.dirFailed(remoteDir, err)
				continue
			}
			remote := m.listRemote(remoteDir)
			m.handleFile(remoteDir, src, st, remote)
		}
	}
	m.wg.Wait()
	if err := m.man.save(); err != nil {
		env.Log.Warnf("не удалось сохранить манифест: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fatal != nil {
		return m.stats, m.fatal
	}
	return m.stats, parent.Err()
}

func (m *mirror) addErr() {
	m.mu.Lock()
	m.stats.Errors++
	m.mu.Unlock()
}

func (m *mirror) setFatal(err error) {
	m.mu.Lock()
	if m.fatal == nil {
		m.fatal = err
		m.env.Log.Errorf("остановка: %v", err)
	}
	m.mu.Unlock()
	m.cancel()
}

func (m *mirror) dirFailed(remoteDir string, err error) {
	if isFatal(err) {
		m.setFatal(err)
		return
	}
	if m.ctx.Err() == nil {
		m.env.Log.Errorf("папка %s: %v", remoteDir, err)
		m.addErr()
	}
}

func (m *mirror) ensureDir(dir string) error {
	m.dirMu.Lock()
	defer m.dirMu.Unlock()
	if m.made[dir] {
		return nil
	}
	if err := provider.MkdirAll(m.ctx, m.env.Provider, dir); err != nil {
		return err
	}
	m.made[dir] = true
	return nil
}

func (m *mirror) listRemote(dir string) map[string]provider.Entry {
	out := map[string]provider.Entry{}
	list, err := m.env.Provider.List(m.ctx, dir)
	if err != nil {
		if isFatal(err) {
			m.setFatal(err)
		} else if !errors.Is(err, provider.ErrNotFound) && m.ctx.Err() == nil {
			m.env.Log.Warnf("не удалось получить список %s: %v (файлы будут выгружены заново)", dir, err)
		}
		return out
	}
	for _, e := range list {
		out[e.Name] = e
	}
	return out
}

func (m *mirror) processDir(localDir, remoteDir, rel string) {
	if m.ctx.Err() != nil {
		return
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		m.env.Log.Errorf("каталог %s не прочитан: %v (выдайте пользователю пакета право чтения на эту папку)", localDir, err)
		m.addErr()
		return
	}
	if err := m.ensureDir(remoteDir); err != nil {
		m.dirFailed(remoteDir, err)
		return
	}
	remote := m.listRemote(remoteDir)
	for _, de := range entries {
		if m.ctx.Err() != nil {
			return
		}
		name := de.Name()
		relPath := path.Join(rel, name)
		if excluded(m.job.Excludes, name, relPath) {
			continue
		}
		full := filepath.Join(localDir, name)
		switch t := de.Type(); {
		case t&fs.ModeSymlink != 0:
			continue
		case de.IsDir():
			m.processDir(full, provider.Join(remoteDir, name), relPath)
		case t.IsRegular():
			fi, err := de.Info()
			if err != nil {
				m.env.Log.Errorf("файл %s: %v", full, err)
				m.addErr()
				continue
			}
			m.handleFile(remoteDir, full, fi, remote)
		}
	}
}

func (m *mirror) handleFile(remoteDir, full string, fi os.FileInfo, remote map[string]provider.Entry) {
	remotePath := provider.Join(remoteDir, fi.Name())
	var re *provider.Entry
	if e, ok := remote[fi.Name()]; ok {
		re = &e
	}
	m.mu.Lock()
	m.stats.Files++
	m.mu.Unlock()
	if m.unchanged(remotePath, full, fi, re) {
		m.mu.Lock()
		m.stats.Skipped++
		m.mu.Unlock()
		return
	}
	select {
	case m.sem <- struct{}{}:
	case <-m.ctx.Done():
		return
	}
	m.wg.Add(1)
	go m.upload(full, remotePath, fi)
}

// unchanged решает, можно ли пропустить файл.
func (m *mirror) unchanged(remotePath, full string, fi os.FileInfo, re *provider.Entry) bool {
	if re == nil || re.IsDir || re.Size != fi.Size() {
		return false
	}
	mt := fi.ModTime().UnixNano()
	if me, ok := m.man.get(remotePath); ok && me.Size == fi.Size() && me.MTime == mt {
		return true
	}
	if re.MD5 != "" {
		sum, err := md5File(full)
		if err == nil && strings.EqualFold(sum, re.MD5) {
			m.man.set(remotePath, fi.Size(), mt)
			return true
		}
		return false
	}
	if !re.ModTime.IsZero() && !re.ModTime.Before(fi.ModTime()) {
		m.man.set(remotePath, fi.Size(), mt)
		return true
	}
	return false
}

func (m *mirror) upload(local, remote string, fi os.FileInfo) {
	defer m.wg.Done()
	defer func() { <-m.sem }()
	src := provider.Source{Size: fi.Size(), ModTime: fi.ModTime(), Open: func() (io.ReadCloser, error) { return os.Open(local) }}
	how, err := provider.UploadVerified(m.ctx, m.env.Provider, remote, src, m.env.Verify)
	if err != nil {
		switch {
		case isFatal(err):
			m.setFatal(err)
		case m.ctx.Err() != nil:
		default:
			m.env.Log.Errorf("%s: %v", local, err)
			m.addErr()
		}
		return
	}
	m.man.set(remote, fi.Size(), fi.ModTime().UnixNano())
	m.mu.Lock()
	m.stats.Uploaded++
	m.stats.Bytes += fi.Size()
	n := m.stats.Uploaded
	m.mu.Unlock()
	m.env.Log.Infof("выгружено: %s (%s, сверка: %s)", local, humanSize(fi.Size()), how)
	if n%500 == 0 {
		_ = m.man.save()
	}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d Б", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cБ", float64(n)/float64(div), []rune("КМГТП")[exp])
}
