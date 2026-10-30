package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"nascloud/internal/provider"
	"nascloud/internal/store"
)

// nowFunc подменяется в тестах (метка времени в имени архива).
var nowFunc = time.Now

// partWriter пишет поток во временные файлы и выгружает каждую готовую часть.
// На диске одновременно лежит не больше одной части.
type partWriter struct {
	ctx       context.Context
	env       Env
	remoteDir string
	baseName  string
	partSize  int64 // 0 — одна часть без номера

	f       *os.File
	written int64
	n       int
	err     error

	names []string // выгруженные части
	bytes int64
}

func (w *partWriter) open() error {
	if err := os.MkdirAll(w.env.TmpDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(w.env.TmpDir, "part-*.tmp")
	if err != nil {
		return fmt.Errorf("временный файл: %w", err)
	}
	w.f, w.written = f, 0
	w.n++
	return nil
}

func (w *partWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	total := 0
	for len(b) > 0 {
		if w.f == nil {
			if err := w.open(); err != nil {
				w.err = err
				return total, err
			}
		}
		chunk := b
		if w.partSize > 0 {
			if room := w.partSize - w.written; int64(len(chunk)) > room {
				chunk = chunk[:room]
			}
		}
		n, err := w.f.Write(chunk)
		w.written += int64(n)
		total += n
		b = b[n:]
		if err != nil {
			w.err = fmt.Errorf("запись во временный файл (нет места в %s?): %w", w.env.TmpDir, err)
			return total, w.err
		}
		if w.partSize > 0 && w.written >= w.partSize {
			if err := w.flush(); err != nil {
				w.err = err
				return total, err
			}
		}
	}
	return total, nil
}

// flush выгружает текущую часть и удаляет временный файл.
func (w *partWriter) flush() error {
	if w.f == nil {
		return nil
	}
	tmp, size := w.f.Name(), w.written
	if err := w.f.Close(); err != nil {
		os.Remove(tmp)
		w.f = nil
		return err
	}
	w.f = nil
	defer os.Remove(tmp)
	name := w.baseName
	if w.partSize > 0 {
		name = fmt.Sprintf("%s.%03d", w.baseName, w.n)
	}
	remote := provider.Join(w.remoteDir, name)
	start := time.Now()
	src := provider.Source{Size: size, Open: func() (io.ReadCloser, error) { return os.Open(tmp) }}
	how, err := provider.UploadVerified(w.ctx, w.env.Provider, remote, src, w.env.Verify)
	if err != nil {
		return fmt.Errorf("выгрузка %s: %w", name, err)
	}
	w.names = append(w.names, remote)
	w.bytes += size
	w.env.Log.Infof("выгружена часть %s (%s, %.0f с, сверка: %s)", name, humanSize(size), time.Since(start).Seconds(), how)
	return nil
}

func (w *partWriter) Close() error {
	if w.err != nil {
		return w.err
	}
	return w.flush()
}

// abort удаляет незавершённую временную часть.
func (w *partWriter) abort() {
	if w.f != nil {
		name := w.f.Name()
		w.f.Close()
		os.Remove(name)
		w.f = nil
	}
}

// RunArchive создаёт tar.gz из источников, выгружает его (по частям при заданном
// размере) и применяет ротацию.
func RunArchive(ctx context.Context, env Env, job store.Job) (Stats, error) {
	var st Stats
	slug := Slug(job.Name)
	remoteDir := provider.Join(job.RemoteDir, slug)
	if err := provider.MkdirAll(ctx, env.Provider, remoteDir); err != nil {
		return st, fmt.Errorf("папка %s: %w", remoteDir, err)
	}
	ext := ".tar.gz"
	if job.Archive.NoCompress {
		ext = ".tar"
	}
	base := fmt.Sprintf("%s-%s%s", slug, nowFunc().Format("2006-01-02_150405"), ext)
	pw := &partWriter{ctx: ctx, env: env, remoteDir: remoteDir, baseName: base, partSize: int64(job.Archive.PartSizeMB) << 20}
	env.Log.Infof("архив %s → %s (часть: %d МБ, хранить: %d)", base, remoteDir, job.Archive.PartSizeMB, job.Archive.Keep)

	var out io.Writer = pw
	var gz *gzip.Writer
	if !job.Archive.NoCompress {
		gz, _ = gzip.NewWriterLevel(pw, gzip.BestSpeed)
		out = gz
	}
	tw := tar.NewWriter(out)
	err := addSources(ctx, env, job, tw, pw, &st)
	if err == nil && st.Files == 0 {
		err = errors.New("в источниках не найдено ни одного файла")
	}
	if err == nil {
		err = tw.Close()
	}
	if err == nil && gz != nil {
		err = gz.Close()
	}
	if err == nil {
		err = pw.Close()
	}
	if err != nil {
		pw.abort()
		for _, p := range pw.names { // неполный снимок бесполезен
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			if derr := env.Provider.Delete(dctx, p); derr != nil {
				env.Log.Warnf("не удалось удалить часть %s неполного архива: %v", p, derr)
			}
			cancel()
		}
		return st, err
	}
	st.Uploaded, st.Parts, st.Bytes = len(pw.names), len(pw.names), pw.bytes
	applyRetention(ctx, env, remoteDir, slug, job.Archive)
	return st, nil
}

func addSources(ctx context.Context, env Env, job store.Job, tw *tar.Writer, pw *partWriter, st *Stats) error {
	seen := map[string]int{}
	for _, src := range job.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		fi, err := os.Stat(src)
		if err != nil {
			env.Log.Errorf("источник %s недоступен: %v", src, err)
			st.Errors++
			continue
		}
		base := uniqueBase(src, seen)
		if !fi.IsDir() {
			if err := addFile(ctx, env, tw, pw, st, src, base); err != nil {
				return err
			}
			continue
		}
		root := filepath.Clean(src)
		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if err != nil {
				env.Log.Errorf("%s: %v", p, err)
				st.Errors++
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			name := base
			if rel != "." {
				name = base + "/" + filepath.ToSlash(rel)
				if excluded(job.Excludes, d.Name(), filepath.ToSlash(rel)) {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
			}
			switch t := d.Type(); {
			case d.IsDir():
				info, err := d.Info()
				if err != nil {
					st.Errors++
					return nil
				}
				hdr, _ := tar.FileInfoHeader(info, "")
				hdr.Name, hdr.Uname, hdr.Gname = name+"/", "", ""
				return tw.WriteHeader(hdr)
			case t&fs.ModeSymlink != 0:
				link, err := os.Readlink(p)
				info, ierr := d.Info()
				if err != nil || ierr != nil {
					st.Errors++
					return nil
				}
				hdr, _ := tar.FileInfoHeader(info, link)
				hdr.Name, hdr.Uname, hdr.Gname = name, "", ""
				return tw.WriteHeader(hdr)
			case t.IsRegular():
				return addFile(ctx, env, tw, pw, st, p, name)
			}
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// addFile добавляет файл. Возвращает ошибку только когда поток архива нельзя продолжать.
func addFile(ctx context.Context, env Env, tw *tar.Writer, pw *partWriter, st *Stats, p, name string) error {
	f, err := os.Open(p)
	if err != nil {
		env.Log.Errorf("%s: %v (выдайте пользователю пакета право чтения)", p, err)
		st.Errors++
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		env.Log.Errorf("%s: %v", p, err)
		st.Errors++
		return nil
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		st.Errors++
		return nil
	}
	hdr.Name, hdr.Uname, hdr.Gname = name, "", ""
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	n, err := io.Copy(tw, io.LimitReader(ctxReader{ctx, f}, fi.Size()))
	if err != nil {
		if pw.err != nil {
			return pw.err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		env.Log.Errorf("%s: ошибка чтения: %v", p, err)
		st.Errors++
	} else if n < fi.Size() {
		env.Log.Warnf("%s изменился во время архивации", p)
		st.Errors++
	}
	if n < fi.Size() { // дополняем нулями, чтобы tar остался корректным
		if _, err := io.CopyN(tw, zeroReader{}, fi.Size()-n); err != nil {
			return err
		}
	}
	st.Files++
	st.Bytes += n
	return nil
}

// applyRetention удаляет старые снимки; трогает только файлы со строго нашим именем.
func applyRetention(ctx context.Context, env Env, remoteDir, slug string, opts store.ArchiveOpts) {
	if opts.Keep <= 0 {
		return
	}
	list, err := env.Provider.List(ctx, remoteDir)
	if err != nil {
		env.Log.Warnf("ротация: не удалось получить список %s: %v", remoteDir, err)
		return
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(slug) + `-(\d{4}-\d{2}-\d{2}_\d{6})\.tar(\.gz)?(\.\d{3,})?$`)
	groups := map[string][]string{}
	for _, e := range list {
		if e.IsDir {
			continue
		}
		if m := re.FindStringSubmatch(e.Name); m != nil {
			groups[m[1]] = append(groups[m[1]], e.Path)
		}
	}
	stamps := make([]string, 0, len(groups))
	for s := range groups {
		stamps = append(stamps, s)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(stamps)))
	times := make([]time.Time, len(stamps))
	for i, s := range stamps {
		times[i], _ = time.ParseInLocation("2006-01-02_150405", s, time.Local)
	}
	for _, i := range staleStamps(times, opts.Keep, opts.Policy) {
		for _, p := range groups[stamps[i]] {
			if err := env.Provider.Delete(ctx, p); err != nil {
				env.Log.Warnf("ротация: не удалось удалить %s: %v", p, err)
			}
		}
		env.Log.Infof("ротация: удалён старый снимок %s", stamps[i])
	}
}
