package testutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nascloud/internal/provider"
)

// YandexFake — имитатор REST API Яндекс.Диска поверх Mem.
type YandexFake struct {
	*httptest.Server
	Mem          *Mem
	Token        string
	Fail503      atomic.Int32 // сколько следующих запросов к API ответят 503
	StorageFull  atomic.Bool  // загрузка вернёт 507
	APICalls     atomic.Int32
	UploadStatus int // 0 → 201; 202 — имитация асинхронной обработки

	// DropPatches — сколько следующих PATCH ответят 200, но свойство не сохранят (имитация асинхронной финализации).
	DropPatches atomic.Int32

	propsMu sync.Mutex
	props   map[string]map[string]any // свойства ресурсов (PATCH custom_properties)

	trashMu sync.Mutex
	trashed []string // пути, перенесённые в корзину (DELETE без permanently=true)
}

// Trashed возвращает пути, которые ушли в корзину.
func (y *YandexFake) Trashed() []string {
	y.trashMu.Lock()
	defer y.trashMu.Unlock()
	return append([]string(nil), y.trashed...)
}

// NewYandexFake запускает имитатор.
func NewYandexFake(m *Mem, token string) *YandexFake {
	y := &YandexFake{Mem: m, Token: token}
	y.Server = httptest.NewServer(http.HandlerFunc(y.handle))
	return y
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, code int, name, msg string) {
	jsonOut(w, code, map[string]string{"error": name, "message": msg, "description": msg})
}

func (y *YandexFake) res(e provider.Entry) map[string]any {
	t := "file"
	if e.IsDir {
		t = "dir"
	}
	r := map[string]any{"name": e.Name, "path": "disk:" + e.Path, "type": t, "modified": e.ModTime.UTC().Format(time.RFC3339)}
	y.propsMu.Lock()
	if p, ok := y.props[e.Path]; ok {
		r["custom_properties"] = p
	}
	y.propsMu.Unlock()
	if !e.IsDir {
		r["size"] = e.Size
		if e.MD5 != "" {
			r["md5"] = e.MD5
		}
	}
	return r
}

