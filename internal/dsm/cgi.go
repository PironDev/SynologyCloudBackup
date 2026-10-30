package dsm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProxyFile — файл в каталоге данных, где служба публикует адрес и секрет внутреннего
// интерфейса для CGI-моста.
const ProxyFile = "proxy.json"

// ProxyInfo — содержимое ProxyFile.
type ProxyInfo struct {
	Addr   string `json:"addr"`
	Secret string `json:"secret"`
}

// WriteProxyInfo сохраняет адрес и секрет (права 0600).
func WriteProxyInfo(dataDir string, p ProxyInfo) error {
	b, _ := json.Marshal(p)
	tmp := filepath.Join(dataDir, ProxyFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, ProxyFile))
}

// CGI — мост: принимает запрос от веб-сервера DSM (классический CGI), проверяет сессию DSM
// и пересылает запрос службе пакета, которая слушает только 127.0.0.1.
//
// Адрес в браузере: …/index.cgi?r=<закодированный путь с запросом>, например r=%2Fapi%2Fjobs.
type CGI struct {
	DataDir string
	Auth    Auth
	// Base — относительный префикс, по которому страница обращается к мосту.
	Base string
}

type cgiResp struct {
	status  int
	headers http.Header
	body    []byte
	// stream — тело ответа службы, передаётся без буферизации (файлы проводника);
	// length < 0 — длина неизвестна.
	stream io.ReadCloser
	length int64
}

func plainError(status int, msg string) cgiResp {
	b, _ := json.Marshal(map[string]string{"error": msg})
	h := http.Header{}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	return cgiResp{status: status, headers: h, body: b}
}

// Run обрабатывает один CGI-запрос: environ — окружение процесса, stdin — тело запроса,
// stdout — куда писать ответ в формате CGI.
func (c CGI) Run(environ []string, stdin io.Reader, stdout io.Writer) {
	env := envMap(environ)
	resp := c.handle(env, environ, stdin)
	w := &bytes.Buffer{}
	fmt.Fprintf(w, "Status: %d %s\r\n", resp.status, http.StatusText(resp.status))
	for k, vs := range resp.headers {
		for _, v := range vs {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
	if resp.stream == nil {
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(resp.body))
		w.Write(resp.body)
		_, _ = stdout.Write(w.Bytes())
		return
	}
	defer resp.stream.Close()
	if resp.length >= 0 {
		fmt.Fprintf(w, "Content-Length: %d\r\n", resp.length)
	}
	w.WriteString("\r\n")
	_, _ = stdout.Write(w.Bytes())
	_, _ = io.Copy(stdout, resp.stream)
}

func envMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

// forwardedHeaders — заголовки браузера, которые мост передаёт службе.
var forwardedHeaders = map[string]string{
	"HTTP_X_REQUESTED_WITH": "X-Requested-With",
	"CONTENT_TYPE":          "Content-Type",
	"HTTP_RANGE":            "Range",
}

// maxFileBody — предел тела при загрузке файла в проводник; остальные запросы — до 1 МиБ.
const maxFileBody = 64 << 30

func (c CGI) handle(env map[string]string, environ []string, stdin io.Reader) cgiResp {
	values, err := url.ParseQuery(env["QUERY_STRING"])
	if err != nil {
		return plainError(400, "неверный запрос")
	}
	target := values.Get("r")
	if target == "" {
		target = "/"
	}
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.ContainsAny(target, "\r\n\\") {
		return plainError(400, "неверный путь")
	}

	raw, err := os.ReadFile(filepath.Join(c.DataDir, ProxyFile))
	var pi ProxyInfo
	if err == nil {
		err = json.Unmarshal(raw, &pi)
	}
	if err != nil || pi.Addr == "" {
		return plainError(502, "служба пакета не запущена или недоступна для CGI (нет "+ProxyFile+"): запустите пакет в Центре пакетов")
	}

	method := env["REQUEST_METHOD"]
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	var bodyLen int64
	if method != http.MethodGet && method != http.MethodHead {
		n, _ := strconv.ParseInt(env["CONTENT_LENGTH"], 10, 64)
		limit := int64(1 << 20)
		if method == http.MethodPut && strings.HasPrefix(target, "/api/x/file") {
			limit = maxFileBody
		}
		if n > limit {
			return plainError(413, "слишком большой запрос")
		}
		if n > 0 {
			body, bodyLen = io.LimitReader(stdin, n), n
		}
	}

	req, err := http.NewRequest(method, "http://"+pi.Addr+target, body)
	if err != nil {
		return plainError(400, "неверный запрос")
	}
	if body != nil {
		req.ContentLength = bodyLen
	}
	req.Header.Set("X-Proxy-Secret", pi.Secret)
	req.Header.Set("X-Api-Base", c.Base)
	for e, h := range forwardedHeaders {
		if v := env[e]; v != "" {
			req.Header.Set(h, v)
		}
	}

	// Проверка сессии DSM нужна только для API; страница и скрипты не секретны.
	if strings.HasPrefix(target, "/api/") {
		user, aerr := c.Auth.User(environ)
		if aerr == nil {
			var ok bool
			ok, aerr = c.Auth.Allowed(user)
			if aerr == nil && !ok {
				aerr = fmt.Errorf("пользователь «%s» не входит в группу администраторов DSM", user)
			}
		}
		if aerr != nil {
			req.Header.Set("X-DSM-Reason", sanitizeHeader(aerr.Error()))
		} else {
			req.Header.Set("X-DSM-User", user)
		}
	}

	// Общего таймаута нет: передача больших файлов может идти долго.
	client := &http.Client{
		Transport:     &http.Transport{ResponseHeaderTimeout: 10 * time.Minute, DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return plainError(502, "служба пакета не отвечает: "+err.Error())
	}
	h := http.Header{}
	for _, k := range []string{"Content-Type", "Cache-Control", "Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy",
		"Last-Modified", "Etag", "Content-Disposition", "Content-Range", "Accept-Ranges"} {
		if v := res.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if req.Method == http.MethodHead {
		res.Body.Close()
		return cgiResp{status: res.StatusCode, headers: h}
	}
	return cgiResp{status: res.StatusCode, headers: h, stream: res.Body, length: res.ContentLength}
}

func sanitizeHeader(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	return url.PathEscape(s)
}
