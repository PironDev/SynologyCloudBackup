package engine

import (
	"reflect"
	"testing"
	"time"
)

func TestStaleOldestDropsTail(t *testing.T) {
	base := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	var times []time.Time
	for i := 0; i < 5; i++ {
		times = append(times, base.AddDate(0, 0, -i))
	}
	if got := staleStamps(times, 3, PolicyOldest); !reflect.DeepEqual(got, []int{3, 4}) {
		t.Fatalf("oldest: %v", got)
	}
	if got := staleStamps(times, 0, PolicyOldest); got != nil {
		t.Fatalf("keep=0 должен ничего не удалять: %v", got)
	}
}

func TestStaleSmartThinsOldVersions(t *testing.T) {
	ref := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	// Два снимка за сегодня, один вчера, потом по одному на неделю и один на месяц.
	times := []time.Time{
		ref,
		ref.Add(-2 * time.Hour),
		ref.Add(-5 * time.Hour),
		ref.AddDate(0, 0, -10),
		ref.AddDate(0, 0, -17),
		ref.AddDate(0, 0, -60),
		ref.AddDate(0, 0, -62),
	}
	got := staleStamps(times, 4, PolicySmart)
	// Сегодня остаётся самый новый (0). Вчера (2), неделя -10 (3) и неделя -17 (4) остаются.
	// Из -60 и -62 дней один месяц: остаётся самый новый (5). Лимит 4: самый старый (5) уходит.
	want := []int{1, 5, 6}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("smart: got %v, want %v", got, want)
	}
}

func TestStaleSmartRespectsLimit(t *testing.T) {
	ref := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	var times []time.Time
	for i := 0; i < 6; i++ {
		times = append(times, ref.AddDate(0, 0, -i))
	}
	got := staleStamps(times, 2, PolicySmart)
	if len(times)-len(got) != 2 {
		t.Fatalf("после ротации осталось %d, ожидалось 2: %v", len(times)-len(got), got)
	}
	if got[0] != 2 {
		t.Fatalf("самый новый снимок должен остаться: %v", got)
	}
}