func (y *YandexFake) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx := r.Context()
	switch r.URL.Path {
	case "/up": // хранилище загрузки: токен не нужен
		if y.StorageFull.Load() {
			w.WriteHeader(507)
			return
		}
		if r.ContentLength < 0 {
			w.WriteHeader(411)
			return
		}
		data, _ := io.ReadAll(r.Body)
		src := provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(data))), nil }}
		if err := y.Mem.Upload(ctx, q.Get("path"), src); err != nil {
			w.WriteHeader(409)
			return
		}
		if y.UploadStatus != 0 {
			w.WriteHeader(y.UploadStatus)
			return
		}
		w.WriteHeader(201)
		return
	case "/dl":
		e, err := y.Mem.Stat(ctx, q.Get("path"))
		if err != nil {
			w.WriteHeader(404)
			return
		}
		start, length, status := int64(0), e.Size, 200
		if h := r.Header.Get("Range"); strings.HasPrefix(h, "bytes=") {
			spec := strings.Split(strings.TrimPrefix(h, "bytes="), "-")
			start, _ = strconv.ParseInt(spec[0], 10, 64)
			end := e.Size - 1
			if len(spec) > 1 && spec[1] != "" {
				end, _ = strconv.ParseInt(spec[1], 10, 64)
			}
			if end >= e.Size {
				end = e.Size - 1
			}
			length, status = end-start+1, 206
		}
		rc, _ := y.Mem.Download(ctx, q.Get("path"), start, length)
		defer rc.Close()
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(status)
		_, _ = io.Copy(w, rc)
		return
	}

	y.APICalls.Add(1)
	if r.Header.Get("Authorization") != "OAuth "+y.Token {
		apiErr(w, 401, "UnauthorizedError", "Не авторизован")
		return
	}
	if y.Fail503.Load() > 0 {
		y.Fail503.Add(-1)
		w.WriteHeader(503)
		return
	}
	path := q.Get("path")
	link := func(p string, extra url.Values) string {
		v := url.Values{"path": {path}}
		for k, vv := range extra {
			v[k] = vv
		}
		return y.URL + p + "?" + v.Encode()
	}
	switch {
	case r.URL.Path == "/v1/disk/" && r.Method == "GET":
		jsonOut(w, 200, map[string]any{"total_space": 1000, "used_space": 10, "user": map[string]string{"login": "yauser"}})

	case r.URL.Path == "/v1/disk/resources" && r.Method == "GET":
		e, err := y.Mem.Stat(ctx, path)
		if err != nil {
			apiErr(w, 404, "DiskNotFoundError", "Ресурс не найден")
			return
		}
		out := y.res(e)
		if e.IsDir && q.Get("limit") != "0" {
			list, _ := y.Mem.List(ctx, path)
			limit, _ := strconv.Atoi(q.Get("limit"))
			if limit <= 0 || limit > 2 { // мелкие страницы проверяют постраничную загрузку
				limit = 2
			}
			offset, _ := strconv.Atoi(q.Get("offset"))
			var items []map[string]any
			for i := offset; i < len(list) && i < offset+limit; i++ {
				items = append(items, y.res(list[i]))
			}
			out["_embedded"] = map[string]any{"items": items, "total": len(list), "limit": limit, "offset": offset}
		}
		jsonOut(w, 200, out)

	case r.URL.Path == "/v1/disk/resources" && r.Method == "PATCH":
		var body struct {
			CustomProperties map[string]any `json:"custom_properties"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			apiErr(w, 400, "BadRequest", err.Error())
			return
		}
		if y.DropPatches.Load() > 0 {
			y.DropPatches.Add(-1)
			w.WriteHeader(200)
			return
		}
		y.propsMu.Lock()
		if y.props == nil {
			y.props = map[string]map[string]any{}
		}
		y.props[q.Get("path")] = body.CustomProperties
		y.propsMu.Unlock()
		w.WriteHeader(200)

	case r.URL.Path == "/v1/disk/resources" && r.Method == "PUT":
		if e, err := y.Mem.Stat(ctx, path); err == nil && e.IsDir {
			apiErr(w, 409, "DiskPathPointsToExistentDirectoryError", "Папка уже существует")
			return
		}
		if err := y.Mem.Mkdir(ctx, path); err != nil {
			if errors.Is(err, provider.ErrNotFound) {
				apiErr(w, 409, "DiskPathDoesntExistsError", "Родитель не найден")
			} else {
				apiErr(w, 409, "DiskPathPointsToExistentFileError", "Файл с таким именем уже есть")
			}
			return
		}
		jsonOut(w, 201, map[string]string{"href": link("/v1/disk/resources", nil), "method": "GET"})

	case r.URL.Path == "/v1/disk/resources/upload" && r.Method == "GET":
		parent := provider.Join(path, "..")
		if _, err := y.Mem.Stat(ctx, parent); err != nil {
			apiErr(w, 409, "DiskPathDoesntExistsError", "Родитель не найден")
			return
		}
		jsonOut(w, 200, map[string]any{"href": link("/up", nil), "method": "PUT", "templated": false})

	case r.URL.Path == "/v1/disk/resources/download" && r.Method == "GET":
		if _, err := y.Mem.Stat(ctx, path); err != nil {
			apiErr(w, 404, "DiskNotFoundError", "Ресурс не найден")
			return
		}
		jsonOut(w, 200, map[string]any{"href": link("/dl", nil), "method": "GET", "templated": false})

	case r.URL.Path == "/v1/disk/resources" && r.Method == "DELETE":
		e, err := y.Mem.Stat(ctx, path)
		if err != nil {
			apiErr(w, 404, "DiskNotFoundError", "Ресурс не найден")
			return
		}
		_ = y.Mem.Delete(ctx, path)
		if q.Get("permanently") != "true" {
			y.trashMu.Lock()
			y.trashed = append(y.trashed, path)
			y.trashMu.Unlock()
		}
		if e.IsDir {
			jsonOut(w, 202, map[string]string{"href": y.URL + "/v1/disk/operations/1", "method": "GET"})
		} else {
			w.WriteHeader(204)
		}

	case r.URL.Path == "/v1/disk/trash/resources" && r.Method == "DELETE": // очистка корзины, как в rclone CleanUp
		y.trashMu.Lock()
		y.trashed = nil
		y.trashMu.Unlock()
		w.WriteHeader(204)

	case (r.URL.Path == "/v1/disk/resources/move" || r.URL.Path == "/v1/disk/resources/copy") && r.Method == "POST":
		from := q.Get("from")
		ow := q.Get("overwrite") == "true"
		var err error
		if strings.HasSuffix(r.URL.Path, "/move") {
			err = y.Mem.Move(ctx, from, path, ow)
		} else {
			err = y.Mem.Copy(ctx, from, path, ow)
		}
		switch {
		case errors.Is(err, provider.ErrNotFound):
			apiErr(w, 404, "DiskNotFoundError", "Ресурс не найден")
		case errors.Is(err, provider.ErrExists):
			apiErr(w, 409, "DiskResourceAlreadyExistsError", "Ресурс уже существует")
		case err != nil:
			apiErr(w, 500, "Err", err.Error())
		default:
			jsonOut(w, 202, map[string]string{"href": y.URL + "/v1/disk/operations/2", "method": "GET"})
		}

	case strings.HasPrefix(r.URL.Path, "/v1/disk/operations/"):
		jsonOut(w, 200, map[string]string{"status": "success"})

	default:
		apiErr(w, 404, "NotFound", fmt.Sprintf("неизвестный маршрут %s %s", r.Method, r.URL.Path))
	}
}
