package provider

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"
	"time"
)

// Режимы сверки после загрузки.
const (
	VerifySizeOnly = "size" // только размер (облако возвращает его всегда)
	VerifyHash     = "hash" // размер + MD5, если облако отдаёт контрольную сумму (по умолчанию)
	VerifyFull     = "full" // размер + MD5; если облако хеш не отдаёт — файл скачивается обратно и хешируется
)

// NormVerify приводит значение режима к известному.
func NormVerify(m string) string {
	switch m {
	case VerifySizeOnly, VerifyFull:
		return m
	}
	return VerifyHash
}

// hashSource считает MD5 того, что реально прочитал загрузчик. Каждый Open начинает счёт заново,
// итог берётся по последней попытке, дочитанной до объявленного размера.
type hashSource struct {
	size int64
	open func() (io.ReadCloser, error)
	mu   sync.Mutex
	sum  string
}

func (h *hashSource) Open() (io.ReadCloser, error) {
	rc, err := h.open()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.sum = ""
	h.mu.Unlock()
	r := &hashReader{ReadCloser: rc, h: h, md: md5.New()}
	if h.size == 0 {
		r.finish()
	}
	return r, nil
}

func (h *hashSource) Sum() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sum
}

type hashReader struct {
	io.ReadCloser
	h    *hashSource
	md   hash.Hash
	n    int64
	done bool
}

func (r *hashReader) Read(b []byte) (int, error) {
	n, err := r.ReadCloser.Read(b)
	if n > 0 && !r.done {
		r.md.Write(b[:n])
		r.n += int64(n)
	}
	if r.n >= r.h.size && !r.done || err == io.EOF && !r.done && r.n == r.h.size {
		r.finish()
	}
	return n, err
}

func (r *hashReader) finish() {
	r.done = true
	r.h.mu.Lock()
	r.h.sum = hex.EncodeToString(r.md.Sum(nil))
	r.h.mu.Unlock()
}

// ReportsMD5 можно реализовать у провайдера, который отдаёт MD5 (тогда сверка немного ждёт его появления).

// UploadVerified загружает файл и сверяет результат с исходником. Возвращает, чем подтверждена загрузка:
// "md5" (контрольная сумма совпала), "md5+скачивание" (сверка полным чтением) или "размер".
// При несовпадении суммы файл загружается заново (до 3 попыток), затем возвращается ошибка.
func UploadVerified(ctx context.Context, p Provider, remote string, src Source, mode string) (string, error) {
	if v, ok := p.(VerifiedUploader); ok {
		return v.UploadVerified(ctx, remote, src, mode)
	}
	return uploadVerified(ctx, p, remote, src, mode)
}

// VerifiedUploader реализуют провайдеры-обёртки, которые сами решают, как загрузить и сверить файл.
type VerifiedUploader interface {
	UploadVerified(ctx context.Context, remote string, src Source, mode string) (string, error)
}

func uploadVerified(ctx context.Context, p Provider, remote string, src Source, mode string) (string, error) {
	mode = NormVerify(mode)
	if mode == VerifySizeOnly {
		return "размер", p.Upload(ctx, remote, src)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		hs := &hashSource{size: src.Size, open: src.Open}
		if err := p.Upload(ctx, remote, Source{Size: src.Size, Open: hs.Open}); err != nil {
			return "", err
		}
		local := hs.Sum()
		if local == "" {
			return "размер", nil // источник не удалось прочитать целиком в одном проходе — сверка по размеру
		}
		how, err := compareRemote(ctx, p, remote, local, mode)
		if err == nil {
			return how, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lastErr = err
	}
	return "", lastErr
}

func compareRemote(ctx context.Context, p Provider, remote, local, mode string) (string, error) {
	var re Entry
	var err error
	// облако может отдать хеш не сразу — несколько коротких попыток
	for i := 0; i < 4; i++ {
		re, err = p.Stat(ctx, remote)
		if err != nil {
			return "", err
		}
		if re.MD5 != "" {
			break
		}
		if hr, ok := p.(interface{ ReportsMD5() bool }); !(ok && hr.ReportsMD5()) || i == 3 {
			// облако без хешей (WebDAV) пустым и останется — долго не ждём
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if re.MD5 != "" {
		if !strings.EqualFold(re.MD5, local) {
			return "", fmt.Errorf("контрольная сумма не совпала: на диске %s, у источника %s", re.MD5, local)
		}
		return "md5", nil
	}
	if mode != VerifyFull {
		return "размер", nil
	}
	rc, err := p.Download(ctx, remote, 0, -1)
	if err != nil {
		return "", fmt.Errorf("сверка скачиванием: %w", err)
	}
	defer rc.Close()
	md := md5.New()
	if _, err := io.Copy(md, rc); err != nil {
		return "", fmt.Errorf("сверка скачиванием: %w", err)
	}
	if got := hex.EncodeToString(md.Sum(nil)); !strings.EqualFold(got, local) {
		return "", fmt.Errorf("контрольная сумма не совпала после скачивания: на диске %s, у источника %s", got, local)
	}
	return "md5+скачивание", nil
}
