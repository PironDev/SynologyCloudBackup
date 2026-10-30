package engine

import (
	"fmt"
	"time"
)

// Политики ротации. oldest — удалять самые старые сверх лимита; smart — «умная» ротация.
const (
	PolicyOldest = "oldest"
	PolicySmart  = "smart"
)

// staleStamps возвращает версии, которые нужно удалить. times — времена снимков, новые первыми.
// keep <= 0 — ничего не удалять.
func staleStamps(times []time.Time, keep int, policy string) []int {
	if keep <= 0 || len(times) <= keep {
		return nil
	}
	if policy != PolicySmart {
		out := make([]int, 0, len(times)-keep)
		for i := keep; i < len(times); i++ {
			out = append(out, i)
		}
		return out
	}
	// Smart: в каждом интервале оставляем самый новый снимок.
	// До 7 дней — по дням, до 35 дней — по неделям, дальше — по месяцам.
	ref := times[0]
	seen := map[string]bool{}
	var kept []int
	for i, t := range times {
		age := ref.Sub(t)
		var key string
		switch {
		case age < 7*24*time.Hour:
			key = t.Format("d2006-01-02")
		case age < 35*24*time.Hour:
			y, w := t.ISOWeek()
			key = fmt.Sprintf("w%d-%02d", y, w)
		default:
			key = "m" + t.Format("2006-01")
		}
		if !seen[key] {
			seen[key] = true
			kept = append(kept, i)
		}
	}
	if len(kept) > keep {
		kept = kept[:keep] // сверх лимита остаются самые новые
	}
	keepIdx := map[int]bool{}
	for _, i := range kept {
		keepIdx[i] = true
	}
	var out []int
	for i := range times {
		if !keepIdx[i] {
			out = append(out, i)
		}
	}
	return out
}
