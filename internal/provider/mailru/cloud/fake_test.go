package cloud

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"nascloud/internal/httpx"
	"nascloud/internal/provider"
	"nascloud/internal/provider/mailru/mrhash"
	"nascloud/internal/testutil"
)

const (
	fakeLogin    = "user@mail.ru"
	fakePassword = "app-pass"
)

// fakeCloud имитирует приватный API Облака Mail.ru поверх testutil.Mem.
// Загрузка идёт в два шага, как в настоящем протоколе: шард хранит блок по хешу,
// метасервер связывает блок с путём командой add-file.
type fakeCloud struct {
	Mem             *testutil.Mem
	srv             *httptest.Server
	tokens          map[string]bool
	blobs           map[string][]byte
	corrupt         bool             // шард возвращает неверный хеш
	corruptDownload bool             // скачивание отдаёт испорченный первый байт
	dropDownloads   int              // сколько скачиваний оборвать на полпути
	shardPuts       int              // сколько блоков принял шард
	mtimes          map[string]int64 // mtime, который клиент передал в add-file
	issued          int
	kinds           map[string]string // переопределённый тип элемента, например неизвестный
}

// newFake создаёт клиент, подключённый к фейку.
func newFake(t *testing.T) (*Client, *fakeCloud) {
	t.Helper()
	httpx.Backoff = time.Millisecond
	f := &fakeCloud{Mem: testutil.NewMem(), tokens: map[string]bool{}, blobs: map[string][]byte{}, mtimes: map[string]int64{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	c, err := New(fakeLogin, fakePassword, "test/1")
	if err != nil {
		t.Fatal(err)
	}
	c.apiBase = f.srv.URL
	c.dispatchBase = f.srv.URL
	c.oauthURL = f.srv.URL + "/token"
	c.PollDelay = time.Millisecond
	c.pace = httpx.NewPacer(time.Microsecond, time.Millisecond)
	return c, f
}

// revoke отзывает все выданные токены, как после истечения срока.
func (f *fakeCloud) revoke() { f.tokens = map[string]bool{} }

func (f *fakeCloud) authorized(r *http.Request) bool {
	q := r.URL.Query()
	return f.tokens[q.Get("access_token")] || f.tokens[q.Get("token")]
}

// reply пишет JSON-конверт API: поля status и body.
func reply(w http.ResponseWriter, status int, body any) {
	raw, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "body": json.RawMessage(raw), "time": 0})
}

