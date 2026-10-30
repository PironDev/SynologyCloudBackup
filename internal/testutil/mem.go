// Package testutil — хранилище в памяти, имитатор REST API Яндекс.Диска и
// общий тест соответствия драйверов. Используется только в тестах.
package testutil

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"nascloud/internal/provider"
)

type memFile struct {
	data []byte
	mod  time.Time
}

// Mem — драйвер хранилища в памяти.
type Mem struct {
	mu    sync.Mutex
	files map[string]memFile
	dirs  map[string]time.Time

	WithMD5 bool
	// FailUpload вызывается перед каждой загрузкой (n — порядковый номер попытки с 1).
	FailUpload func(p string, n int) error
	uploads    int
}

// NewMem создаёт пустое хранилище с корнем.
func NewMem() *Mem {
	return &Mem{files: map[string]memFile{}, dirs: map[string]time.Time{"/": time.Now()}}
}

// Uploads возвращает число попыток загрузки.
func (m *Mem) Uploads() int { m.mu.Lock(); defer m.mu.Unlock(); return m.uploads }

// Get возвращает содержимое файла.
func (m *Mem) Get(p string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[provider.Clean(p)]
	return f.data, ok
}

// Put кладёт файл напрямую (создавая папки).
func (m *Mem) Put(p string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = provider.Clean(p)
	for d := path.Dir(p); ; d = path.Dir(d) {
		m.dirs[d] = time.Now()
		if d == "/" {
			break
		}
	}
	m.files[p] = memFile{data: append([]byte(nil), data...), mod: time.Now()}
}

// Paths возвращает все пути (файлы и папки), кроме корня.
func (m *Mem) Paths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p := range m.files {
		out = append(out, p)
	}
	for p := range m.dirs {
		if p != "/" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Files возвращает только пути файлов.
func (m *Mem) Files() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p := range m.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (m *Mem) entry(p string) (provider.Entry, bool) {
	if t, ok := m.dirs[p]; ok {
		return provider.Entry{Name: path.Base(p), Path: p, IsDir: true, ModTime: t}, true
	}
	if f, ok := m.files[p]; ok {
		e := provider.Entry{Name: path.Base(p), Path: p, Size: int64(len(f.data)), ModTime: f.mod}
		if m.WithMD5 {
			s := md5.Sum(f.data)
			e.MD5 = hex.EncodeToString(s[:])
		}
		return e, true
	}
	return provider.Entry{}, false
}

func (m *Mem) Check(ctx context.Context) (provider.AccountInfo, error) {
	return provider.AccountInfo{Title: "mem-user", Total: 1000, Used: 10}, nil
}

func (m *Mem) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = provider.Clean(dir)
	if _, ok := m.dirs[dir]; !ok {
		return nil, provider.ErrNotFound
	}
	var out []provider.Entry
	for p := range m.dirs {
		if p != "/" && path.Dir(p) == dir {
			e, _ := m.entry(p)
			out = append(out, e)
		}
	}
	for p := range m.files {
		if path.Dir(p) == dir {
			e, _ := m.entry(p)
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *Mem) Stat(ctx context.Context, p string) (provider.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entry(provider.Clean(p)); ok {
		return e, nil
	}
	return provider.Entry{}, provider.ErrNotFound
}

func (m *Mem) Mkdir(ctx context.Context, p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = provider.Clean(p)
	if _, ok := m.dirs[p]; ok {
		return nil
	}
	if _, ok := m.files[p]; ok {
		return provider.ErrExists
	}
	if _, ok := m.dirs[path.Dir(p)]; !ok {
		return provider.ErrNotFound
	}
	m.dirs[p] = time.Now()
	return nil
}

func (m *Mem) Upload(ctx context.Context, p string, src provider.Source) error {
	m.mu.Lock()
	m.uploads++
	n := m.uploads
	hook := m.FailUpload
	m.mu.Unlock()
	p = provider.Clean(p)
	if hook != nil {
		if err := hook(p, n); err != nil {
			return err
		}
	}
	rc, err := src.Open()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return err
	}
	if int64(len(data)) != src.Size {
		return fmt.Errorf("размер данных %d, заявлено %d", len(data), src.Size)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.dirs[path.Dir(p)]; !ok {
		return provider.ErrNotFound
	}
	if _, ok := m.dirs[p]; ok {
		return provider.ErrExists
	}
	m.files[p] = memFile{data: data, mod: time.Now()}
	return nil
}

func (m *Mem) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[provider.Clean(p)]
	if !ok {
		return nil, provider.ErrNotFound
	}
	if offset > int64(len(f.data)) {
		offset = int64(len(f.data))
	}
	d := f.data[offset:]
	if length >= 0 && length < int64(len(d)) {
		d = d[:length]
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), d...))), nil
}

func (m *Mem) Delete(ctx context.Context, p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p = provider.Clean(p)
	_, isDir := m.dirs[p]
	_, isFile := m.files[p]
	if !isDir && !isFile {
		return provider.ErrNotFound
	}
	delete(m.files, p)
	delete(m.dirs, p)
	prefix := p + "/"
	for k := range m.files {
		if strings.HasPrefix(k, prefix) {
			delete(m.files, k)
		}
	}
	for k := range m.dirs {
		if strings.HasPrefix(k, prefix) {
			delete(m.dirs, k)
		}
	}
	return nil
}

func (m *Mem) transfer(from, to string, overwrite, move bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	from, to = provider.Clean(from), provider.Clean(to)
	if _, ok := m.entry(from); !ok {
		return provider.ErrNotFound
	}
	if _, ok := m.dirs[path.Dir(to)]; !ok {
		return provider.ErrNotFound
	}
	if _, ok := m.entry(to); ok && !overwrite {
		return provider.ErrExists
	}
	apply := func(src string) string { return to + strings.TrimPrefix(src, from) }
	newFiles, newDirs := map[string]memFile{}, map[string]time.Time{}
	for k, v := range m.files {
		if k == from || strings.HasPrefix(k, from+"/") {
			newFiles[apply(k)] = v
		}
	}
	for k, v := range m.dirs {
		if k == from || strings.HasPrefix(k, from+"/") {
			newDirs[apply(k)] = v
		}
	}
	if move {
		for k := range m.files {
			if k == from || strings.HasPrefix(k, from+"/") {
				delete(m.files, k)
			}
		}
		for k := range m.dirs {
			if k == from || strings.HasPrefix(k, from+"/") {
				delete(m.dirs, k)
			}
		}
	}
	for k, v := range newFiles {
		m.files[k] = v
	}
	for k, v := range newDirs {
		m.dirs[k] = v
	}
	return nil
}

func (m *Mem) Move(ctx context.Context, from, to string, overwrite bool) error {
	return m.transfer(from, to, overwrite, true)
}

func (m *Mem) Copy(ctx context.Context, from, to string, overwrite bool) error {
	return m.transfer(from, to, overwrite, false)
}

// Descriptor возвращает описание «вендора» для тестов, всегда отдающего m.
func Descriptor(m provider.Provider) provider.Descriptor {
	return provider.Descriptor{
		Kind: "test", Title: "Тест",
		Fields: []provider.Field{{Key: "token", Label: "Токен", Secret: true}, {Key: "login", Label: "Логин"}},
		New: func(s map[string]string) (provider.Provider, error) {
			if s["token"] == "" {
				return nil, fmt.Errorf("не задан токен")
			}
			return m, nil
		},
	}
}
