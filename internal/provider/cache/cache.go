// Package cache — краткий кэш списков папок поверх провайдера (для проводника).
// Любое изменение через эту обёртку сбрасывает весь кэш; изменения «со стороны» (например, фоновое задание)
// видны не позже чем через TTL либо после ListFresh.
package cache

import (
	"context"
	"io"
	"sync"
	"time"

	"nascloud/internal/provider"
)

type item struct {
	at time.Time
	es []provider.Entry
}

// FS — провайдер-обёртка с кэшем List.
type FS struct {
	in  provider.Provider
	ttl time.Duration
	mu  sync.Mutex
	m   map[string]item
}

// Wrap создаёт обёртку; ttl <= 0 отключает кэширование.
func Wrap(in provider.Provider, ttl time.Duration) *FS {
	return &FS{in: in, ttl: ttl, m: map[string]item{}}
}

func (f *FS) reset() { f.mu.Lock(); f.m = map[string]item{}; f.mu.Unlock() }

// Inner возвращает исходного провайдера.
func (f *FS) Inner() provider.Provider { return f.in }

func (f *FS) Check(ctx context.Context) (provider.AccountInfo, error) { return f.in.Check(ctx) }

// ListFresh читает папку, минуя кэш, и обновляет его.
func (f *FS) ListFresh(ctx context.Context, dir string) ([]provider.Entry, error) {
	es, err := f.in.List(ctx, dir)
	if err == nil && f.ttl > 0 {
		f.mu.Lock()
		f.m[provider.Clean(dir)] = item{time.Now(), append([]provider.Entry(nil), es...)}
		f.mu.Unlock()
	}
	return es, err
}

func (f *FS) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	if f.ttl > 0 {
		f.mu.Lock()
		it, ok := f.m[provider.Clean(dir)]
		f.mu.Unlock()
		if ok && time.Since(it.at) < f.ttl {
			return append([]provider.Entry(nil), it.es...), nil
		}
	}
	return f.ListFresh(ctx, dir)
}

func (f *FS) Stat(ctx context.Context, p string) (provider.Entry, error) { return f.in.Stat(ctx, p) }

func (f *FS) Mkdir(ctx context.Context, p string) error {
	defer f.reset()
	return f.in.Mkdir(ctx, p)
}

func (f *FS) Upload(ctx context.Context, p string, src provider.Source) error {
	defer f.reset()
	return f.in.Upload(ctx, p, src)
}

func (f *FS) UploadVerified(ctx context.Context, p string, src provider.Source, mode string) (string, error) {
	defer f.reset()
	return provider.UploadVerified(ctx, f.in, p, src, mode)
}

func (f *FS) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	return f.in.Download(ctx, p, offset, length)
}

func (f *FS) Delete(ctx context.Context, p string) error {
	defer f.reset()
	return f.in.Delete(ctx, p)
}

func (f *FS) Move(ctx context.Context, from, to string, ow bool) error {
	defer f.reset()
	return f.in.Move(ctx, from, to, ow)
}

func (f *FS) Copy(ctx context.Context, from, to string, ow bool) error {
	defer f.reset()
	return f.in.Copy(ctx, from, to, ow)
}

var _ provider.Provider = (*FS)(nil)