func (f *fakeCloud) handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch {
	case r.URL.Path == "/token":
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "password" || r.Form.Get("username") != fakeLogin || r.Form.Get("password") != fakePassword {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		f.issued++
		tok := "tok-" + strconv.Itoa(f.issued)
		f.tokens[tok] = true
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600,"token_type":"Bearer"}`, tok)
	case r.URL.Path == "/m":
		_, _ = io.WriteString(w, f.srv.URL+"/meta\n")
	case r.URL.Path == "/u":
		_, _ = io.WriteString(w, f.srv.URL+"/shard\n")
	case r.URL.Path == "/d":
		_, _ = io.WriteString(w, f.srv.URL+"/dl\n")
	case r.URL.Path == "/shard" && r.Method == "PUT":
		f.shard(w, r)
	case r.URL.Path == "/meta" && r.Method == "POST":
		f.meta(w, r)
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		f.download(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/m1/"):
		if !f.authorized(r) {
			w.WriteHeader(401)
			return
		}
		f.api(ctx, w, r)
	default:
		w.WriteHeader(404)
	}
}

// shard принимает блок и отвечает его хешем.
func (f *fakeCloud) shard(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(r) {
		w.WriteHeader(401)
		return
	}
	f.shardPuts++
	data, _ := io.ReadAll(r.Body)
	h := mrhash.New()
	_, _ = h.Write(data)
	sum := h.Sum(nil)
	f.blobs[hex.EncodeToString(sum)] = data
	word := hex.EncodeToString(sum)
	if f.corrupt {
		word = strings.Repeat("0", len(word))
	}
	_, _ = io.WriteString(w, word)
}

// binReader читает команды бинарного протокола.
type binReader struct{ r *bytes.Reader }

func (b binReader) u() int64 {
	v, _ := binary.ReadUvarint(b.r)
	return int64(v)
}

func (b binReader) str() string {
	n := b.u()
	if n < 1 {
		return ""
	}
	buf := make([]byte, n-1)
	_, _ = io.ReadFull(b.r, buf)
	_, _ = b.r.ReadByte()
	return string(buf)
}

// meta выполняет команду метасервера и возвращает код результата одним байтом.
func (f *fakeCloud) meta(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(r) {
		w.WriteHeader(401)
		return
	}
	body, _ := io.ReadAll(r.Body)
	br := binReader{bytes.NewReader(body)}
	op := br.u()
	br.u() // ревизия
	code := byte(0)
	switch op {
	case opCreateFolder:
		name := provider.Clean(decodeName(br.str()))
		if e, err := f.Mem.Stat(context.Background(), name); err == nil && e.IsDir {
			code = 4 // папка уже есть
		} else {
			code = mkdirCode(f.Mem.Mkdir(context.Background(), name))
		}
	case opRename:
		src := decodeName(br.str())
		br.u()
		dst := decodeName(br.str())
		code = renameCode(f.Mem.Move(context.Background(), src, dst, false))
	case opAddFile:
		p := decodeName(br.str())
		size := br.u()
		mtime := br.u()
		br.u()
		sum := make([]byte, mrhash.Size)
		_, _ = io.ReadFull(br.r, sum)
		data, ok := f.blobs[hex.EncodeToString(sum)]
		switch {
		case !ok || int64(len(data)) != size:
			code = 1
		case !f.parentIsDir(path.Dir(provider.Clean(p))):
			code = 5
		default:
			f.Mem.Put(provider.Clean(p), data)
			f.mtimes[provider.Clean(p)] = mtime
		}
	}
	_, _ = w.Write([]byte{code})
}

func (f *fakeCloud) parentIsDir(dir string) bool {
	e, err := f.Mem.Stat(context.Background(), dir)
	return err == nil && e.IsDir
}

func mkdirCode(err error) byte {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, provider.ErrExists):
		return 4
	case errors.Is(err, provider.ErrNotFound):
		return 1
	}
	return 254
}

func renameCode(err error) byte {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, provider.ErrExists):
		return 4
	case errors.Is(err, provider.ErrNotFound):
		return 1
	}
	return 254
}

// fakeItem переводит запись Mem в элемент API; у файла добавляет mrhash.
func (f *fakeCloud) fakeItem(e provider.Entry) map[string]any {
	kind := "file"
	if e.IsDir {
		kind = "folder"
	}
	if k, ok := f.kinds[e.Path]; ok {
		kind = k
	}
	mt := e.ModTime.Unix()
	if m, ok := f.mtimes[e.Path]; ok {
		mt = m
	}
	it := map[string]any{"kind": kind, "home": encodeName(e.Path), "size": e.Size, "mtime": mt, "name": e.Name}
	if !e.IsDir {
		data, _ := f.Mem.Get(e.Path)
		it["hash"] = hex.EncodeToString(mrhash.Sum(data))
	}
	return it
}

// api отвечает на методы /api/m1.
func (f *fakeCloud) api(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm() // Form содержит и query, и тело формы
	home := decodeName(r.Form.Get("home"))
	switch r.URL.Path {
	case "/api/m1/user":
		space := map[string]any{"bytes_total": int64(1 << 30), "bytes_used": int64(0)}
		reply(w, 200, map[string]any{"login": fakeLogin, "cloud": map[string]any{"space": space}})
	case "/api/m1/file":
		e, err := f.Mem.Stat(ctx, home)
		if err != nil {
			reply(w, 404, "not found")
			return
		}
		reply(w, 200, f.fakeItem(e))
	case "/api/m1/folder":
		entries, err := f.Mem.List(ctx, home)
		if err != nil {
			reply(w, 404, "not found")
			return
		}
		items := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			items = append(items, f.fakeItem(e))
		}
		reply(w, 200, map[string]any{"list": items})
	case "/api/m1/file/remove":
		if err := f.Mem.Delete(ctx, home); err != nil {
			reply(w, 404, "not found")
			return
		}
		reply(w, 200, "")
	case "/api/m1/file/publish":
		if _, err := f.Mem.Stat(ctx, home); err != nil {
			reply(w, 404, "not found")
			return
		}
		reply(w, 200, "abc123")
	case "/api/m1/file/copy":
		folder := decodeName(r.Form.Get("folder"))
		tmp := path.Join(folder, "copy_"+path.Base(home))
		if err := f.Mem.Copy(ctx, home, tmp, false); err != nil {
			reply(w, 404, "not found")
			return
		}
		reply(w, 200, encodeName(tmp))
	default:
		w.WriteHeader(404)
	}
}

// download отдаёт файл, поддерживая Range.
func (f *fakeCloud) download(w http.ResponseWriter, r *http.Request) {
	if !f.authorized(r) {
		w.WriteHeader(401)
		return
	}
	name := decodeName(strings.TrimPrefix(r.URL.Path, "/dl/"))
	data, ok := f.Mem.Get(provider.Clean(name))
	if !ok {
		w.WriteHeader(404)
		return
	}
	if f.dropDownloads > 0 && len(data) > 1 {
		f.dropDownloads--
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_, _ = fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(data))
			_, _ = buf.Write(data[:len(data)/2])
			_ = buf.Flush()
			_ = conn.Close()
			return
		}
	}
	if f.corruptDownload && len(data) > 0 {
		data = append([]byte{data[0] ^ 0xff}, data[1:]...)
	}
	rng := r.Header.Get("Range")
	if !strings.HasPrefix(rng, "bytes=") {
		_, _ = w.Write(data)
		return
	}
	from, to, _ := strings.Cut(strings.TrimPrefix(rng, "bytes="), "-")
	start, _ := strconv.Atoi(from)
	end := len(data) - 1
	if to != "" {
		end, _ = strconv.Atoi(to)
	}
	w.WriteHeader(206)
	_, _ = w.Write(data[start : end+1])
}
