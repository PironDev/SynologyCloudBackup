package cron

import (
	"testing"
	"time"
)

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "*/0 * * * *", "a * * * *", "5-1 * * * *"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) должен вернуть ошибку", bad)
		}
	}
}

func TestMatchAndNext(t *testing.T) {
	loc := time.UTC
	at := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, loc) }
	cases := []struct {
		spec  string
		after time.Time
		want  time.Time
	}{
		{"0 3 * * *", at(2026, 10, 30, 16, 40), at(2026, 10, 31, 3, 0)},
		{"*/15 * * * *", at(2026, 10, 30, 16, 40), at(2026, 10, 30, 16, 45)},
		{"0 */6 * * *", at(2026, 10, 30, 16, 40), at(2026, 10, 30, 18, 0)},
		{"30 2 1 * *", at(2026, 10, 30, 16, 40), at(2026, 11, 1, 2, 30)},
		{"0 3 * * 0", at(2026, 10, 30, 16, 40), at(2026, 11, 1, 3, 0)},   // воскресенье
		{"0 3 * * 7", at(2026, 10, 30, 16, 40), at(2026, 11, 1, 3, 0)},   // 7 = воскресенье
		{"0 9 * * 1-5", at(2026, 10, 30, 16, 40), at(2026, 11, 2, 9, 0)}, // пятница → понедельник
		{"0 0 29 2 *", at(2026, 3, 1, 0, 0), at(2028, 2, 29, 0, 0)},
		{"5,35 8-9 * * *", at(2026, 10, 30, 8, 5), at(2026, 10, 30, 8, 35)},
	}
	for _, c := range cases {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.spec, err)
		}
		got, ok := s.Next(c.after)
		if !ok || !got.Equal(c.want) {
			t.Errorf("Next(%q, %v) = %v, ожидалось %v", c.spec, c.after, got, c.want)
		}
		if !s.Match(got) {
			t.Errorf("Match(%q, %v) должен быть true", c.spec, got)
		}
	}
}

func TestDomOrDow(t *testing.T) {
	// когда заданы и день месяца, и день недели, срабатывает любое совпадение
	s, _ := Parse("0 0 13 * 5")
	if !s.Match(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)) { // пятница 13-е
		t.Error("13 ноября 2026 (пятница, 13-е) должно совпасть")
	}
	if !s.Match(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)) { // пятница, не 13-е
		t.Error("пятница должна совпасть")
	}
	if !s.Match(time.Date(2026, 12, 13, 0, 0, 0, 0, time.UTC)) { // воскресенье, 13-е
		t.Error("13-е число должно совпасть")
	}
	if s.Match(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)) {
		t.Error("вторник 10-е не должен совпасть")
	}
}

func TestImpossibleSchedule(t *testing.T) {
	s, err := Parse("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Next(time.Now()); ok {
		t.Error("31 февраля не существует: Next должен вернуть false")
	}
}

func TestOnceAt(t *testing.T) {
	at, ok, err := OnceAt("@at 2026-11-04 01:00")
	if err != nil || !ok || at.Day() != 4 || at.Month() != 11 || at.Hour() != 1 {
		t.Fatalf("OnceAt: %v %v %v", at, ok, err)
	}
	if _, ok, _ := OnceAt("0 3 * * *"); ok {
		t.Fatal("cron-выражение не должно считаться @at")
	}
	if _, _, err := OnceAt("@at 2026-13-45 01:00"); err == nil {
		t.Fatal("неверная дата должна давать ошибку")
	}
}
