// Package telemetry отправляет отчёты об ошибках в Sentry (самохостинг) по протоколу envelope,
// используя только стандартную библиотеку. Работает ТОЛЬКО если пользователь дал согласие;
// перед отправкой из текста вырезаются пути, адреса, токены и прочие персональные данные.
package telemetry

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DSN проекта (публичный ключ, предназначен для встраивания в клиентские приложения).
// Переопределяется переменной CB_SENTRY_DSN; пустое значение переменной отключает отправку.
const DefaultDSN = "https://7d273303a2f849bbaedb4d2614a8e203@sentry.xenos-rbel.synology.me:8443/5"

const (
	maxPerHour = 30
	maxMsgLen  = 600
)

var (
	mu       sync.Mutex
	endpoint string
	pubKey   string
	brand    string
	release  string
	enabled  func() bool
	installF func() string
	sent     []time.Time
	seen     = map[string]time.Time{}
	client   = &http.Client{Timeout: 10 * time.Second}
	wg       sync.WaitGroup
)

// Init настраивает отправку. enabled вызывается перед каждым событием.
func Init(brandID, version string, isEnabled func() bool, installID func() string) {
	dsn := DefaultDSN
	if v, ok := os.LookupEnv("CB_SENTRY_DSN"); ok {
		dsn = v
	}
	mu.Lock()
	defer mu.Unlock()
	brand, release, enabled, installF = brandID, version, isEnabled, installID
	endpoint, pubKey = "", ""
	u, err := url.Parse(dsn)
	if dsn == "" || err != nil || u.User == nil || u.Host == "" {
		return
	}
	project := strings.Trim(u.Path, "/")
	if project == "" {
		return
	}
	pubKey = u.User.Username()
	endpoint = fmt.Sprintf("%s://%s/api/%s/envelope/", u.Scheme, u.Host, project)
}

var (
	reURLQuery = regexp.MustCompile(`(https?://[^\s"'?]+)\?[^\s"']*`)
	reEmail    = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	rePath     = regexp.MustCompile(`(?:[A-Za-z]:\\|/)(?:[^\s"'<>:;,()\[\]{}|]+[/\\])+[^\s"'<>:;,()\[\]{}|]*`)
	reSecret   = regexp.MustCompile(`\b(?:[A-Za-z0-9_\-]{24,}|y0_[A-Za-z0-9_\-]+|[0-9a-f]{16,})\b`)
	reIP       = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	reQuoted   = regexp.MustCompile(`«[^»]*»`)
)

// Scrub вырезает из текста пути, адреса, токены и названия, заданные пользователем.
func Scrub(s string) string {
	s = reURLQuery.ReplaceAllString(s, "$1")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = rePath.ReplaceAllString(s, "<path>")
	s = reIP.ReplaceAllString(s, "<ip>")
	s = reSecret.ReplaceAllString(s, "<secret>")
	s = reQuoted.ReplaceAllString(s, "«…»")
	if r := []rune(s); len(r) > maxMsgLen {
		s = string(r[:maxMsgLen]) + "…"
	}
	return s
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func allow(fp string) bool {
	mu.Lock()
	defer mu.Unlock()
	if endpoint == "" || enabled == nil || !enabled() {
		return false
	}
	now := time.Now()
	if t, ok := seen[fp]; ok && now.Sub(t) < 10*time.Minute {
		return false // одинаковые события не чаще раза в 10 минут
	}
	cut := now.Add(-time.Hour)
	keep := sent[:0]
	for _, t := range sent {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	sent = keep
	if len(sent) >= maxPerHour {
		return false
	}
	sent = append(sent, now)
	seen[fp] = now
	if len(seen) > 500 {
		seen = map[string]time.Time{fp: now}
	}
	return true
}

// Capture отправляет сообщение об ошибке. source — откуда (job, http, ui, panic), tags — дополнительные метки.
func Capture(source, msg string, tags map[string]string) {
	msg = Scrub(msg)
	if msg == "" {
		return
	}
	sum := sha1.Sum([]byte(source + "|" + msg))
	fp := hex.EncodeToString(sum[:])
	if !allow(fp) {
		return
	}
	mu.Lock()
	ep, key, br, rel, inst := endpoint, pubKey, brand, release, ""
	if installF != nil {
		inst = installF()
	}
	mu.Unlock()

	t := map[string]string{"brand": br, "source": source, "arch": runtime.GOARCH, "os": runtime.GOOS}
	for k, v := range tags {
		t[k] = Scrub(v)
	}
	id := randHex(16)
	ev := map[string]any{
		"event_id":    id,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"platform":    "go",
		"level":       "error",
		"release":     br + "@" + rel,
		"environment": "production",
		"logger":      source,
		"message":     map[string]string{"formatted": msg},
		"fingerprint": []string{source, fp},
		"tags":        t,
		"user":        map[string]string{"id": inst}, // анонимный идентификатор установки, без IP и имён
		"sdk":         map[string]string{"name": "nascloud.telemetry", "version": "1"},
	}
	body, _ := json.Marshal(ev)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"event_id":"%s","sent_at":"%s"}`+"\n", id, time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&buf, `{"type":"event","length":%d}`+"\n", len(body))
	buf.Write(body)
	buf.WriteByte('\n')

	wg.Add(1)
	go func() {
		defer wg.Done()
		req, err := http.NewRequest("POST", ep, &buf)
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/x-sentry-envelope")
		req.Header.Set("X-Sentry-Auth", fmt.Sprintf("Sentry sentry_version=7, sentry_client=nascloud/1, sentry_key=%s", key))
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
	}()
}

// Flush ждёт завершения отправки (до timeout).
func Flush(timeout time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// Stack возвращает обезличенный стек вызовов (только имена функций).
func Stack() string {
	pc := make([]uintptr, 24)
	n := runtime.Callers(3, pc)
	var b strings.Builder
	fr := runtime.CallersFrames(pc[:n])
	for {
		f, more := fr.Next()
		if i := strings.LastIndex(f.Function, "/"); i >= 0 {
			f.Function = f.Function[i+1:]
		}
		fmt.Fprintf(&b, "%s ", f.Function)
		if !more {
			break
		}
	}
	return b.String()
}
