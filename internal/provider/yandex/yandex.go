// Package yandex — драйвер Яндекс.Диска через официальный REST API
// (https://yandex.ru/dev/disk-api). Загрузка и скачивание идут напрямую на
// серверы хранилища по ссылкам, выданным API; OAuth-токен отправляется только
// на хост API.
package yandex

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nascloud/internal/auth"
	"nascloud/internal/httpx"
	"nascloud/internal/provider"
	"nascloud/internal/provider/webdav"
)

// DefaultWebDAV — адрес WebDAV-сервера Яндекс.Диска.
const DefaultWebDAV = "https://webdav.yandex.ru"

// userAgentTemplate — строка официального клиента Яндекс.Диска (как у rclone с spoof_ua).
const userAgentTemplate = `Yandex.Disk {"os":"windows","dtype":"ydisk3","vsn":"3.2.37.4977","id":"6BD01244C7A94456BBCEE7EEC990AEAD","id2":"0F370CD40C594A4783BC839C846B999C","session_id":"%s"}`

// spoofUA возвращает User-Agent клиента Яндекс.Диска со случайным session_id.
func spoofUA() string { return fmt.Sprintf(userAgentTemplate, auth.RandomToken(64)) }

// DefaultBase — адрес API.
const DefaultBase = "https://cloud-api.yandex.net"

// Client — драйвер Яндекс.Диска.
type Client struct {
	base  *url.URL
	token string
	api   *http.Client // короткие запросы к API
	xfer  *http.Client // передача файлов без общего таймаута
	pace  *httpx.Pacer // пауза между запросами и повторы при 429/5xx

	// UserAgent — значение заголовка User-Agent; New задаёт имитацию клиента Яндекс.Диска.
	UserAgent string

	// PollDelay — пауза при ожидании асинхронных операций и подтверждения загрузки.
	PollDelay time.Duration
}

// Descriptor описывает вендорскую сборку для Яндекс.Диска.
func Descriptor() provider.Descriptor {
	d := provider.Descriptor{
		Kind:  "yandex",
		Title: "Яндекс.Диск",
		Fields: []provider.Field{
			{Key: "client_id", Label: "ID приложения", Placeholder: "32 символа из карточки приложения", Global: true,
				Help: "Один раз сохраняется для всех аккаунтов. Нужен только для кнопки «Получить токен» — она откроет страницу разрешения доступа Яндекса"},
			{Key: "token", Label: "OAuth-токен", Secret: true, Placeholder: "y0_AgAAAA…",
				Help: "Токен с правами cloud_api:disk.read, disk.write и disk.info"},
			{Key: "transport", Label: "Тип подключения", Default: "api",
				Options: []provider.Option{{Value: "api", Label: "REST API (рекомендуется)"}, {Value: "webdav", Label: "WebDAV (webdav.yandex.ru)"}},
				Help:    "REST API — родной протокол Яндекс.Диска, обычно быстрее. WebDAV — запасной вариант: у Яндекса он медленнее и ограничивает размер файла; переключайтесь, только если API недоступен. Тот же OAuth-токен подходит для обоих."},
			{Key: "user_agent", Label: "User-Agent", Placeholder: "по умолчанию: клиент Яндекс.Диска", Help: "Необязательно. Значение заголовка User-Agent для всех запросов этого аккаунта. Пусто — имитация клиента Яндекс.Диска (как у rclone). Это ваш выбор: некоторые облака по-разному относятся к разным клиентам, но выдавать себя за чужую программу может нарушать правила сервиса."},
		},
		AuthURL: "https://oauth.yandex.ru/authorize?response_type=token&client_id={client_id}",
		Help: "Как получить токен: 1) откройте https://oauth.yandex.ru/client/new и создайте приложение «Веб-сервисы» " +
			"с Callback URL https://oauth.yandex.ru/verification_code. 2) В разделе «Доступ к данным» добавьте права " +
			"cloud_api:disk.read, cloud_api:disk.write, cloud_api:disk.info (при желании — cloud_api:disk.app_folder). " +
			"3) Вставьте ID приложения и нажмите «Получить токен» — разрешите доступ, токен будет показан на странице. Вставьте его сюда. " +
			"Для каждого аккаунта Яндекса токен свой: создавайте отдельные аккаунты в списке.",
		Steps: []provider.Step{
			{Title: "Создайте приложение", Text: "Откройте https://oauth.yandex.ru/client/new и создайте приложение «Веб-сервисы». Callback URL: https://oauth.yandex.ru/verification_code."},
			{Title: "Добавьте права", Text: "В разделе «Доступ к данным» укажите cloud_api:disk.read (чтение), cloud_api:disk.write (запись) и cloud_api:disk.info (информация о диске). Для доступа только к папке приложения достаточно cloud_api:disk.app_folder."},
			{Title: "Получите токен", Text: "Вставьте ID приложения в поле выше и нажмите «Получить токен». Откроется страница Яндекса под нужным аккаунтом — разрешите доступ."},
			{Title: "Скопируйте токен", Text: "Токен будет показан на странице Яндекса. Скопируйте его и вставьте в поле «OAuth-токен»."},
			{Title: "Несколько аккаунтов", Text: "Для каждого аккаунта Яндекса свой токен: добавляйте их отдельными записями в списке."},
		},
		New: func(s map[string]string) (provider.Provider, error) {
			ua := strings.TrimSpace(s["user_agent"])
			if ua == "" {
				ua = spoofUA()
			}
			if s["transport"] == "webdav" {
				if strings.TrimSpace(s["token"]) == "" {
					return nil, errors.New("не задан OAuth-токен")
				}
				base := s["base_url"]
				if base == "" {
					base = DefaultWebDAV
				}
				return webdav.New(webdav.Config{BaseURL: base, AuthHeader: "OAuth " + strings.TrimSpace(s["token"]), UserAgent: ua})
			}
			c, err := New(s["token"], s["base_url"])
			if err != nil {
				return nil, err
			}
			c.UserAgent = ua
			return c, nil
		},
	}
	d.Fields = append(d.Fields, provider.CommonFields()...)
	return d
}

