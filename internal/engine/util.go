// Package engine выполняет задания: зеркало и архив, ведёт журнал, запускает по расписанию.
package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Stats — итоги запуска.
type Stats struct {
	Files    int   `json:"files"`    // просмотрено файлов
	Uploaded int   `json:"uploaded"` // выгружено файлов (для архива — частей)
	Skipped  int   `json:"skipped"`  // пропущено как неизменённые
	Errors   int   `json:"errors"`   // файлов с ошибками
	Bytes    int64 `json:"bytes"`    // выгружено байт
	Parts    int   `json:"parts"`
}

// DefaultExcludes — исключения для новых заданий.
var DefaultExcludes = []string{"@eaDir", "#recycle", ".DS_Store", "Thumbs.db"}

// Slug делает безопасное имя папки из названия задания.
func Slug(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		return "backup"
	}
	return s
}

// excluded проверяет имя и относительный путь по шаблонам исключений.
func excluded(patterns []string, name, rel string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		if strings.Contains(p, "/") {
			if ok, _ := path.Match(strings.TrimPrefix(p, "/"), rel); ok {
				return true
			}
		}
	}
	return false
}

func md5File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func uniqueBase(p string, seen map[string]int) string {
	b := filepath.Base(filepath.Clean(p))
	if b == "/" || b == "." || b == "" {
		b = "root"
	}
	seen[b]++
	if n := seen[b]; n > 1 {
		return fmt.Sprintf("%s-%d", b, n)
	}
	return b
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// RunLog пишет построчный журнал запуска в файл (читается «вживую»).
type RunLog struct {
	mu sync.Mutex
	f  *os.File
}

// NewRunLog создаёт журнал.
func NewRunLog(p string) (*RunLog, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &RunLog{f: f}, nil
}

func (l *RunLog) printf(level, format string, a ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, "%s %-5s %s\n", time.Now().Format("2006-01-02 15:04:05"), level, fmt.Sprintf(format, a...))
}

func (l *RunLog) Infof(format string, a ...any)  { l.printf("INFO", format, a...) }
func (l *RunLog) Warnf(format string, a ...any)  { l.printf("WARN", format, a...) }
func (l *RunLog) Errorf(format string, a ...any) { l.printf("ERROR", format, a...) }

// Close закрывает файл журнала.
func (l *RunLog) Close() {
	if l != nil {
		l.f.Close()
	}
}
