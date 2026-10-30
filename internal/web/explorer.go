package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"nascloud/internal/provider/cache"
	"nascloud/internal/store"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nascloud/internal/explorer"
	"nascloud/internal/provider"
	"nascloud/internal/provider/local"
)

const localSrc = "local"

// localRoots — папки NAS для проводника: заданные в настройках или общие папки томов.
func (s *Server) localRoots() (roots []string, auto bool) {
	if r := s.Store.Get().Settings.LocalRoots; len(r) > 0 {
		return r, false
	}
	return local.DiscoverRoots("/volume*"), true
}

// provFor возвращает драйвер источника: "local" — NAS, иначе ID аккаунта.
func (s *Server) provFor(src string) (provider.Provider, error) {
	if src == localSrc {
		roots, _ := s.localRoots()
		if len(roots) == 0 {
			return nil, errors.New("не найдено ни одной папки NAS, доступной пакету: выдайте пользователю пакета права на общие папки в DSM (Панель управления → Общая папка → Права доступа) или укажите папки в «Настройки → Папки NAS»")
		}
		return local.New(roots), nil
	}
	acc, ok := s.Store.Get().Account(src)
	if !ok {
		return nil, errors.New("аккаунт не найден")
	}
	pr, err := s.Pool.Get(acc)
	if err != nil {
		return nil, err
	}
	return s.cacheOf(pr), nil
}

// cacheOf возвращает общую для всех запросов обёртку с кэшем списков (30 с).
func (s *Server) cacheOf(pr provider.Provider) *cache.FS {
	if c, ok := s.caches.Load(pr); ok {
		return c.(*cache.FS)
	}
	c, _ := s.caches.LoadOrStore(pr, cache.Wrap(pr, 30*time.Second))
	return c.(*cache.FS)
}

// canPublish — облако умеет публичные ссылки и файлы лежат в нём как есть (без шифрования и нарезки).
func (s *Server) canPublish(a store.Account) bool {
	if a.Fields["enc_password"] != "" {
		return false
	}
	if v := strings.TrimSpace(a.Fields["split_mb"]); v != "" && v != "0" {
		return false
	}
	raw, err := s.Pool.Raw(a)
	if err != nil {
		return false
	}
	_, ok := raw.(provider.Publisher)
	return ok
}

// trasher возвращает перенос в корзину, если облако аккаунта его умеет.
func (s *Server) trasher(a store.Account) (provider.Trasher, bool) {
	raw, err := s.Pool.Raw(a)
	if err != nil {
		return nil, false
	}
	t, ok := raw.(provider.Trasher)
	return t, ok
}

// canTrash — облако умеет переносить файлы в свою корзину.
func (s *Server) canTrash(a store.Account) bool {
	_, ok := s.trasher(a)
	return ok
}

type xEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	MD5   string `json:"md5,omitempty"`
}

func toX(e provider.Entry) xEntry {
	x := xEntry{Name: e.Name, Path: e.Path, IsDir: e.IsDir, Size: e.Size, MD5: e.MD5}
	if !e.ModTime.IsZero() {
		x.Mtime = e.ModTime.Unix()
	}
	return x
}

func (s *Server) xSources(w http.ResponseWriter, r *http.Request) {
	type src struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Kind  string   `json:"kind"`
		Roots []string `json:"roots,omitempty"`
		Share bool     `json:"canPublish,omitempty"`
		Trash bool     `json:"canTrash,omitempty"`
	}
	roots, auto := s.localRoots()
	out := []src{{ID: localSrc, Name: "NAS", Kind: "local", Roots: roots}}
	for _, a := range s.Store.Get().Accounts {
		out = append(out, src{ID: a.ID, Name: a.Name, Kind: s.Desc.Kind, Share: s.canPublish(a), Trash: s.canTrash(a)})
	}
	writeJSON(w, 200, map[string]any{"sources": out, "autoRoots": auto, "provider": s.Desc.Title})
}

func (s *Server) xList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := s.provFor(q.Get("src"))
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	dir := provider.Clean(q.Get("path"))
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	var ents []provider.Entry
	if c, ok := p.(*cache.FS); ok && q.Get("fresh") == "1" {
		ents, err = c.ListFresh(ctx, dir)
	} else {
		ents, err = p.List(ctx, dir)
	}
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	out := make([]xEntry, 0, len(ents))
	for _, e := range ents {
		out = append(out, toX(e))
	}
	res := map[string]any{"path": dir, "entries": out}
	if l, ok := p.(*local.FS); ok {
		res["free"] = l.FreeSpace(dir)
	}
	writeJSON(w, 200, res)
}