// New создаёт клиент. base можно оставить пустым.
func New(token, base string) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("не задан OAuth-токен")
	}
	if base == "" {
		base = DefaultBase
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:      u,
		token:     strings.TrimSpace(token),
		api:       &http.Client{Timeout: 60 * time.Second},
		xfer:      &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 10 * time.Minute, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second}},
		pace:      httpx.NewPacer(10*time.Millisecond, 2*time.Second),
		PollDelay: 2 * time.Second,
		UserAgent: spoofUA(),
	}, nil
}

func (c *Client) apiURL(p string, q url.Values) string {
	u := *c.base
	u.Path = "/v1/disk" + p
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) auth(req *http.Request) {
	req.Header.Set("User-Agent", c.UserAgent)
	if req.URL.Host == c.base.Host {
		req.Header.Set("Authorization", "OAuth "+c.token)
	}
}

type apiError struct {
	Message     string `json:"message"`
	Description string `json:"description"`
	Name        string `json:"error"`
}

func (c *Client) mapErr(err error) error {
	var se *httpx.StatusError
	if !errors.As(err, &se) {
		return err
	}
	var ae apiError
	_ = json.Unmarshal([]byte(se.Body), &ae)
	detail := ae.Message
	if detail == "" {
		detail = ae.Description
	}
	if detail == "" {
		detail = strings.TrimSpace(se.Body)
	}
	switch {
	case se.Code == 401:
		return fmt.Errorf("%w (%s)", provider.ErrAuth, detail)
	case se.Code == 403:
		return fmt.Errorf("доступ запрещён: %s", detail)
	case se.Code == 404:
		return fmt.Errorf("%w: %s", provider.ErrNotFound, detail)
	case se.Code == 507:
		return provider.ErrInsufficientStorage
	case se.Code == 413:
		return fmt.Errorf("файл слишком большой для тарифа Яндекс.Диска (без подписки Яндекс 360 лимит 1 ГБ): используйте разбиение архива на части: %s", detail)
	case se.Code == 409 && ae.Name == "DiskPathPointsToExistentDirectoryError":
		return provider.ErrExists
	case se.Code == 409 && ae.Name == "DiskResourceAlreadyExistsError":
		return provider.ErrExists
	case se.Code == 409 && ae.Name == "DiskPathDoesntExistsError":
		return fmt.Errorf("%w: родительская папка отсутствует", provider.ErrNotFound)
	}
	return fmt.Errorf("Яндекс.Диск: HTTP %d: %s", se.Code, detail)
}

