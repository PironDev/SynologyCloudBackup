// Package local — драйвер файловой системы NAS: даёт проводнику те же операции,
// что и облачные драйверы. Доступны только перечисленные корневые папки.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"nascloud/internal/auth"
	"nascloud/internal/provider"
)

type root struct{ name, abs string }

// FS — файловая система с несколькими корнями. Виртуальный путь: /<имя корня>/<путь внутри>.
type FS struct{ roots []root }

// New создаёт драйвер. Имя корня — имя его папки; одинаковые имена получают суффикс.
func New(paths []string) *FS {
	f := &FS{}
	used := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) || p == "/" {
			continue
		}
		name := filepath.Base(p)
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", filepath.Base(p), i)
		}
		used[name] = true
		f.roots = append(f.roots, root{name, p})
	}
	return f
}

// DiscoverRoots находит общие папки томов (/volume1/photo и т. п.), доступные для чтения.
func DiscoverRoots(volumesGlob string) []string {
	vols, _ := filepath.Glob(volumesGlob)
	sort.Strings(vols)
	var out []string
	for _, v := range vols {
		ents, err := os.ReadDir(v)
		if err != nil {
			continue
		}
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() || strings.HasPrefix(n, "@") || strings.HasPrefix(n, "#") || n == "lost+found" {
				continue
			}
			full := filepath.Join(v, n)
			if !readable(full) { // у пользователя пакета нет прав — в списке не показываем
				continue
			}
			out = append(out, full)
		}
	}
	return out
}

// Roots возвращает пары «имя → абсолютный путь».
func (f *FS) Roots() [][2]string {
	out := make([][2]string, len(f.roots))
	for i, r := range f.roots {
		out[i] = [2]string{r.name, r.abs}
	}
	return out
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w", provider.ErrNotFound)
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%w", provider.ErrExists)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("нет доступа (выдайте пользователю пакета права на эту папку в DSM): %w", err)
	case errors.Is(err, syscall.ENOSPC):
		return provider.ErrInsufficientStorage
	}
	return err
}

func within(base, p string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// resolve переводит виртуальный путь в реальный и проверяет, что он не выходит за корень
// (в том числе через символические ссылки).
func (f *FS) resolve(p string) (string, *root, error) {
	p = provider.Clean(p)
	if p == "/" {
		return "", nil, nil
	}
	rest := strings.TrimPrefix(p, "/")
	name, sub, _ := strings.Cut(rest, "/")
	var r *root
	for i := range f.roots {
		if f.roots[i].name == name {
			r = &f.roots[i]
		}
	}
	if r == nil {
		return "", nil, fmt.Errorf("%w: %s", provider.ErrNotFound, p)
	}
	abs := filepath.Join(r.abs, filepath.FromSlash(sub))
	real, err := filepath.EvalSymlinks(r.abs)
	if err != nil {
		return "", nil, mapErr(err)
	}
	// Проверяем ближайший существующий предок: ссылка не должна уводить за пределы корня.
	probe := abs
	for {
		if rp, err := filepath.EvalSymlinks(probe); err == nil {
			if !within(real, rp) {
				return "", nil, errors.New("путь выходит за пределы разрешённой папки")
			}
			break
		}
		next := filepath.Dir(probe)
		if next == probe || !within(r.abs, next) {
			break
		}
		probe = next
	}
	return abs, r, nil
}

func entryOf(vpath string, fi fs.FileInfo) provider.Entry {
	return provider.Entry{Name: fi.Name(), Path: vpath, IsDir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()}
}

// Check возвращает объём тома первой папки.
func (f *FS) Check(ctx context.Context) (provider.AccountInfo, error) {
	if len(f.roots) == 0 {
		return provider.AccountInfo{}, errors.New("не настроены папки NAS")
	}
	total, free, err := DiskUsage(f.roots[0].abs)
	if err != nil {
		return provider.AccountInfo{}, mapErr(err)
	}
	return provider.AccountInfo{Title: "NAS", Total: total, Used: total - free}, nil
}

func (f *FS) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	abs, _, err := f.resolve(dir)
	if err != nil {
		return nil, err
	}
	if abs == "" {
		out := []provider.Entry{}
		for _, r := range f.roots {
			e := provider.Entry{Name: r.name, Path: "/" + r.name, IsDir: true}
			if fi, err := os.Stat(r.abs); err == nil {
				e.ModTime = fi.ModTime()
			}
			out = append(out, e)
		}
		return out, nil
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]provider.Entry, 0, len(ents))
	base := provider.Clean(dir)
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.Mode()&fs.ModeSymlink != 0 { // показываем то, на что указывает ссылка, если она внутри
			if t, err := os.Stat(filepath.Join(abs, e.Name())); err == nil {
				fi = t
			} else {
				continue
			}
		}
		out = append(out, entryOf(provider.Join(base, e.Name()), fi))
	}
	return out, nil
}

func (f *FS) Stat(ctx context.Context, p string) (provider.Entry, error) {
	abs, _, err := f.resolve(p)
	if err != nil {
		return provider.Entry{}, err
	}
	if abs == "" {
		return provider.Entry{Name: "/", Path: "/", IsDir: true}, nil
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return provider.Entry{}, mapErr(err)
	}
	return entryOf(provider.Clean(p), fi), nil
}

