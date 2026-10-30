// Package cron разбирает расписание из 5 полей: минуты часы день месяц день_недели.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule — разобранное расписание.
type Schedule struct {
	min, hour, dom, mon, dow uint64
	domStar, dowStar         bool
}

// Parse разбирает выражение. Поддерживаются *, списки, диапазоны, шаги (*/5, 1-10/2).
func Parse(spec string) (*Schedule, error) {
	f := strings.Fields(spec)
	if len(f) != 5 {
		return nil, fmt.Errorf("ожидается 5 полей (минуты часы день месяц день_недели), получено %d", len(f))
	}
	s := &Schedule{}
	var err error
	if s.min, _, err = field(f[0], 0, 59); err != nil {
		return nil, fmt.Errorf("минуты: %w", err)
	}
	if s.hour, _, err = field(f[1], 0, 23); err != nil {
		return nil, fmt.Errorf("часы: %w", err)
	}
	if s.dom, s.domStar, err = field(f[2], 1, 31); err != nil {
		return nil, fmt.Errorf("день месяца: %w", err)
	}
	if s.mon, _, err = field(f[3], 1, 12); err != nil {
		return nil, fmt.Errorf("месяц: %w", err)
	}
	if s.dow, s.dowStar, err = field(f[4], 0, 7); err != nil {
		return nil, fmt.Errorf("день недели: %w", err)
	}
	if s.dow&(1<<7) != 0 { // 7 = воскресенье
		s.dow |= 1
	}
	return s, nil
}

func field(s string, lo, hi int) (mask uint64, star bool, err error) {
	for _, part := range strings.Split(s, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			step, err = strconv.Atoi(part[i+1:])
			if err != nil || step < 1 {
				return 0, false, fmt.Errorf("неверный шаг %q", part)
			}
			part = part[:i]
		}
		a, b := lo, hi
		switch {
		case part == "*":
			if step == 1 && !strings.Contains(s, ",") {
				star = true
			}
		case strings.Contains(part, "-"):
			r := strings.SplitN(part, "-", 2)
			if a, err = strconv.Atoi(r[0]); err != nil {
				return 0, false, fmt.Errorf("неверное значение %q", part)
			}
			if b, err = strconv.Atoi(r[1]); err != nil {
				return 0, false, fmt.Errorf("неверное значение %q", part)
			}
		default:
			if a, err = strconv.Atoi(part); err != nil {
				return 0, false, fmt.Errorf("неверное значение %q", part)
			}
			if step == 1 || !strings.Contains(s, "/") {
				b = a
			}
		}
		if a < lo || b > hi || a > b {
			return 0, false, fmt.Errorf("значение вне диапазона %d-%d: %q", lo, hi, part)
		}
		for v := a; v <= b; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, star, nil
}

func has(mask uint64, v int) bool { return mask&(1<<uint(v)) != 0 }

func (s *Schedule) dayOK(t time.Time) bool {
	d, w := has(s.dom, t.Day()), has(s.dow, int(t.Weekday()))
	if s.domStar || s.dowStar {
		return d && w
	}
	return d || w
}

// Match сообщает, срабатывает ли расписание в минуту t.
func (s *Schedule) Match(t time.Time) bool {
	return has(s.min, t.Minute()) && has(s.hour, t.Hour()) && has(s.mon, int(t.Month())) && s.dayOK(t)
}

// Next возвращает ближайшее срабатывание строго после after.
func (s *Schedule) Next(after time.Time) (time.Time, bool) {
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, after.Location()).Add(time.Minute)
	for i := 0; i < 200000; i++ {
		switch {
		case !has(s.mon, int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
		case !s.dayOK(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		case !has(s.hour, t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
		case !has(s.min, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}

// AtPrefix начинает однократное расписание: "@at 2026-11-04 01:00" (время NAS).
const AtPrefix = "@at "

// OnceAt разбирает однократное расписание. ok — spec начинается с AtPrefix.
func OnceAt(spec string) (at time.Time, ok bool, err error) {
	rest, found := strings.CutPrefix(strings.TrimSpace(spec), AtPrefix)
	if !found {
		return time.Time{}, false, nil
	}
	at, err = time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(rest), time.Local)
	return at, true, err
}