func (s *Server) xMkdir(w http.ResponseWriter, r *http.Request) {
	var in struct{ Src, Path string }
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	p, err := s.provFor(in.Src)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	if bad := badName(path.Base(in.Path)); bad != "" {
		jerr(w, 400, "%s", bad)
		return
	}
	if err := p.Mkdir(r.Context(), provider.Clean(in.Path)); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func badName(n string) string {
	switch {
	case n == "" || n == "." || n == "..":
		return "недопустимое имя"
	case strings.ContainsAny(n, "/\\\x00"):
		return "имя не должно содержать / и \\"
	case len(n) > 255:
		return "слишком длинное имя"
	}
	return ""
}

func (s *Server) xRename(w http.ResponseWriter, r *http.Request) {
	var in struct{ Src, From, Name string }
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if bad := badName(in.Name); bad != "" {
		jerr(w, 400, "%s", bad)
		return
	}
	p, err := s.provFor(in.Src)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	from := provider.Clean(in.From)
	to := provider.Join(path.Dir(from), in.Name)
	if err := p.Move(r.Context(), from, to, false); err != nil {
		if errors.Is(err, provider.ErrExists) {
			jerr(w, 409, "«%s» уже существует", in.Name)
			return
		}
		jerr(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": to})
}

// xDelete удаляет объекты. Trash — перенести в корзину облака вместо безвозвратного удаления.
func (s *Server) xDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Src   string
		Paths []string
		Trash bool
	}
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	p, err := s.provFor(in.Src)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	del := p.Delete
	if in.Trash {
		acc, ok := s.Store.Get().Account(in.Src)
		if !ok {
			jerr(w, 400, "корзину можно использовать только в облаке")
			return
		}
		t, ok := s.trasher(acc)
		if !ok {
			jerr(w, 400, "это облако не переносит файлы в корзину")
			return
		}
		del = t.Trash
	}
	var errs []string
	n := 0
	for _, x := range in.Paths {
		x = provider.Clean(x)
		if x == "/" {
			errs = append(errs, "корень облака нельзя удалить")
			continue
		}
		if err := del(r.Context(), x); err != nil {
			errs = append(errs, path.Base(x)+": "+err.Error())
			continue
		}
		n++
	}
	writeJSON(w, 200, map[string]any{"deleted": n, "errors": errs})
}

func (s *Server) xTransfer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Op, Src, Dst, DstDir, Conflict string
		Paths                          []string
	}
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	id, err := s.X.Start(in.Op, in.Src, in.Paths, in.Dst, in.DstDir, in.Conflict)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]string{"id": id})
}

func (s *Server) xTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"tasks": s.X.List()})
}

func (s *Server) xCancel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"ok": s.X.Cancel(r.PathValue("id"))})
}

func (s *Server) xClear(w http.ResponseWriter, r *http.Request) {
	s.X.ClearFinished()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) xInfo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := s.provFor(q.Get("src"))
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	e, err := p.Stat(ctx, provider.Clean(q.Get("path")))
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	res := map[string]any{"entry": toX(e), "mime": mimeOf(e.Name), "previewable": inlineOK(mimeOf(e.Name))}
	if e.IsDir && q.Get("deep") == "1" {
		files, dirs, size, trunc, err := explorer.DirStats(ctx, p, e.Path, 20000)
		if err != nil {
			jerr(w, 400, "%v", err)
			return
		}
		res["stats"] = map[string]any{"files": files, "dirs": dirs, "size": size, "truncated": trunc}
	}
	writeJSON(w, 200, res)
}

func mimeOf(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(name))); t != "" {
		return strings.SplitN(t, ";", 2)[0]
	}
	return "application/octet-stream"
}

// inlineOK — можно ли открывать в браузере (только безопасные типы: ни HTML, ни SVG).
func inlineOK(t string) bool {
	switch t {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp",
		"audio/mpeg", "audio/ogg", "audio/wav", "audio/mp4", "audio/flac", "audio/x-wav",
		"video/mp4", "video/webm", "video/ogg", "video/quicktime":
		return true
	}
	return false
}

