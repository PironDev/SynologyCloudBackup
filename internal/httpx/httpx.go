// Package httpx содержит общие HTTP-помощники: проверка статуса и повторы.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Backoff — начальная пауза между повторами (в тестах уменьшается).
var Backoff = time.Second

const maxBackoff = 30 * time.Second

// StatusError — неожиданный HTTP-статус.
type StatusError struct {
	Op         string
	Code       int
	Body       string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	b := strings.TrimSpace(e.Body)
	if b == "" {
		return fmt.Sprintf("%s: HTTP %d", e.Op, e.Code)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Op, e.Code, b)
}

// ExpectStatus возвращает nil, если статус среди ok. Иначе читает короткое тело,
// закрывает ответ и возвращает *StatusError. При успехе тело остаётся открытым.
func ExpectStatus(op string, resp *http.Response, ok ...int) error {
	for _, c := range ok {
		if resp.StatusCode == c {
			return nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	resp.Body.Close()
	se := &StatusError{Op: op, Code: resp.StatusCode, Body: string(b)}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if s, err := strconv.Atoi(ra); err == nil && s > 0 && s <= 300 {
			se.RetryAfter = time.Duration(s) * time.Second
		}
	}
	return se
}

// Retryable определяет, стоит ли повторять запрос.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == 429 || (se.Code >= 500 && se.Code != 501 && se.Code != 507)
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// WithRetry вызывает fn до tries раз с экспоненциальной паузой.
func WithRetry(ctx context.Context, tries int, fn func() error) error {
	var err error
	delay := Backoff
	for i := 0; i < tries; i++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		err = fn()
		if err == nil || !Retryable(err) || ctx.Err() != nil {
			return err
		}
		if i == tries-1 {
			break
		}
		wait := delay
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > 0 {
			wait = se.RetryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
		if delay > maxBackoff {
			delay = maxBackoff
		}
	}
	return err
}
