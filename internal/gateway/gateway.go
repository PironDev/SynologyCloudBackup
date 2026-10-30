// Package gateway — локальный WebDAV-сервер поверх драйвера хранилища.
// Его можно подключить в File Station как «WebDAV-сервер», и облачный диск
// появится в дереве файлов DSM. Каждый аккаунт доступен по пути /<slug>/.
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nascloud/internal/auth"
	"nascloud/internal/engine"
	"nascloud/internal/provider"
	"nascloud/internal/store"
)

// Server — WebDAV-шлюз.
type Server struct {
	Store  *store.Store
	Pool   *engine.Pool
	TmpDir func() string
}

// ServeHTTP реализует http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Get()
	if _, pw, ok := r.BasicAuth(); !ok || !auth.Equal(pw, cfg.GatewayPassword) {
		if ok {
			time.Sleep(300 * time.Millisecond)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="CloudBackup gateway", charset="UTF-8"`)
		http.Error(w, "требуется авторизация", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("MS-Author-Via", "DAV")
		w.Header().Set("Allow", "OPTIONS, PROPFIND, PROPPATCH, GET, HEAD, PUT, MKCOL, DELETE, MOVE, COPY, LOCK, UNLOCK")
		w.WriteHeader(http.StatusOK)
		return
	}
	acc, remote, isRoot, ok := resolve(cfg, r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case "PROPFIND":
		s.propfind(w, r, cfg, acc, remote, isRoot)
		return
	case "LOCK":
		token := "opaquelocktoken:" + randomUUID()
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.Header().Set("Lock-Token", "<"+token+">")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock><D:locktype><D:write/></D:locktype><D:lockscope><D:exclusive/></D:lockscope><D:depth>infinity</D:depth><D:timeout>Second-3600</D:timeout><D:locktoken><D:href>%s</D:href></D:locktoken></D:activelock></D:lockdiscovery></D:prop>`, token)
		return
	case "UNLOCK":
		w.WriteHeader(http.StatusNoContent)
		return
	case "PROPPATCH":
		w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>%s</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`, escXML(r.URL.EscapedPath()))
		return
	}
	if isRoot && acc.ID == "" { // корень шлюза: список аккаунтов, изменять нечего
		http.Error(w, "корень шлюза доступен только для чтения", http.StatusForbidden)
		return
	}
	prov, err := s.Pool.Get(acc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.get(ctx, w, r, prov, remote)
	case http.MethodPut:
		s.put(ctx, w, r, prov, remote)
	case "MKCOL":
		if r.ContentLength > 0 {
			http.Error(w, "тело запроса не поддерживается", http.StatusUnsupportedMediaType)
			return
		}
		fail(w, prov.Mkdir(ctx, remote), http.StatusCreated)
	case http.MethodDelete:
		fail(w, prov.Delete(ctx, remote), http.StatusNoContent)
	case "MOVE", "COPY":
		s.moveCopy(ctx, w, r, cfg, acc, prov, remote)
	default:
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
	}
}

// resolve разбирает путь запроса: корень шлюза, корень аккаунта или путь внутри него.
func resolve(cfg store.Config, p string) (acc store.Account, remote string, isRoot, ok bool) {
	p = path.Clean("/" + p)
	if p == "/" {
		return store.Account{}, "/", true, true
	}
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)
	for _, a := range cfg.Accounts {
		if a.Slug == parts[0] {
			remote = "/"
			if len(parts) > 1 {
				remote = provider.Clean(parts[1])
			}
			return a, remote, remote == "/", true
		}
	}
	return store.Account{}, "", false, false
}

func fail(w http.ResponseWriter, err error, okStatus int) {
	switch {
	case err == nil:
		w.WriteHeader(okStatus)
	case errors.Is(err, provider.ErrNotFound):
		http.Error(w, "не найдено или нет родительской папки", http.StatusNotFound)
	case errors.Is(err, provider.ErrExists):
		http.Error(w, "уже существует", http.StatusMethodNotAllowed)
	case errors.Is(err, provider.ErrInsufficientStorage):
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
	case errors.Is(err, provider.ErrAuth):
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		log.Printf("шлюз: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func escXML(s string) string {
	var b bytes.Buffer
	_ = xmlEscape(&b, s)
	return b.String()
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func hrefFor(slug, remote string, dir bool) string {
	segs := []string{}
	if slug != "" {
		segs = append(segs, slug)
	}
	for _, s := range strings.Split(strings.Trim(remote, "/"), "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	h := "/"
	for i, s := range segs {
		h += (&url.URL{Path: s}).EscapedPath()
		if i < len(segs)-1 {
			h += "/"
		}
	}
	if dir && !strings.HasSuffix(h, "/") {
		h += "/"
	}
	return h
}

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, cfg store.Config, acc store.Account, remote string, isRoot bool) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
	depth := r.Header.Get("Depth")
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
	now := time.Now()
	if acc.ID == "" { // корень шлюза
		writeEntry(&buf, "/", "", true, 0, now)
		if depth != "0" {
			for _, a := range cfg.Accounts {
				writeEntry(&buf, hrefFor(a.Slug, "/", true), a.Name, true, 0, now)
			}
		}
	} else {
		prov, err := s.Pool.Get(acc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		var self provider.Entry
		if isRoot {
			self = provider.Entry{Name: acc.Name, Path: "/", IsDir: true, ModTime: now}
		} else {
			self, err = prov.Stat(r.Context(), remote)
			if err != nil {
				fail(w, err, 0)
				return
			}
		}
		writeEntry(&buf, hrefFor(acc.Slug, remote, self.IsDir), self.Name, self.IsDir, self.Size, self.ModTime)
		if self.IsDir && depth != "0" {
			list, err := prov.List(r.Context(), remote)
			if err != nil {
				fail(w, err, 0)
				return
			}
			for _, e := range list {
				writeEntry(&buf, hrefFor(acc.Slug, provider.Join(remote, e.Name), e.IsDir), e.Name, e.IsDir, e.Size, e.ModTime)
			}
		}
	}
	buf.WriteString(`</D:multistatus>`)
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write(buf.Bytes())
}

func writeEntry(b *bytes.Buffer, href, name string, dir bool, size int64, mod time.Time) {
	if mod.IsZero() {
		mod = time.Now()
	}
	b.WriteString("<D:response><D:href>" + escXML(href) + "</D:href><D:propstat><D:prop>")
	b.WriteString("<D:displayname>" + escXML(name) + "</D:displayname>")
	if dir {
		b.WriteString("<D:resourcetype><D:collection/></D:resourcetype>")
	} else {
		b.WriteString("<D:resourcetype/>")
		b.WriteString("<D:getcontentlength>" + strconv.FormatInt(size, 10) + "</D:getcontentlength>")
		ct := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
		if ct == "" {
			ct = "application/octet-stream"
		}
		b.WriteString("<D:getcontenttype>" + escXML(ct) + "</D:getcontenttype>")
	}
	b.WriteString("<D:getlastmodified>" + mod.UTC().Format(http.TimeFormat) + "</D:getlastmodified>")
	b.WriteString("<D:creationdate>" + mod.UTC().Format(time.RFC3339) + "</D:creationdate>")
	b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>")
}

// parseRange разбирает одиночный Range: bytes=a-b | a- | -n.
func parseRange(h string, size int64) (start, length int64, ok bool) {
	if !strings.HasPrefix(h, "bytes=") || strings.Contains(h, ",") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(h, "bytes=")
	i := strings.Index(spec, "-")
	if i < 0 {
		return 0, 0, false
	}
	a, b := strings.TrimSpace(spec[:i]), strings.TrimSpace(spec[i+1:])
	switch {
	case a == "":
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, n, true
	default:
		st, err := strconv.ParseInt(a, 10, 64)
		if err != nil || st < 0 || st >= size {
			return 0, 0, false
		}
		end := size - 1
		if b != "" {
			e, err := strconv.ParseInt(b, 10, 64)
			if err != nil || e < st {
				return 0, 0, false
			}
			if e < end {
				end = e
			}
		}
		return st, end - st + 1, true
	}
}

func (s *Server) get(ctx context.Context, w http.ResponseWriter, r *http.Request, prov provider.Provider, remote string) {
	e, err := prov.Stat(ctx, remote)
	if err != nil {
		fail(w, err, 0)
		return
	}
	if e.IsDir {
		http.Error(w, "это папка", http.StatusForbidden)
		return
	}
	h := w.Header()
	ct := mime.TypeByExtension(strings.ToLower(path.Ext(e.Name)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("Accept-Ranges", "bytes")
	if !e.ModTime.IsZero() {
		h.Set("Last-Modified", e.ModTime.UTC().Format(http.TimeFormat))
	}
	h.Set("ETag", fmt.Sprintf(`W/"%x-%x"`, e.Size, e.ModTime.Unix()))
	start, length, partial := int64(0), e.Size, false
	if rh := r.Header.Get("Range"); rh != "" && e.Size > 0 {
		if st, ln, ok := parseRange(rh, e.Size); ok {
			start, length, partial = st, ln, true
		} else {
			h.Set("Content-Range", fmt.Sprintf("bytes */%d", e.Size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, e.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead || length == 0 {
		return
	}
	rc, err := prov.Download(ctx, remote, start, length)
	if err != nil {
		log.Printf("шлюз: чтение %s: %v", remote, err)
		return
	}
	defer rc.Close()
	_, _ = io.Copy(w, rc)
}

func (s *Server) put(ctx context.Context, w http.ResponseWriter, r *http.Request, prov provider.Provider, remote string) {
	if remote == "/" {
		http.Error(w, "нельзя записать файл в корень аккаунта как ресурс", http.StatusForbidden)
		return
	}
	var src provider.Source
	if r.ContentLength >= 0 {
		used := false
		src = provider.Source{Size: r.ContentLength, Open: func() (io.ReadCloser, error) {
			if used {
				return nil, errors.New("повтор невозможен: тело запроса уже прочитано")
			}
			used = true
			return io.NopCloser(r.Body), nil
		}}
	} else { // без Content-Length: сначала во временный файл
		dir := s.TmpDir()
		_ = os.MkdirAll(dir, 0o700)
		tmp, err := os.CreateTemp(dir, "gw-*.tmp")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
		defer os.Remove(tmp.Name())
		n, err := io.Copy(tmp, r.Body)
		tmp.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name := tmp.Name()
		src = provider.Source{Size: n, Open: func() (io.ReadCloser, error) { return os.Open(filepath.Clean(name)) }}
	}
	fail(w, prov.Upload(ctx, remote, src), http.StatusCreated)
}

func (s *Server) moveCopy(ctx context.Context, w http.ResponseWriter, r *http.Request, cfg store.Config, acc store.Account, prov provider.Provider, from string) {
	dst := r.Header.Get("Destination")
	if dst == "" {
		http.Error(w, "нет заголовка Destination", http.StatusBadRequest)
		return
	}
	if u, err := url.Parse(dst); err == nil {
		dst = u.Path
	}
	dacc, to, _, ok := resolve(cfg, dst)
	if !ok || dacc.ID != acc.ID || to == "/" {
		http.Error(w, "перенос между аккаунтами или в корень не поддерживается", http.StatusBadGateway)
		return
	}
	overwrite := !strings.EqualFold(r.Header.Get("Overwrite"), "F")
	_, derr := prov.Stat(ctx, to)
	existed := derr == nil
	if existed && !overwrite {
		http.Error(w, "назначение существует", http.StatusPreconditionFailed)
		return
	}
	var err error
	if r.Method == "MOVE" {
		err = prov.Move(ctx, from, to, overwrite)
	} else {
		err = prov.Copy(ctx, from, to, overwrite)
	}
	if existed {
		fail(w, err, http.StatusNoContent)
	} else {
		fail(w, err, http.StatusCreated)
	}
}
