package httpx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPacerRetriesRetryableErrors(t *testing.T) {
	p := NewPacer(time.Microsecond, time.Millisecond)
	calls := 0
	err := p.Do(context.Background(), 10, func() error {
		calls++
		if calls < 3 {
			return &StatusError{Op: "GET", Code: 429}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, ожидалось nil и 3 вызова", err, calls)
	}
}

func TestPacerStopsOnNonRetryableError(t *testing.T) {
	p := NewPacer(time.Microsecond, time.Millisecond)
	calls := 0
	want := &StatusError{Op: "GET", Code: 404}
	err := p.Do(context.Background(), 10, func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d, ожидалось один вызов с исходной ошибкой", err, calls)
	}
}

func TestPacerSpacesRequests(t *testing.T) {
	p := NewPacer(20*time.Millisecond, time.Second)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := p.Do(context.Background(), 1, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Fatalf("три запроса за %v, ожидалась пауза не меньше 40ms", d)
	}
}

func TestPacerHonorsRetryAfter(t *testing.T) {
	p := NewPacer(time.Microsecond, time.Microsecond)
	calls := 0
	start := time.Now()
	err := p.Do(context.Background(), 2, func() error {
		calls++
		if calls == 1 {
			return &StatusError{Op: "GET", Code: 429, RetryAfter: 50 * time.Millisecond}
		}
		return nil
	})
	if err != nil || time.Since(start) < 50*time.Millisecond {
		t.Fatalf("err=%v за %v: Retry-After не учтён", err, time.Since(start))
	}
}
