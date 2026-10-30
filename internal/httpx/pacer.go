package httpx

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Pacer задаёт паузу между запросами к одному API и растягивает её после ошибок.
// Логика повторяет pacer из rclone: после повторяемой ошибки пауза удваивается
// до max, после успеха медленно сокращается до min.
type Pacer struct {
	mu    sync.Mutex
	min   time.Duration
	max   time.Duration
	sleep time.Duration // текущая пауза между запросами
	next  time.Time     // раньше этого момента запрос не отправляется
}

// NewPacer создаёт pacer с паузами от min до max.
func NewPacer(minSleep, maxSleep time.Duration) *Pacer {
	return &Pacer{min: minSleep, max: maxSleep, sleep: minSleep}
}

// reserve резервирует слот для следующего запроса и возвращает, сколько ждать до него.
func (p *Pacer) reserve() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	wait := p.next.Sub(now)
	p.next = p.next.Add(p.sleep)
	return wait
}

// failed растягивает паузу после повторяемой ошибки и учитывает Retry-After.
func (p *Pacer) failed(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sleep = min(p.sleep*2, p.max)
	now := time.Now()
	until := now.Add(p.sleep)
	var se *StatusError
	if errors.As(err, &se) && se.RetryAfter > 0 && now.Add(se.RetryAfter).After(until) {
		until = now.Add(se.RetryAfter)
	}
	if until.After(p.next) {
		p.next = until
	}
}

// succeeded сокращает паузу после успешного запроса.
func (p *Pacer) succeeded() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sleep = (p.sleep + p.min) / 2
}

// Do вызывает fn до tries раз. Перед каждым вызовом выдерживает паузу pacer.
// Повторяются только ошибки, для которых Retryable возвращает true.
func (p *Pacer) Do(ctx context.Context, tries int, fn func() error) error {
	var err error
	for i := 0; i < tries; i++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if w := p.reserve(); w > 0 {
			if serr := sleepCtx(ctx, w); serr != nil {
				return serr
			}
		}
		err = fn()
		if !Retryable(err) {
			if err == nil {
				p.succeeded()
			}
			return err
		}
		if i == tries-1 {
			break
		}
		p.failed(err)
	}
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
