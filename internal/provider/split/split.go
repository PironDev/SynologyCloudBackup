// Package split — прозрачная нарезка файлов крупнее лимита на части (name.cbp0001, name.cbp0002, …)
// и склейка при чтении. Части скрыты: в списках файл виден целиком.
package split

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"

	"nascloud/internal/provider"
)

var partRe = regexp.MustCompile(`^(.+)\.cbp(\d{4})$`)

// MinLimit — минимально допустимый размер части.
const MinLimit = 16 << 20

// FS — провайдер-обёртка.
type FS struct {
	in    provider.Provider
	limit int64
}

// Wrap оборачивает провайдера; файлы больше limit байт режутся на части.
func Wrap(in provider.Provider, limit int64) *FS { return &FS{in: in, limit: limit} }

func partName(p string, i int) string { return fmt.Sprintf("%s.cbp%04d", provider.Clean(p), i) }

type group struct {
	parts []provider.Entry // по порядку, без пропусков с №1
	size  int64
}

// groups разбирает содержимое папки: обычные записи и склеенные файлы.
func groups(es []provider.Entry) (plain []provider.Entry, gs map[string]*group) {
	byBase := map[string]map[int]provider.Entry{}
	names := map[string]bool{}
	for _, e := range es {
		names[e.Name] = true
	}
	for _, e := range es {
		if m := partRe.FindStringSubmatch(e.Name); m != nil && !e.IsDir {
			i, _ := strconv.Atoi(m[2])
			if byBase[m[1]] == nil {
				byBase[m[1]] = map[int]provider.Entry{}
			}
			byBase[m[1]][i] = e
			continue
		}
		plain = append(plain, e)
	}
	gs = map[string]*group{}
	for base, m := range byBase {
		g := &group{}
		for i := 1; ; i++ {
			e, ok := m[i]
			if !ok {
				break
			}
			g.parts = append(g.parts, e)
			g.size += e.Size
		}
		if len(g.parts) > 0 && !names[base] {
			gs[base] = g
		}
	}
	return plain, gs
}

func (f *FS) Check(ctx context.Context) (provider.AccountInfo, error) { return f.in.Check(ctx) }

func (f *FS) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	es, err := f.in.List(ctx, dir)
	if err != nil {
		return es, err
	}
	plain, gs := groups(es)
	bases := make([]string, 0, len(gs))
	for b := range gs {
		bases = append(bases, b)
	}
	sort.Strings(bases)
	for _, b := range bases {
		g := gs[b]
		last := g.parts[len(g.parts)-1]
		plain = append(plain, provider.Entry{Name: b, Path: provider.Join(dir, b), Size: g.size, ModTime: last.ModTime})
	}
	return plain, nil
}

func (f *FS) lookup(ctx context.Context, p string) (*group, error) {
	p = provider.Clean(p)
	es, err := f.in.List(ctx, path.Dir(p))
	if err != nil {
		return nil, err
	}
	_, gs := groups(es)
	if g, ok := gs[path.Base(p)]; ok {
		return g, nil
	}
	return nil, provider.ErrNotFound
}

func (f *FS) Stat(ctx context.Context, p string) (provider.Entry, error) {
	e, err := f.in.Stat(ctx, p)
	if err == nil || !errors.Is(err, provider.ErrNotFound) {
		return e, err
	}
	g, gerr := f.lookup(ctx, p)
	if gerr != nil {
		return provider.Entry{}, err
	}
	last := g.parts[len(g.parts)-1]
	return provider.Entry{Name: path.Base(provider.Clean(p)), Path: provider.Clean(p), Size: g.size, ModTime: last.ModTime}, nil
}

func (f *FS) Mkdir(ctx context.Context, p string) error { return f.in.Mkdir(ctx, p) }

type partRC struct {
	io.Reader
	c io.Closer
}

func (r partRC) Close() error { return r.c.Close() }

// openSlice открывает [off, off+n) исходника; для файлов использует Seek.
func openSlice(src provider.Source, off, n int64) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		rc, err := src.Open()
		if err != nil {
			return nil, err
		}
		if off > 0 {
			if sk, ok := rc.(io.Seeker); ok {
				if _, err := sk.Seek(off, io.SeekStart); err != nil {
					rc.Close()
					return nil, err
				}
			} else if _, err := io.CopyN(io.Discard, rc, off); err != nil {
				rc.Close()
				return nil, err
			}
		}
		return partRC{io.LimitReader(rc, n), rc}, nil
	}
}