// doOnce — один запрос к API без повторов; ошибки не преобразуются.
func (c *Client) doOnce(ctx context.Context, method, rawURL string, out any, ok ...int) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return 0, err
	}
	c.auth(req)
	req.Header.Set("Accept", "application/json")
	resp, err := c.api.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := httpx.ExpectStatus(method+" "+req.URL.Path, resp, ok...); err != nil {
		return resp.StatusCode, err
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return resp.StatusCode, err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, nil
}

func (c *Client) do(ctx context.Context, method, rawURL string, out any, ok ...int) (int, error) {
	var status int
	err := c.pace.Do(ctx, 10, func() error {
		var err error
		status, err = c.doOnce(ctx, method, rawURL, out, ok...)
		return err
	})
	if err != nil {
		return status, c.mapErr(err)
	}
	return status, nil
}

type resource struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	MD5      string `json:"md5"`
	// CustomProperties — свойства, в том числе rclone_modified с исходным временем изменения.
	CustomProperties map[string]any `json:"custom_properties"`
}

func (r resource) entry() provider.Entry {
	p := strings.TrimPrefix(r.Path, "disk:")
	e := provider.Entry{Name: r.Name, Path: provider.Clean(p), IsDir: r.Type == "dir", Size: r.Size, MD5: r.MD5}
	// Свойство rclone_modified точнее серверного времени; без него берётся время загрузки.
	if s, ok := r.CustomProperties["rclone_modified"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			e.ModTime = t
			return e
		}
	}
	if t, err := time.Parse(time.RFC3339, r.Modified); err == nil {
		e.ModTime = t
	}
	return e
}

// Check проверяет токен и возвращает сведения о диске.
func (c *Client) Check(ctx context.Context) (provider.AccountInfo, error) {
	var d struct {
		Total int64 `json:"total_space"`
		Used  int64 `json:"used_space"`
		User  struct {
			Login string `json:"login"`
			Name  string `json:"display_name"`
		} `json:"user"`
	}
	if _, err := c.do(ctx, "GET", c.apiURL("/", nil), &d, 200); err != nil {
		return provider.AccountInfo{}, err
	}
	title := d.User.Login
	if title == "" {
		title = d.User.Name
	}
	return provider.AccountInfo{Title: title, Total: d.Total, Used: d.Used}, nil
}

// List возвращает содержимое папки (с постраничной загрузкой).
func (c *Client) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	dir = provider.Clean(dir)
	var out []provider.Entry
	for offset := 0; ; {
		var r struct {
			Embedded struct {
				Items []resource `json:"items"`
				Total int        `json:"total"`
			} `json:"_embedded"`
		}
		q := url.Values{"path": {dir}, "limit": {"1000"}, "offset": {strconv.Itoa(offset)}}
		if _, err := c.do(ctx, "GET", c.apiURL("/resources", q), &r, 200); err != nil {
			return nil, err
		}
		for _, it := range r.Embedded.Items {
			out = append(out, it.entry())
		}
		offset += len(r.Embedded.Items)
		if len(r.Embedded.Items) == 0 || offset >= r.Embedded.Total {
			return out, nil
		}
	}
}

// Stat возвращает сведения о файле или папке.
func (c *Client) Stat(ctx context.Context, p string) (provider.Entry, error) {
	var r resource
	q := url.Values{"path": {provider.Clean(p)}, "limit": {"0"}}
	if _, err := c.do(ctx, "GET", c.apiURL("/resources", q), &r, 200); err != nil {
		return provider.Entry{}, err
	}
	return r.entry(), nil
}

// Mkdir создаёт папку; существующая папка — не ошибка.
func (c *Client) Mkdir(ctx context.Context, p string) error {
	q := url.Values{"path": {provider.Clean(p)}}
	_, err := c.do(ctx, "PUT", c.apiURL("/resources", q), nil, 201)
	if errors.Is(err, provider.ErrExists) {
		return nil
	}
	return err
}

