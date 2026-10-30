// Package auth: пароль администратора, сессии, защита от перебора.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const iterations = 120000

// RandomToken возвращает n случайных байт в hex.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HashPassword возвращает строку вида pbkdf2-sha256$iter$salt$hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h, err := pbkdf2.Key(sha256.New, pw, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}

// CheckPassword сверяет пароль с хэшем.
func CheckPassword(pw, enc string) bool {
	p := strings.Split(enc, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(p[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[2])
	want, err2 := base64.RawStdEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Equal — сравнение строк за постоянное время.
func Equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// Sessions — сессии в памяти со скользящим сроком.
type Sessions struct {
	mu  sync.Mutex
	m   map[string]time.Time
	ttl time.Duration
}

func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{m: map[string]time.Time{}, ttl: ttl}
}

func (s *Sessions) New() string {
	t := RandomToken(24)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
	s.m[t] = now.Add(s.ttl)
	return t
}

func (s *Sessions) Valid(t string) bool {
	if t == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[t]
	if !ok || time.Now().After(exp) {
		delete(s.m, t)
		return false
	}
	s.m[t] = time.Now().Add(s.ttl)
	return true
}

func (s *Sessions) Delete(t string) {
	s.mu.Lock()
	delete(s.m, t)
	s.mu.Unlock()
}

// Clear завершает все сессии (после смены пароля).
func (s *Sessions) Clear() {
	s.mu.Lock()
	s.m = map[string]time.Time{}
	s.mu.Unlock()
}

// Limiter блокирует источник после серии неудачных входов.
type Limiter struct {
	mu      sync.Mutex
	m       map[string]*attempt
	Max     int
	Penalty time.Duration
}

type attempt struct {
	fails   int
	blocked time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{m: map[string]*attempt{}, Max: 5, Penalty: 5 * time.Minute}
}

// Allowed сообщает, можно ли пробовать вход; вторым значением — сколько ждать.
func (l *Limiter) Allowed(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[key]
	if a == nil {
		return true, 0
	}
	if d := time.Until(a.blocked); d > 0 {
		return false, d
	}
	return true, 0
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[key]
	if a == nil {
		a = &attempt{}
		l.m[key] = a
	}
	a.fails++
	if a.fails >= l.Max {
		a.fails = 0
		a.blocked = time.Now().Add(l.Penalty)
	}
}

func (l *Limiter) Success(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}