func (f *FS) UploadVerified(ctx context.Context, p string, src provider.Source, mode string) (string, error) {
	p = provider.Clean(p)
	if src.Size <= f.limit {
		return provider.UploadVerified(ctx, f.in, p, src, mode)
	}
	// файл вырос за лимит: обычный файл с тем же именем не должен перекрывать части
	if err := f.in.Delete(ctx, p); err != nil && !errors.Is(err, provider.ErrNotFound) {
		return "", err
	}
	n := int((src.Size + f.limit - 1) / f.limit)
	how := ""
	for i := 1; i <= n; i++ {
		off := int64(i-1) * f.limit
		sz := f.limit
		if off+sz > src.Size {
			sz = src.Size - off
		}
		h, err := provider.UploadVerified(ctx, f.in, partName(p, i), provider.Source{Size: sz, ModTime: src.ModTime, Open: openSlice(src, off, sz)}, mode)
		if err != nil {
			return "", fmt.Errorf("часть %d из %d: %w", i, n, err)
		}
		how = h
	}
	// лишние части от прежней, более длинной версии
	for i := n + 1; ; i++ {
		if err := f.in.Delete(ctx, partName(p, i)); err != nil {
			break
		}
	}
	return how, nil
}

func (f *FS) Upload(ctx context.Context, p string, src provider.Source) error {
	_, err := f.UploadVerified(ctx, p, src, provider.VerifySizeOnly)
	return err
}

type multiRC struct {
	ctx   context.Context
	f     *FS
	parts []provider.Entry
	off   int64
	left  int64
	cur   io.ReadCloser
	i     int
	skip  int64
}

func (m *multiRC) Read(p []byte) (int, error) {
	for {
		if m.left == 0 {
			return 0, io.EOF
		}
		if m.cur == nil {
			if m.i >= len(m.parts) {
				return 0, io.EOF
			}
			pe := m.parts[m.i]
			m.i++
			if m.skip >= pe.Size {
				m.skip -= pe.Size
				continue
			}
			rc, err := m.f.in.Download(m.ctx, pe.Path, m.skip, -1)
			if err != nil {
				return 0, err
			}
			m.skip, m.cur = 0, rc
		}
		if m.left >= 0 && int64(len(p)) > m.left {
			p = p[:m.left]
		}
		n, err := m.cur.Read(p)
		if m.left >= 0 {
			m.left -= int64(n)
		}
		if err == io.EOF {
			m.cur.Close()
			m.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (m *multiRC) Close() error {
	if m.cur != nil {
		return m.cur.Close()
	}
	return nil
}

func (f *FS) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	if _, err := f.in.Stat(ctx, p); err == nil {
		return f.in.Download(ctx, p, offset, length)
	} else if !errors.Is(err, provider.ErrNotFound) {
		return nil, err
	}
	g, err := f.lookup(ctx, p)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	left := int64(-1)
	if length >= 0 {
		left = length
	}
	return &multiRC{ctx: ctx, f: f, parts: g.parts, left: left, skip: offset}, nil
}

func (f *FS) Delete(ctx context.Context, p string) error {
	err := f.in.Delete(ctx, p)
	if err == nil || !errors.Is(err, provider.ErrNotFound) {
		return err
	}
	g, gerr := f.lookup(ctx, p)
	if gerr != nil {
		return err
	}
	for _, pe := range g.parts {
		if e := f.in.Delete(ctx, pe.Path); e != nil && !errors.Is(e, provider.ErrNotFound) {
			return e
		}
	}
	return nil
}

func (f *FS) moveCopy(ctx context.Context, from, to string, overwrite, move bool) error {
	do := func(a, b string) error {
		if move {
			return f.in.Move(ctx, a, b, overwrite)
		}
		return f.in.Copy(ctx, a, b, overwrite)
	}
	err := do(from, to)
	if err == nil || !errors.Is(err, provider.ErrNotFound) {
		return err
	}
	g, gerr := f.lookup(ctx, from)
	if gerr != nil {
		return err
	}
	for i := range g.parts {
		if e := do(partName(from, i+1), partName(to, i+1)); e != nil {
			return e
		}
	}
	return nil
}

func (f *FS) Move(ctx context.Context, from, to string, overwrite bool) error {
	return f.moveCopy(ctx, from, to, overwrite, true)
}

func (f *FS) Copy(ctx context.Context, from, to string, overwrite bool) error {
	return f.moveCopy(ctx, from, to, overwrite, false)
}

var _ provider.Provider = (*FS)(nil)
