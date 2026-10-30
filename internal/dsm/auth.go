// Package dsm связывает пакет с учётными записями DSM: проверка сессии через штатный
// authenticate.cgi и CGI-мост между веб-сервером DSM и службой пакета.
package dsm

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Пути DSM по умолчанию; в тестах подменяются.
const (
	DefaultAuthProgram = "/usr/syno/synoman/webman/modules/authenticate.cgi"
	DefaultGroupFile   = "/etc/group"
)

var userRe = regexp.MustCompile(`^[A-Za-z0-9._@ \\-]{1,64}$`)

// Auth проверяет, кто сделал запрос, и разрешено ли ему управлять пакетом.
type Auth struct {
	Program   string   // authenticate.cgi
	GroupFile string   // файл групп (для проверки членства в administrators)
	Allow     []string // дополнительные разрешённые пользователи; "*" — любой пользователь DSM
}

// ErrNoSession — запрос без действительной сессии DSM.
var ErrNoSession = errors.New("нет действительной сессии DSM: войдите в DSM заново")

// SessionError — отказ проверки сессии с подробностями для диагностики.
type SessionError struct{ Detail string }

func (e *SessionError) Error() string   { return ErrNoSession.Error() + " [" + e.Detail + "]" }
func (e *SessionError) Is(t error) bool { return t == ErrNoSession }

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// hasEnv сообщает, задана ли переменная (и содержит ли она подстроку contains).
func hasEnv(environ []string, key, contains string) bool {
	for _, kv := range environ {
		if strings.HasPrefix(kv, key+"=") {
			return strings.Contains(kv[len(key)+1:], contains)
		}
	}
	return false
}

// User запускает authenticate.cgi с окружением запроса (в нём уже лежат HTTP_COOKIE,
// HTTP_X_SYNO_TOKEN и т. п.) и возвращает имя пользователя.
func (a Auth) User(environ []string) (string, error) {
	prog := a.Program
	if prog == "" {
		prog = DefaultAuthProgram
	}
	cmd := exec.Command(prog)
	cmd.Env = environ
	var out bytes.Buffer
	cmd.Stdout = &out
	timer := time.AfterFunc(10*time.Second, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	defer timer.Stop()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &SessionError{Detail: fmt.Sprintf("authenticate.cgi: код %d, вывод %q, stderr %q; в запросе: cookie id=%v, X-SYNO-TOKEN=%v",
				ee.ExitCode(), clip(out.String()), clip(stderr.String()), hasEnv(environ, "HTTP_COOKIE", "id="), hasEnv(environ, "HTTP_X_SYNO_TOKEN", ""))}
		}
		return "", fmt.Errorf("не удалось запустить проверку DSM (%s): %w", prog, err)
	}
	return parseUser(out.String())
}

// parseUser достаёт имя из вывода: допускаются заголовки CGI перед пустой строкой.
func parseUser(out string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.Contains(line, ": ") || strings.HasPrefix(strings.ToLower(line), "status") {
			continue
		}
		if !userRe.MatchString(line) {
			return "", ErrNoSession
		}
		return line, nil
	}
	return "", ErrNoSession
}

// Allowed говорит, можно ли пользователю управлять пакетом: администраторы DSM,
// плюс явно перечисленные в Allow.
func (a Auth) Allowed(user string) (bool, error) {
	for _, u := range a.Allow {
		if u == "*" || strings.EqualFold(u, user) {
			return true, nil
		}
	}
	file := a.GroupFile
	if file == "" {
		file = DefaultGroupFile
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return false, fmt.Errorf("не удалось проверить права администратора (%v)", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ":")
		if len(parts) < 4 || parts[0] != "administrators" {
			continue
		}
		for _, m := range strings.Split(parts[3], ",") {
			if strings.EqualFold(strings.TrimSpace(m), user) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, errors.New("в системе не найдена группа administrators")
}