// Upload загружает файл (с перезаписью) и проверяет размер.
func (c *Client) Upload(ctx context.Context, p string, src provider.Source) error {
	p = provider.Clean(p)
	var sum string // MD5 тела последней удачной попытки
	err := c.pace.Do(ctx, 10, func() error {
		var link struct {
			Href string `json:"href"`
		}
		q := url.Values{"path": {p}, "overwrite": {"true"}}
		if _, err := c.doOnce(ctx, "GET", c.apiURL("/resources/upload", q), &link, 200); err != nil {
			return err
		}
		if link.Href == "" {
			return errors.New("Яндекс.Диск не вернул ссылку для загрузки")
		}
		body, err := src.Open()
		if err != nil {
			return err
		}
		h := md5.New()
		// Закрытие тела остаётся за http.Client; TeeReader считает MD5 тех же байтов, что уходят на сервер.
		rc := struct {
			io.Reader
			io.Closer
		}{io.TeeReader(body, h), body}
		req, err := http.NewRequestWithContext(ctx, "PUT", link.Href, rc)
		if err != nil {
			body.Close()
			return err
		}
		req.ContentLength = src.Size
		if src.Size == 0 {
			body.Close()
			req.Body = http.NoBody
		}
		c.auth(req)
		resp, err := c.xfer.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := httpx.ExpectStatus("PUT upload", resp, 200, 201, 202); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		sum = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	if err != nil {
		return c.mapErr(err)
	}
	if err := provider.VerifySize(ctx, c, p, src.Size, 8, c.PollDelay); err != nil {
		return err
	}
	if err := c.verifyMD5(ctx, p, sum); err != nil {
		return err
	}
	if src.ModTime.IsZero() {
		return nil
	}
	return c.setModTime(ctx, p, src.ModTime)
}

// modTimeTries — сколько раз записать время и проверить, что Яндекс его сохранил.
const modTimeTries = 3

// setModTime сохраняет время изменения в свойстве rclone_modified и проверяет запись.
// Яндекс финализирует загрузку асинхронно, поэтому свойство, записанное сразу, может не сохраниться.
// После записи ресурс читается заново. При расхождении запись повторяется, пауза растёт с каждой попыткой.
// Ошибка чтения не прерывает проверку, а повторяется вместе с записью.
func (c *Client) setModTime(ctx context.Context, p string, t time.Time) error {
	var stored time.Time
	for try := 1; ; try++ {
		if err := c.patchModTime(ctx, p, t); err != nil {
			return err
		}
		if e, err := c.Stat(ctx, p); err == nil {
			if e.ModTime.Equal(t) {
				return nil
			}
			stored = e.ModTime
		}
		if try == modTimeTries {
			return fmt.Errorf("Яндекс.Диск не сохранил время изменения за %d попытки: в облаке %s, ожидалось %s",
				modTimeTries, stored.Format(time.RFC3339Nano), t.Format(time.RFC3339Nano))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.PollDelay * time.Duration(try)):
		}
	}
}

// patchModTime записывает свойство rclone_modified (PATCH /resources).
func (c *Client) patchModTime(ctx context.Context, p string, t time.Time) error {
	body, err := json.Marshal(map[string]any{"custom_properties": map[string]string{"rclone_modified": t.Format(time.RFC3339Nano)}})
	if err != nil {
		return err
	}
	err = c.pace.Do(ctx, 10, func() error {
		req, err := http.NewRequestWithContext(ctx, "PATCH", c.apiURL("/resources", url.Values{"path": {p}}), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		c.auth(req)
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := httpx.ExpectStatus("PATCH resources", resp, 200); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	})
	return c.mapErr(err)
}

// verifyMD5 сверяет MD5 файла на диске с MD5 загруженных байтов.
// Пустой MD5 от сервера — проверка пропускается: Яндекс может не вернуть его сразу.
func (c *Client) verifyMD5(ctx context.Context, p, want string) error {
	e, err := c.Stat(ctx, p)
	if err != nil {
		return err
	}
	if e.MD5 != "" && !strings.EqualFold(e.MD5, want) {
		return fmt.Errorf("контрольная сумма на диске %s, ожидалось %s: загрузка повреждена", e.MD5, want)
	}
	return nil
}

// Download открывает файл на чтение с указанного смещения.
func (c *Client) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	var link struct {
		Href string `json:"href"`
	}
	if _, err := c.do(ctx, "GET", c.apiURL("/resources/download", url.Values{"path": {provider.Clean(p)}}), &link, 200); err != nil {
		return nil, err
	}
	var rc io.ReadCloser
	err := c.pace.Do(ctx, 10, func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", link.Href, nil)
		if err != nil {
			return err
		}
		c.auth(req)
		if offset > 0 || length >= 0 {
			rng := fmt.Sprintf("bytes=%d-", offset)
			if length >= 0 {
				rng = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
			}
			req.Header.Set("Range", rng)
		}
		resp, err := c.xfer.Do(req)
		if err != nil {
			return err
		}
		if err := httpx.ExpectStatus("GET download", resp, 200, 206); err != nil {
			return err
		}
		rc = resp.Body
		if resp.StatusCode == 200 && offset > 0 { // сервер проигнорировал Range
			if err := provider.SkipBytes(rc, offset); err != nil {
				rc.Close()
				return err
			}
		}
		if resp.StatusCode == 200 && length >= 0 {
			rc = provider.Limit(rc, length)
		}
		return nil
	})
	if err != nil {
		return nil, c.mapErr(err)
	}
	return rc, nil
}

