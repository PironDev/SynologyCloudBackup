// Package webdav — универсальный WebDAV-клиент (используется для Облака Mail.ru).
package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"nascloud/internal/httpx"
	"nascloud/internal/provider"
)

// Config — параметры подключения.
type Config struct {
	BaseURL  string
	User     string
	Password string
	// AuthHeader — готовое значение заголовка Authorization (например «OAuth …»); если задано, логин и пароль не используются.
	AuthHeader string
	// UserAgent — значение заголовка User-Agent (пусто — стандартное).
	UserAgent string
}

// Client — WebDAV-клиент.
type Client struct {
	base      *url.URL
	user, pw  string
	authHdr   string
	ua        string
	api, xfer *http.Client

	// pace — пауза между запросами и повторы при 429/5xx.
	pace *httpx.Pacer

	// VerifyDelay — пауза между проверками размера после загрузки.
	VerifyDelay time.Duration
}

// New создаёт клиента.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("неверный адрес WebDAV %q", cfg.BaseURL)
	}
	return &Client{
		base: u, user: cfg.User, pw: cfg.Password, authHdr: cfg.AuthHeader, ua: cfg.UserAgent,
		api:         &http.Client{Timeout: 90 * time.Second},
		xfer:        &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 15 * time.Minute, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second}},
		pace:        httpx.NewPacer(10*time.Millisecond, 2*time.Second),
		VerifyDelay: 2 * time.Second,
	}, nil
}

func (c *Client) urlFor(p string) string {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + provider.Clean(p)
	u.RawPath = ""
	return u.String()
}