func (s *Server) xDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := s.provFor(q.Get("src"))
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	fp := provider.Clean(q.Get("path"))
	e, err := p.Stat(r.Context(), fp)
	if err != nil {
		jerr(w, 404, "%v", err)
		return
	}
	if e.IsDir {
		jerr(w, 400, "это папка")
		return
	}
	start, length, partial := int64(0), e.Size, false
	if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") && !strings.Contains(rg, ",") {
		a, b, _ := strings.Cut(strings.TrimPrefix(rg, "bytes="), "-")
		if a == "" { // последние N байт
			if n, err := strconv.ParseInt(b, 10, 64); err == nil && n > 0 {
				start = max(e.Size-n, 0)
				length, partial = e.Size-start, true
			}
		} else if st, err := strconv.ParseInt(a, 10, 64); err == nil && st >= 0 && st < e.Size {
			end := e.Size - 1
			if b != "" {
				if v, err := strconv.ParseInt(b, 10, 64); err == nil && v < end {
					end = v
				}
			}
			if end >= st {
				start, length, partial = st, end-st+1, true
			}
		} else if err == nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", e.Size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}
	rc, err := p.Download(r.Context(), fp, start, length)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	defer rc.Close()
	t := mimeOf(e.Name)
	h := w.Header()
	disp := "attachment"
	if q.Get("dl") != "1" && inlineOK(t) {
		disp = "inline"
	} else {
		t = "application/octet-stream"
	}
	h.Set("Content-Type", t)
	h.Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(e.Name))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	h.Set("Cache-Control", "private, no-store")
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, e.Size))
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, rc)
}

// xUpload принимает файл целиком в теле запроса: PUT /api/x/file?src=&path=<папка>&name=<имя>&conflict=.
func (s *Server) xUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := s.provFor(q.Get("src"))
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	name := q.Get("name")
	if bad := badName(name); bad != "" {
		jerr(w, 400, "%s", bad)
		return
	}
	dir := provider.Clean(q.Get("path"))
	ctx := r.Context()
	if err := provider.MkdirAll(ctx, p, dir); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	dest := provider.Join(dir, name)
	if _, err := p.Stat(ctx, dest); err == nil {
		switch q.Get("conflict") {
		case "overwrite":
		case "skip":
			writeJSON(w, 200, map[string]any{"ok": true, "skipped": true, "path": dest})
			return
		default:
			if dest, err = explorer.FreeName(ctx, p, dir, name); err != nil {
				jerr(w, 400, "%v", err)
				return
			}
		}
	}
	size := r.ContentLength
	var src provider.Source
	if _, isLocal := p.(*local.FS); isLocal {
		used := false
		src = provider.Source{Size: size, Open: func() (io.ReadCloser, error) {
			if used {
				return nil, errors.New("тело запроса уже прочитано")
			}
			used = true
			return io.NopCloser(r.Body), nil
		}}
	} else { // облако: сначала во временный файл, чтобы можно было повторять попытки
		tmpDir := s.Store.Get().Settings.TmpDir
		if tmpDir == "" {
			tmpDir = filepath.Join(s.Mgr.DataDir, "tmp")
		}
		_ = os.MkdirAll(tmpDir, 0o700)
		f, err := os.CreateTemp(tmpDir, "upload-*")
		if err != nil {
			jerr(w, 500, "%v", err)
			return
		}
		defer os.Remove(f.Name())
		defer f.Close()
		n, err := io.Copy(f, r.Body)
		if err != nil {
			jerr(w, 400, "загрузка прервана: %v", err)
			return
		}
		src = provider.Source{Size: n, Open: func() (io.ReadCloser, error) { return os.Open(f.Name()) }}
	}
	if err := p.Upload(ctx, dest, src); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": dest})
}

// xPublish делает публичную ссылку на файл или папку (если облако это умеет).
func (s *Server) xPublish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Src, Path string
		Unpublish bool
	}
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	acc, ok := s.Store.Get().Account(in.Src)
	if !ok || !s.canPublish(acc) {
		jerr(w, 400, "это облако не поддерживает публичные ссылки (или включено шифрование/нарезка файлов)")
		return
	}
	raw, err := s.Pool.Raw(acc)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	pub := raw.(provider.Publisher)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if in.Unpublish {
		if err := pub.Unpublish(ctx, in.Path); err != nil {
			jerr(w, 400, "%v", err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	u, err := pub.Publish(ctx, in.Path)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	writeJSON(w, 200, map[string]any{"url": u})
}

// xEmptyTrash очищает корзину облака (как CleanUp в rclone).
func (s *Server) xEmptyTrash(w http.ResponseWriter, r *http.Request) {
	var in struct{ Src string }
	if err := readJSON(r, &in); err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	acc, ok := s.Store.Get().Account(in.Src)
	if !ok {
		jerr(w, 400, "корзину можно очистить только в облаке")
		return
	}
	raw, err := s.Pool.Raw(acc)
	if err != nil {
		jerr(w, 400, "%v", err)
		return
	}
	te, ok := raw.(provider.TrashEmptier)
	if !ok {
		jerr(w, 400, "у этого облака нет корзины для очистки")
		return
	}
	if err := te.EmptyTrash(r.Context()); err != nil {
		jerr(w, 500, "очистка корзины: %v", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