func (c *Client) waitOp(ctx context.Context, href string) error {
	deadline := time.Now().Add(15 * time.Minute)
	for {
		var op struct {
			Status string `json:"status"`
		}
		if _, err := c.do(ctx, "GET", href, &op, 200); err != nil {
			return err
		}
		switch op.Status {
		case "success":
			return nil
		case "failed":
			return errors.New("операция на Яндекс.Диске завершилась ошибкой")
		}
		if time.Now().After(deadline) {
			return errors.New("операция на Яндекс.Диске не завершилась за 15 минут")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.PollDelay):
		}
	}
}

func (c *Client) async(ctx context.Context, method, rawURL string, ok ...int) error {
	var link struct {
		Href string `json:"href"`
	}
	status, err := c.do(ctx, method, rawURL, &link, ok...)
	if err != nil {
		return err
	}
	if status == 202 && strings.Contains(link.Href, "/operations/") {
		return c.waitOp(ctx, link.Href)
	}
	return nil
}

// Delete удаляет файл или папку без помещения в корзину.
func (c *Client) Delete(ctx context.Context, p string) error {
	q := url.Values{"path": {provider.Clean(p)}, "permanently": {"true"}}
	return c.async(ctx, "DELETE", c.apiURL("/resources", q), 202, 204)
}

// Move перемещает или переименовывает ресурс.
func (c *Client) Move(ctx context.Context, from, to string, overwrite bool) error {
	q := url.Values{"from": {provider.Clean(from)}, "path": {provider.Clean(to)}, "overwrite": {strconv.FormatBool(overwrite)}}
	return c.async(ctx, "POST", c.apiURL("/resources/move", q), 201, 202)
}

// Copy копирует ресурс на стороне Яндекса.
func (c *Client) Copy(ctx context.Context, from, to string, overwrite bool) error {
	q := url.Values{"from": {provider.Clean(from)}, "path": {provider.Clean(to)}, "overwrite": {strconv.FormatBool(overwrite)}}
	return c.async(ctx, "POST", c.apiURL("/resources/copy", q), 201, 202)
}

// ReportsMD5: REST API Яндекс.Диска возвращает MD5 файла.
func (c *Client) ReportsMD5() bool { return true }

// Publish открывает публичный доступ к файлу или папке и возвращает ссылку.
func (c *Client) Publish(ctx context.Context, p string) (string, error) {
	p = provider.Clean(p)
	if _, err := c.do(ctx, "PUT", c.apiURL("/resources/publish", url.Values{"path": {p}}), nil, 200); err != nil {
		return "", err
	}
	var r struct {
		PublicURL string `json:"public_url"`
	}
	if _, err := c.do(ctx, "GET", c.apiURL("/resources", url.Values{"path": {p}, "fields": {"public_url"}}), &r, 200); err != nil {
		return "", err
	}
	if r.PublicURL == "" {
		return "", errors.New("Яндекс.Диск не вернул публичную ссылку")
	}
	return r.PublicURL, nil
}

// Unpublish закрывает публичный доступ.
func (c *Client) Unpublish(ctx context.Context, p string) error {
	_, err := c.do(ctx, "PUT", c.apiURL("/resources/unpublish", url.Values{"path": {provider.Clean(p)}}), nil, 200)
	return err
}

// Trash переносит файл или папку в корзину Яндекс.Диска (восстанавливается оттуда).
func (c *Client) Trash(ctx context.Context, p string) error {
	return c.async(ctx, "DELETE", c.apiURL("/resources", url.Values{"path": {provider.Clean(p)}}), 202, 204)
}

var _ provider.Publisher = (*Client)(nil)
var _ provider.Trasher = (*Client)(nil)

// EmptyTrash очищает корзину Яндекс.Диска (как CleanUp в rclone).
func (c *Client) EmptyTrash(ctx context.Context) error {
	return c.async(ctx, "DELETE", c.apiURL("/trash/resources", nil), 202, 204)
}

var _ provider.TrashEmptier = (*Client)(nil)