func (f *FS) Mkdir(ctx context.Context, p string) error {
	abs, _, err := f.resolve(p)
	if err != nil {
		return err
	}
	if abs == "" {
		return errors.New("в корне нельзя создавать папки")
	}
	if err := os.Mkdir(abs, 0o775); err != nil {
		if fi, serr := os.Stat(abs); serr == nil && fi.IsDir() {
			return nil
		}
		return mapErr(err)
	}
	return nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// Upload пишет во временный файл рядом и переименовывает — частично записанного файла не остаётся.
func (f *FS) Upload(ctx context.Context, p string, src provider.Source) error {
	abs, _, err := f.resolve(p)
	if err != nil {
		return err
	}
	if abs == "" {
		return errors.New("нельзя записать файл в корень")
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		return fmt.Errorf("%w: на месте файла папка", provider.ErrExists)
	}
	rc, err := src.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp := filepath.Join(filepath.Dir(abs), ".cb-"+auth.RandomToken(6)+".part")
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return mapErr(err)
	}
	_, err = io.Copy(out, ctxReader{ctx, rc})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return mapErr(err)
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return mapErr(err)
	}
	return nil
}

type fileLimit struct {
	io.Reader
	f *os.File
}

func (l fileLimit) Close() error { return l.f.Close() }

func (f *FS) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	abs, _, err := f.resolve(p)
	if err != nil {
		return nil, err
	}
	fh, err := os.Open(abs)
	if err != nil {
		return nil, mapErr(err)
	}
	if fi, err := fh.Stat(); err != nil || fi.IsDir() {
		fh.Close()
		return nil, errors.New("это папка, а не файл")
	}
	if offset > 0 {
		if _, err := fh.Seek(offset, io.SeekStart); err != nil {
			fh.Close()
			return nil, err
		}
	}
	var r io.Reader = fh
	if length >= 0 {
		r = io.LimitReader(fh, length)
	}
	return fileLimit{ctxReader{ctx, r}, fh}, nil
}

func (f *FS) Delete(ctx context.Context, p string) error {
	abs, r, err := f.resolve(p)
	if err != nil {
		return err
	}
	if abs == "" || abs == r.abs {
		return errors.New("корневую папку удалять нельзя")
	}
	if _, err := os.Lstat(abs); err != nil {
		return mapErr(err)
	}
	return mapErr(os.RemoveAll(abs))
}

func (f *FS) Move(ctx context.Context, from, to string, overwrite bool) error {
	a, ra, err := f.resolve(from)
	if err != nil {
		return err
	}
	b, _, err := f.resolve(to)
	if err != nil {
		return err
	}
	if a == "" || a == ra.abs || b == "" {
		return errors.New("корневые папки перемещать нельзя")
	}
	if within(a, b) && a != b {
		return errors.New("нельзя переместить папку саму в себя")
	}
	if _, err := os.Lstat(b); err == nil {
		if !overwrite {
			return provider.ErrExists
		}
		if err := os.RemoveAll(b); err != nil {
			return mapErr(err)
		}
	}
	if err := os.Rename(a, b); err != nil {
		var le *os.LinkError
		if errors.As(err, &le) && errors.Is(le.Err, syscall.EXDEV) {
			if err := copyTree(ctx, a, b); err != nil {
				return mapErr(err)
			}
			return mapErr(os.RemoveAll(a))
		}
		return mapErr(err)
	}
	return nil
}

func (f *FS) Copy(ctx context.Context, from, to string, overwrite bool) error {
	a, _, err := f.resolve(from)
	if err != nil {
		return err
	}
	b, _, err := f.resolve(to)
	if err != nil {
		return err
	}
	if a == "" || b == "" {
		return errors.New("неверный путь")
	}
	if within(a, b) {
		return errors.New("нельзя скопировать папку саму в себя")
	}
	if _, err := os.Lstat(b); err == nil {
		if !overwrite {
			return provider.ErrExists
		}
		if err := os.RemoveAll(b); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(copyTree(ctx, a, b))
}

func copyTree(ctx context.Context, from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o775)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, ctxReader{ctx, in})
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	})
}

// Volume — объём тома, на котором лежит корневая папка.
type Volume struct {
	Path        string `json:"path"`
	Total, Free int64
}

// Volumes возвращает объёмы томов корней (без повторов).
func (f *FS) Volumes() []Volume {
	var out []Volume
	seen := map[uint64]bool{}
	for _, r := range f.roots {
		dev, ok := deviceID(r.abs)
		if ok && seen[dev] {
			continue
		}
		seen[dev] = true
		if t, fr, err := DiskUsage(r.abs); err == nil {
			out = append(out, Volume{Path: r.abs, Total: t, Free: fr})
		}
	}
	return out
}

// FreeSpace — свободное место на томе по виртуальному пути (0, если неизвестно).
func (f *FS) FreeSpace(p string) int64 {
	abs, r, err := f.resolve(p)
	if err != nil {
		return 0
	}
	if abs == "" {
		if r == nil {
			if len(f.roots) == 0 {
				return 0
			}
			r = &f.roots[0]
		}
		abs = r.abs
	}
	_, free, _ := DiskUsage(abs)
	return free
}

var _ provider.Provider = (*FS)(nil)

func readable(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	return err == nil || errors.Is(err, io.EOF)
}