func (c *Client) newReq(ctx context.Context, method, p string, body io.Reader, hdr map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.urlFor(p), body)
	if err != nil {
		return nil, err
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	if c.authHdr != "" {
		req.Header.Set("Authorization", c.authHdr)
	} else {
		req.SetBasicAuth(c.user, c.pw)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req, nil
}

func (c *Client) mapErr(err error) error {
	var se *httpx.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code {
	case 401:
		return fmt.Errorf("%w (WebDAV: неверный логин или пароль внешнего приложения)", provider.ErrAuth)
	case 403:
		return fmt.Errorf("доступ запрещён: %s", strings.TrimSpace(se.Body))
	case 404:
		return provider.ErrNotFound
	case 409:
		return fmt.Errorf("%w: родительская папка отсутствует", provider.ErrNotFound)
	case 412:
		return provider.ErrExists
	case 507:
		return provider.ErrInsufficientStorage
	}
	return err
}

type multistatus struct {
	Responses []struct {
		Href      string `xml:"href"`
		Propstats []struct {
			Status string `xml:"status"`
			Prop   struct {
				ResourceType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
				Length   string `xml:"getcontentlength"`
				Modified string `xml:"getlastmodified"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getcontentlength/><D:getlastmodified/></D:prop></D:propfind>`

func (c *Client) hrefToPath(h string) string {
	if u, err := url.Parse(h); err == nil {
		h = u.Path
	} else if s, err := url.PathUnescape(h); err == nil {
		h = s
	}
	h = strings.TrimPrefix(h, strings.TrimRight(c.base.Path, "/"))
	return provider.Clean(h)
}

func (c *Client) propfind(ctx context.Context, p, depth string) ([]provider.Entry, error) {
	var entries []provider.Entry
	err := c.pace.Do(ctx, 10, func() error {
		entries = nil
		req, err := c.newReq(ctx, "PROPFIND", p, strings.NewReader(propfindBody),
			map[string]string{"Depth": depth, "Content-Type": "application/xml; charset=utf-8"})
		if err != nil {
			return err
		}
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := httpx.ExpectStatus("PROPFIND "+p, resp, 207, 200); err != nil {
			return err
		}
		var ms multistatus
		if err := xml.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&ms); err != nil {
			return fmt.Errorf("разбор ответа WebDAV: %w", err)
		}
		for _, r := range ms.Responses {
			e := provider.Entry{Path: c.hrefToPath(r.Href)}
			e.Name = path.Base(e.Path)
			for _, ps := range r.Propstats {
				if ps.Status != "" && !strings.Contains(ps.Status, " 200") {
					continue
				}
				e.IsDir = ps.Prop.ResourceType.Collection != nil
				if n, err := strconv.ParseInt(strings.TrimSpace(ps.Prop.Length), 10, 64); err == nil {
					e.Size = n
				}
				if t, err := http.ParseTime(strings.TrimSpace(ps.Prop.Modified)); err == nil {
					e.ModTime = t
				}
			}
			entries = append(entries, e)
		}
		return nil
	})
	return entries, c.mapErr(err)
}

// Check проверяет логин и пароль.
func (c *Client) Check(ctx context.Context) (provider.AccountInfo, error) {
	if _, err := c.propfind(ctx, "/", "0"); err != nil {
		return provider.AccountInfo{}, err
	}
	info := provider.AccountInfo{Title: c.user}
	if used, avail, ok := c.quota(ctx); ok {
		info.Used, info.Total = used, used+avail
	}
	return info, nil
}

const quotaBody = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:quota-available-bytes/><D:quota-used-bytes/></D:prop></D:propfind>`

// quota запрашивает объём по RFC 4331. Не все серверы его отдают — тогда ok=false.
func (c *Client) quota(ctx context.Context) (used, avail int64, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := c.newReq(ctx, "PROPFIND", "/", strings.NewReader(quotaBody),
		map[string]string{"Depth": "0", "Content-Type": "application/xml; charset=utf-8"})
	if err != nil {
		return
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 207 && resp.StatusCode != 200 {
		return
	}
	var ms struct {
		Responses []struct {
			Propstats []struct {
				Status string `xml:"status"`
				Prop   struct {
					Avail string `xml:"quota-available-bytes"`
					Used  string `xml:"quota-used-bytes"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ms) != nil {
		return
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstats {
			if ps.Status != "" && !strings.Contains(ps.Status, " 200") {
				continue
			}
			a, ea := strconv.ParseInt(strings.TrimSpace(ps.Prop.Avail), 10, 64)
			u, eu := strconv.ParseInt(strings.TrimSpace(ps.Prop.Used), 10, 64)
			if ea == nil && eu == nil && a >= 0 && u >= 0 {
				return u, a, true
			}
		}
	}
	return
}

// List возвращает содержимое папки.
func (c *Client) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	dir = provider.Clean(dir)
	all, err := c.propfind(ctx, dir, "1")
	if err != nil {
		return nil, err
	}
	var out []provider.Entry
	for _, e := range all {
		if e.Path == dir {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Stat возвращает сведения о ресурсе.
func (c *Client) Stat(ctx context.Context, p string) (provider.Entry, error) {
	p = provider.Clean(p)
	all, err := c.propfind(ctx, p, "0")
	if err != nil {
		return provider.Entry{}, err
	}
	if len(all) == 0 {
		return provider.Entry{}, provider.ErrNotFound
	}
	e := all[0]
	e.Path = p
	e.Name = path.Base(p)
	return e, nil
}

// Mkdir создаёт папку; существующая — не ошибка.
func (c *Client) Mkdir(ctx context.Context, p string) error {
	err := c.pace.Do(ctx, 10, func() error {
		req, err := c.newReq(ctx, "MKCOL", p, nil, nil)
		if err != nil {
			return err
		}
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return httpx.ExpectStatus("MKCOL "+p, resp, 201, 200, 405)
	})
	return c.mapErr(err)
}

// Upload загружает файл и проверяет размер.
func (c *Client) Upload(ctx context.Context, p string, src provider.Source) error {
	p = provider.Clean(p)
	err := c.pace.Do(ctx, 10, func() error {
		body, err := src.Open()
		if err != nil {
			return err
		}
		req, err := c.newReq(ctx, "PUT", p, body, nil)
		if err != nil {
			body.Close()
			return err
		}
		req.ContentLength = src.Size
		if src.Size == 0 {
			body.Close()
			req.Body = http.NoBody
		}
		resp, err := c.xfer.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := httpx.ExpectStatus("PUT "+p, resp, 200, 201, 204); err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	})
	if err != nil {
		return c.mapErr(err)
	}
	return provider.VerifySize(ctx, c, p, src.Size, 4, c.VerifyDelay)
}

// Download читает файл с указанного смещения.
func (c *Client) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := c.pace.Do(ctx, 10, func() error {
		hdr := map[string]string{}
		if offset > 0 || length >= 0 {
			rng := fmt.Sprintf("bytes=%d-", offset)
			if length >= 0 {
				rng = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
			}
			hdr["Range"] = rng
		}
		req, err := c.newReq(ctx, "GET", p, nil, hdr)
		if err != nil {
			return err
		}
		resp, err := c.xfer.Do(req)
		if err != nil {
			return err
		}
		if err := httpx.ExpectStatus("GET "+p, resp, 200, 206); err != nil {
			return err
		}
		rc = resp.Body
		if resp.StatusCode == 200 && offset > 0 {
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

// Delete удаляет ресурс рекурсивно.
func (c *Client) Delete(ctx context.Context, p string) error {
	err := c.pace.Do(ctx, 10, func() error {
		req, err := c.newReq(ctx, "DELETE", p, nil, nil)
		if err != nil {
			return err
		}
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return httpx.ExpectStatus("DELETE "+p, resp, 200, 202, 204)
	})
	return c.mapErr(err)
}

func (c *Client) transfer(ctx context.Context, method, from, to string, overwrite bool) error {
	ow := "F"
	if overwrite {
		ow = "T"
	}
	dest := c.urlFor(to)
	err := c.pace.Do(ctx, 10, func() error {
		req, err := c.newReq(ctx, method, from, nil, map[string]string{"Destination": dest, "Overwrite": ow})
		if err != nil {
			return err
		}
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return httpx.ExpectStatus(method+" "+from, resp, 200, 201, 204)
	})
	return c.mapErr(err)
}

// Move перемещает ресурс.
func (c *Client) Move(ctx context.Context, from, to string, overwrite bool) error {
	return c.transfer(ctx, "MOVE", from, to, overwrite)
}

// Copy копирует ресурс.
func (c *Client) Copy(ctx context.Context, from, to string, overwrite bool) error {
	return c.transfer(ctx, "COPY", from, to, overwrite)
}
