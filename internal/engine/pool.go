package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"nascloud/internal/provider/crypt"
	"nascloud/internal/provider/split"
	"sort"
	"strconv"
	"strings"
	"sync"

	"nascloud/internal/provider"
	"nascloud/internal/store"
)

// Pool кэширует драйверы аккаунтов (переиспользует HTTP-соединения).
type Pool struct {
	desc provider.Descriptor
	mu   sync.Mutex
	m    map[string]poolEntry
}

type poolEntry struct {
	fp string
	p  provider.Provider
}

func NewPool(d provider.Descriptor) *Pool { return &Pool{desc: d, m: map[string]poolEntry{}} }

// Descriptor возвращает описание вендора.
func (p *Pool) Descriptor() provider.Descriptor { return p.desc }

func fingerprint(f map[string]string) string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, f[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get возвращает драйвер для аккаунта.
func (p *Pool) Get(a store.Account) (provider.Provider, error) {
	fp := fingerprint(a.Fields)
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.m[a.ID]; ok && e.fp == fp {
		return e.p, nil
	}
	pr, err := p.New(a.Fields)
	if err != nil {
		return nil, err
	}
	p.m[a.ID] = poolEntry{fp: fp, p: pr}
	return pr, nil
}

// New создаёт драйвер без кэширования (проверка подключения из формы).
func (p *Pool) New(fields map[string]string) (provider.Provider, error) {
	pr, err := p.desc.New(fields)
	if err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(fields["split_mb"]); v != "" && v != "0" {
		mb, err := strconv.Atoi(v)
		if err != nil || mb < 16 {
			return nil, errors.New("«Резать файлы крупнее»: целое число МБ, не меньше 16 (или 0)")
		}
		pr = split.Wrap(pr, int64(mb)<<20)
	}
	if pw := fields["enc_password"]; pw != "" {
		c, err := crypt.Wrap(pr, pw)
		if err != nil {
			return nil, err
		}
		pr = c
	}
	return pr, nil
}

// Raw возвращает драйвер без обёрток (шифрование, нарезка) — для возможностей облака вроде публичных ссылок.
func (p *Pool) Raw(a store.Account) (provider.Provider, error) { return p.desc.New(a.Fields) }
