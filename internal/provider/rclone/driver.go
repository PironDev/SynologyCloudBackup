// Package rclone подключает backend'ы rclone как provider.Provider.
// rclone используется как Go-библиотека, отдельный процесс не запускается.
package rclone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/sync"

	"nascloud/internal/provider"
)

var errRoot = errors.New("корень облака нельзя менять этой операцией")

// driver — provider.Provider поверх fs.Fs из rclone. Пути Provider абсолютные (/a/b),
// пути rclone идут без ведущего слеша и отсчитываются от root.
type driver struct {
	ri   *fs.RegInfo
	cfg  map[string]string
	root string // путь в облаке из поля «root»
	f    fs.Fs
}

var _ provider.Provider = (*driver)(nil)

// New создаёт драйвер backend'а rclone с именем name. Значения полей — в cfg, путь — в cfg["root"].
func New(name string, cfg map[string]string) (provider.Provider, error) {
	ri, err := fs.Find(name)
	if err != nil {
		return nil, err
	}
	// rclone не подставляет значения по умолчанию для пустых ключей, поэтому заполняем их сами.
	full := make(map[string]string, len(cfg)+len(ri.Options))
	for _, o := range ri.Options {
		if o.Default != nil {
			full[o.Name] = fmt.Sprint(o.Default)
		}
	}
	maps.Copy(full, cfg)
	d := &driver{ri: ri, cfg: full, root: cfg["root"]}
	if d.f, err = d.newFs(context.Background(), d.root); err != nil {
		return nil, err
	}
	return d, nil
}

// newFs создаёт fs.Fs для пути rel внутри облака (rel — полный путь от корня облака).
func (d *driver) newFs(ctx context.Context, full string) (fs.Fs, error) {
	return d.ri.NewFs(ctx, d.ri.Name, full, configmap.Simple(d.cfg))
}

// rel переводит путь Provider в путь rclone относительно root.
func rel(p string) string { return strings.TrimPrefix(path.Clean("/"+p), "/") }

func abs(remote string) string { return "/" + remote }

// mapErr переводит ошибки rclone в ошибки provider.
func mapErr(err error) error {
	if errors.Is(err, fs.ErrorObjectNotFound) || errors.Is(err, fs.ErrorDirNotFound) {
		return provider.ErrNotFound
	}
	return err
}

func (d *driver) Check(ctx context.Context) (provider.AccountInfo, error) {
	info := provider.AccountInfo{Title: d.f.String()}
	if _, err := d.f.List(ctx, ""); err != nil {
		return info, mapErr(err)
	}
	// Объём не обязателен: облако может его не сообщать.
	if ab, ok := d.f.(fs.Abouter); ok {
		if u, err := ab.About(ctx); err == nil {
			info.Total, info.Used = val(u.Total), val(u.Used)
		}
	}
	return info, nil
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func entry(ctx context.Context, e fs.DirEntry) provider.Entry {
	out := provider.Entry{Name: path.Base(e.Remote()), Path: abs(e.Remote()), Size: max(e.Size(), 0), ModTime: e.ModTime(ctx)}
	if o, ok := e.(fs.Object); ok {
		out.MD5, _ = o.Hash(ctx, hash.MD5) // не у всех облаков есть MD5
	} else {
		out.IsDir = true
	}
	return out
}

func (d *driver) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	ents, err := d.f.List(ctx, rel(dir))
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]provider.Entry, 0, len(ents))
	for _, e := range ents {
		out = append(out, entry(ctx, e))
	}
	return out, nil
}

func (d *driver) Stat(ctx context.Context, p string) (provider.Entry, error) {
	r := rel(p)
	if r == "" {
		return provider.Entry{Path: "/", IsDir: true}, nil
	}
	if o, err := d.f.NewObject(ctx, r); err == nil {
		return entry(ctx, o), nil
	}
	// Папка (в том числе без маркера в S3) ищется в списке родителя.
	parent := path.Dir(r)
	if parent == "." {
		parent = ""
	}
	ents, err := d.f.List(ctx, parent)
	if err != nil {
		return provider.Entry{}, mapErr(err)
	}
	for _, e := range ents {
		if e.Remote() == r {
			return entry(ctx, e), nil
		}
	}
	return provider.Entry{}, provider.ErrNotFound
}

func (d *driver) Mkdir(ctx context.Context, p string) error {
	return mapErr(d.f.Mkdir(ctx, rel(p)))
}

func (d *driver) Upload(ctx context.Context, p string, src provider.Source) error {
	in, err := src.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	info := object.NewStaticObjectInfo(rel(p), src.ModTime, src.Size, true, nil, d.f)
	_, err = d.f.Put(ctx, in, info)
	return mapErr(err)
}

func (d *driver) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	o, err := d.f.NewObject(ctx, rel(p))
	if err != nil {
		return nil, mapErr(err)
	}
	var opts []fs.OpenOption
	if offset > 0 || length >= 0 {
		end := int64(-1) // до конца файла
		if length >= 0 {
			end = offset + length - 1
		}
		opts = append(opts, &fs.RangeOption{Start: offset, End: end})
	}
	rc, err := o.Open(ctx, opts...)
	return rc, mapErr(err)
}

func (d *driver) Delete(ctx context.Context, p string) error {
	r := rel(p)
	if r == "" {
		return errRoot
	}
	if o, err := d.f.NewObject(ctx, r); err == nil {
		return mapErr(o.Remove(ctx))
	}
	return mapErr(operations.Purge(ctx, d.f, r))
}

func (d *driver) Move(ctx context.Context, from, to string, overwrite bool) error {
	return d.transfer(ctx, from, to, overwrite, true)
}

func (d *driver) Copy(ctx context.Context, from, to string, overwrite bool) error {
	return d.transfer(ctx, from, to, overwrite, false)
}

// transfer копирует или переносит файл или папку. Папка при overwrite сливается с существующей.
func (d *driver) transfer(ctx context.Context, from, to string, overwrite, move bool) error {
	src, dst := rel(from), rel(to)
	if src == "" || dst == "" {
		return errRoot
	}
	if _, err := d.Stat(ctx, from); err != nil {
		return err
	}
	if _, err := d.Stat(ctx, to); err == nil && !overwrite {
		return provider.ErrExists
	}
	o, err := d.f.NewObject(ctx, src)
	if err != nil {
		// Не файл: переносим папку через подкаталоги rclone.
		srcFs, err := d.newFs(ctx, path.Join(d.root, src))
		if err != nil {
			return err
		}
		dstFs, err := d.newFs(ctx, path.Join(d.root, dst))
		if err != nil {
			return err
		}
		if move {
			return mapErr(sync.MoveDir(ctx, dstFs, srcFs, true, true))
		}
		return mapErr(sync.CopyDir(ctx, dstFs, srcFs, true))
	}
	var old fs.Object
	if got, err := d.f.NewObject(ctx, dst); err == nil {
		old = got
	}
	if move {
		_, err = operations.Move(ctx, d.f, old, dst, o)
	} else {
		_, err = operations.Copy(ctx, d.f, old, dst, o)
	}
	return mapErr(err)
}
