// Package cloud — драйвер Облака Mail.ru через приватный API cloud.mail.ru:
// вход по логину и паролю (OAuth), загрузка через шард, фиксация файла по
// хешу mrhash. Протокол повторяет rclone (backend/mailru), но не является
// официальным: Mail.ru может его изменить без предупреждения.
package cloud

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"nascloud/internal/httpx"
	"nascloud/internal/provider"
	"nascloud/internal/provider/mailru/mrhash"
)

// Адреса по умолчанию. В тестах заменяются полями Client.
const (
	DefaultAPI      = "https://cloud.mail.ru"
	DefaultDispatch = "https://dispatcher.cloud.mail.ru"
	DefaultOAuth    = "https://o2.mail.ru/token"
	clientID        = "cloud-win"
	maxList         = "2147483647"
	metaTTL         = 20 * time.Minute
	shardTTL        = 30 * time.Minute
	// PublicLinkURL — префикс публичной ссылки на файл.
	PublicLinkURL = "https://cloud.mail.ru/public/"
	// speedupMinSize — меньше этого размера загрузка по хешу не окупается.
	speedupMinSize = 512
	// speedupMaxSize — больше этого размера загрузка по хешу не пробуется (как speedup_max_disk в rclone).
	speedupMaxSize = 3 << 30
	// DefaultSpeedupPatterns — имена файлов для загрузки по хешу, как в rclone; пусто — выключено.
	DefaultSpeedupPatterns = "*.mkv,*.avi,*.mp4,*.mp3,*.zip,*.gz,*.rar,*.pdf"
)

// winNames заменяет символы, которые Облако не принимает в именах, на полноширинные аналоги.
var winNames = []string{`"`, "＂", "*", "＊", ":", "：", "<", "＜", ">", "＞", "?", "？", `\`, "＼", "|", "｜"}

var (
	encodeNames = strings.NewReplacer(winNames...)
	decodeNames = strings.NewReplacer(swapPairs(winNames)...)
)

func swapPairs(p []string) []string {
	out := make([]string, 0, len(p))
	for i := 0; i+1 < len(p); i += 2 {
		out = append(out, p[i+1], p[i])
	}
	return out
}

func encodeName(p string) string { return encodeNames.Replace(p) }
func decodeName(p string) string { return decodeNames.Replace(p) }

// Client — драйвер Облака Mail.ru по приватному API.
type Client struct {
	login    string
	password string

	// UserAgent — значение заголовка User-Agent для всех запросов.
	UserAgent string
	// PollDelay — пауза между проверками размера после загрузки.
	PollDelay time.Duration
	// Speedup — пробовать загрузку по хешу: если облако уже знает файл с тем же mrhash, данные не передаются.
	Speedup bool
	// unknownDirs и atomicMkdir — quirks из rclone (см. SetQuirks).
	unknownDirs bool
	atomicMkdir bool
	// SpeedupPatterns — через запятую; пробуется только для файлов, чьё имя подходит под шаблон.
	SpeedupPatterns string

	apiBase      string
	dispatchBase string
	oauthURL     string

	api  *http.Client // короткие запросы
	xfer *http.Client // передача файлов без общего таймаута
	pace *httpx.Pacer // пауза между запросами и повторы при 429/5xx

	mu       sync.Mutex // токен доступа
	token    string
	tokenExp time.Time

	metaMu  sync.Mutex // адрес метасервера
	meta    string
	metaExp time.Time

	shardMu  sync.Mutex // адрес сервера загрузки
	shard    string
	shardExp time.Time
}

// New создаёт клиент. Логин и пароль обязательны; пароль — пароль внешнего приложения.
func New(login, password, userAgent string) (*Client, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return nil, errors.New("укажите почту и пароль")
	}
	return &Client{
		login:        login,
		password:     password,
		UserAgent:    userAgent,
		apiBase:      DefaultAPI,
		dispatchBase: DefaultDispatch,
		oauthURL:     DefaultOAuth,
		api: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, DisableCompression: true, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second,
		}},
		xfer:            &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 10 * time.Minute, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second}},
		pace:            httpx.NewPacer(100*time.Millisecond, 5*time.Second),
		PollDelay:       2 * time.Second,
		Speedup:         true,
		SpeedupPatterns: DefaultSpeedupPatterns,
	}, nil
}

// apiError — статус, который API вернул внутри ответа.
type apiError struct {
	Status int
	Op     string
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: статус %d: %s", e.Op, e.Status, e.Body)
}

// statusOf возвращает HTTP-статус или статус из ответа API; 0 — неизвестен.
func statusOf(err error) int {
	var se *httpx.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// mapErr переводит ошибки API в общие ошибки провайдера.
func mapErr(err error) error {
	switch statusOf(err) {
	case 0:
		return err
	case 401, 403:
		return fmt.Errorf("%w: %v", provider.ErrAuth, err)
	case 404:
		return fmt.Errorf("%w: %v", provider.ErrNotFound, err)
	}
	return err
}

func (c *Client) setUA(req *http.Request) {
	req.Header.Set("User-Agent", c.UserAgent)
}

// accessToken возвращает действующий токен и входит заново, если срок вышел.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExp) {
		return c.token, nil
	}
	form := url.Values{"client_id": {clientID}, "grant_type": {"password"}, "username": {c.login}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, "POST", c.oauthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setUA(req)
	resp, err := c.api.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%w: вход в Облако Mail.ru отклонён (HTTP %d); нужен пароль внешнего приложения", provider.ErrAuth, resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", err
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("%w: сервер не вернул токен", provider.ErrAuth)
	}
	ttl := time.Duration(t.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	c.token = t.AccessToken
	c.tokenExp = time.Now().Add(ttl - time.Minute)
	return c.token, nil
}

// invalidate сбрасывает токен, чтобы следующий запрос вошёл заново.
func (c *Client) invalidate() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// call выполняет fn с токеном и повторами. При отказе доступа входит заново один раз.
func (c *Client) call(ctx context.Context, fn func(tok string) error) error {
	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		err = c.pace.Do(ctx, 10, func() error { return fn(tok) })
		if attempt == 0 && (statusOf(err) == 401 || statusOf(err) == 403) {
			c.invalidate()
			continue
		}
		return err
	}
}

// dispatch возвращает адрес сервера, который диспетчер выдал на GET path.
func (c *Client) dispatch(ctx context.Context, p string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.dispatchBase+p, nil)
	if err != nil {
		return "", err
	}
	c.setUA(req)
	resp, err := c.api.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := httpx.ExpectStatus("GET "+p, resp, 200); err != nil {
		return "", err
	}
	return readWord(resp.Body)
}

// readWord возвращает первое слово ответа.
func readWord(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "", errors.New("пустой ответ сервера Облака Mail.ru")
	}
	return f[0], nil
}

// metaServer возвращает адрес метасервера; кэширует его на metaTTL.
func (c *Client) metaServer(ctx context.Context) (string, error) {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	if c.meta != "" && time.Now().Before(c.metaExp) {
		return c.meta, nil
	}
	s, err := c.dispatch(ctx, "/m")
	if err != nil {
		return "", err
	}
	c.meta, c.metaExp = s, time.Now().Add(metaTTL)
	return s, nil
}

// uploadShard возвращает адрес сервера загрузки; кэширует его на shardTTL.
func (c *Client) uploadShard(ctx context.Context) (string, error) {
	c.shardMu.Lock()
	defer c.shardMu.Unlock()
	if c.shard != "" && time.Now().Before(c.shardExp) {
		return c.shard, nil
	}
	s, err := c.dispatch(ctx, "/u")
	if err != nil {
		return "", err
	}
	c.shard, c.shardExp = s, time.Now().Add(shardTTL)
	return s, nil
}

// apiCall вызывает JSON-метод /api/m1. Поле body ответа декодируется в out (может быть nil).
func (c *Client) apiCall(ctx context.Context, method, p string, query, form url.Values, out any) error {
	return c.call(ctx, func(tok string) error {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("access_token", tok)
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, c.apiBase+p+"?"+q.Encode(), body)
		if err != nil {
			return err
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		c.setUA(req)
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := httpx.ExpectStatus(method+" "+p, resp, 200); err != nil {
			return err
		}
		var env struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&env); err != nil {
			return err
		}
		if env.Status != 200 {
			return &apiError{Status: env.Status, Op: p, Body: string(env.Body)}
		}
		if out != nil && len(env.Body) > 0 {
			return json.Unmarshal(env.Body, out)
		}
		return nil
	})
}

// binOp отправляет команду метасервера и возвращает код первого байта ответа.
func (c *Client) binOp(ctx context.Context, w *binWriter) (byte, error) {
	var code byte
	err := c.call(ctx, func(tok string) error {
		meta, err := c.metaServer(ctx)
		if err != nil {
			return err
		}
		q := url.Values{"client_id": {clientID}, "token": {tok}}
		req, err := http.NewRequestWithContext(ctx, "POST", meta+"?"+q.Encode(), bytes.NewReader(w.b.Bytes()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		c.setUA(req)
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := httpx.ExpectStatus("POST meta", resp, 200); err != nil {
			return err
		}
		var b [1]byte
		if _, err := io.ReadFull(resp.Body, b[:]); err != nil {
			return err
		}
		code = b[0]
		return nil
	})
	return code, err
}

// item — элемент списка или сведения о файле.
type item struct {
	Kind  string `json:"kind"`
	Home  string `json:"home"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	Hash  string `json:"hash"`
}

// toEntry переводит элемент API в Entry. Неизвестный тип — ошибка, пока не включён quirk unknowndirs.
func (c *Client) toEntry(it item) (provider.Entry, error) {
	p := provider.Clean(decodeName(it.Home))
	isDir := it.Kind != "file"
	switch it.Kind {
	case "file", "folder", "camera-upload", "mounted", "shared":
	default:
		if !c.unknownDirs {
			return provider.Entry{}, fmt.Errorf("облако вернуло неизвестный тип %q у %s", it.Kind, p)
		}
		isDir = true
	}
	return provider.Entry{Name: path.Base(p), Path: p, IsDir: isDir, Size: it.Size, ModTime: time.Unix(it.Mtime, 0)}, nil
}

// Check проверяет вход и возвращает логин и место в облаке.
func (c *Client) Check(ctx context.Context) (provider.AccountInfo, error) {
	var u struct {
		Login string `json:"login"`
		Cloud struct {
			Space struct {
				BytesTotal int64 `json:"bytes_total"`
				BytesUsed  int64 `json:"bytes_used"`
			} `json:"space"`
		} `json:"cloud"`
	}
	if err := c.apiCall(ctx, "GET", "/api/m1/user", nil, nil, &u); err != nil {
		return provider.AccountInfo{}, mapErr(err)
	}
	title := u.Login
	if title == "" {
		title = c.login
	}
	return provider.AccountInfo{Title: title, Total: u.Cloud.Space.BytesTotal, Used: u.Cloud.Space.BytesUsed}, nil
}

// List возвращает содержимое папки.
func (c *Client) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	var out struct {
		List []item `json:"list"`
	}
	form := url.Values{"home": {encodeName(provider.Clean(dir))}}
	q := url.Values{"offset": {"0"}, "limit": {maxList}}
	if err := c.apiCall(ctx, "POST", "/api/m1/folder", q, form, &out); err != nil {
		return nil, mapErr(err)
	}
	entries := make([]provider.Entry, 0, len(out.List))
	for _, it := range out.List {
		e, err := c.toEntry(it)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// statItem возвращает сведения о файле или папке в виде элемента API.
func (c *Client) statItem(ctx context.Context, p string) (item, error) {
	var it item
	q := url.Values{"home": {encodeName(provider.Clean(p))}, "offset": {"0"}, "limit": {maxList}}
	err := c.apiCall(ctx, "GET", "/api/m1/file", q, nil, &it)
	return it, err
}

// statErr переводит ошибку чтения сведений: отсутствие файла — ErrNotFound.
func statErr(err error) error {
	if s := statusOf(err); s == 400 || s == 404 {
		return provider.ErrNotFound
	}
	return mapErr(err)
}

// Stat возвращает сведения о файле или папке.
func (c *Client) Stat(ctx context.Context, p string) (provider.Entry, error) {
	it, err := c.statItem(ctx, p)
	if err != nil {
		return provider.Entry{}, statErr(err)
	}
	return c.toEntry(it)
}

// Mkdir создаёт папку. Существующая папка — не ошибка.
func (c *Client) Mkdir(ctx context.Context, p string) error {
	var w binWriter
	w.u64(opCreateFolder)
	w.u64(0)
	w.str(encodeName(provider.Clean(p)))
	w.u64(0)
	code, err := c.binOp(ctx, &w)
	if err != nil {
		return mapErr(err)
	}
	switch code {
	case 0: // создана
		return nil
	case 4, 9: // уже есть; уже есть в другом регистре
		if c.atomicMkdir {
			return provider.ErrExists
		}
		return nil
	case 1:
		return provider.ErrNotFound
	case 10:
		return fmt.Errorf("Облако Mail.ru не принимает имя %q", p)
	}
	return fmt.Errorf("Облако Mail.ru: создание папки %q, код %d", p, code)
}

// addFile фиксирует файл на метасервере по хешу и возвращает код результата.
func (c *Client) addFile(ctx context.Context, p string, size, mtime int64, sum []byte) (byte, error) {
	var w binWriter
	w.u64(opAddFile)
	w.u64(0)
	w.str(encodeName(p))
	w.u64(size)
	w.u64(mtime)
	w.u64(0)
	w.raw(sum)
	w.u64(1) // перезаписать
	return c.binOp(ctx, &w)
}

// addResult переводит код add-file в ошибку. Коды 0, 4, 9 и 12 — файл принят.
func addResult(code byte, p string) error {
	switch code {
	case 0, 4, 9, 12:
		return nil
	case 5:
		return fmt.Errorf("%w: родительская папка %q отсутствует", provider.ErrNotFound, path.Dir(p))
	case 7:
		return provider.ErrInsufficientStorage
	case 10:
		return fmt.Errorf("Облако Mail.ru не принимает имя %q", p)
	}
	return fmt.Errorf("Облако Mail.ru: фиксация файла %q, код %d", p, code)
}

// hashSource считает mrhash источника, читая его целиком.
func hashSource(src provider.Source) ([]byte, error) {
	rc, err := src.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	h := mrhash.New()
	if _, err := io.Copy(h, rc); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// speedupEligible — как eligibleForSpeedup в rclone: размер в допустимых границах и имя подходит под шаблон.
func (c *Client) speedupEligible(name string, size int64) bool {
	if size < speedupMinSize || size >= speedupMaxSize {
		return false
	}
	name = strings.ToLower(path.Base(name))
	for _, pat := range strings.Split(c.SpeedupPatterns, ",") {
		pat = strings.ToLower(strings.TrimSpace(pat))
		if ok, _ := path.Match(pat, name); ok && pat != "" {
			return true
		}
	}
	return false
}

// Upload загружает файл. Сначала пробует загрузку по хешу (speedup); при неудаче передаёт данные через шард.
func (c *Client) Upload(ctx context.Context, p string, src provider.Source) error {
	p = provider.Clean(p)
	mtime := src.ModTime
	if mtime.IsZero() {
		mtime = time.Now()
	}
	if c.Speedup && c.speedupEligible(p, src.Size) {
		sum, err := hashSource(src)
		if err != nil {
			return err
		}
		if code, err := c.addFile(ctx, p, src.Size, mtime.Unix(), sum); err == nil && addResult(code, p) == nil {
			return provider.VerifySize(ctx, c, p, src.Size, 8, c.PollDelay)
		}
	}

	var sum []byte
	err := c.call(ctx, func(tok string) error {
		shard, err := c.uploadShard(ctx)
		if err != nil {
			return err
		}
		body, err := src.Open()
		if err != nil {
			return err
		}
		h := mrhash.New()
		// TeeReader считает хеш тех же байтов, что уходят на шард.
		rc := struct {
			io.Reader
			io.Closer
		}{io.TeeReader(body, h), body}
		q := url.Values{"client_id": {clientID}, "token": {tok}}
		req, err := http.NewRequestWithContext(ctx, "PUT", shard+"?"+q.Encode(), rc)
		if err != nil {
			body.Close()
			return err
		}
		req.ContentLength = src.Size
		if src.Size == 0 {
			body.Close()
			req.Body = http.NoBody
		}
		c.setUA(req)
		resp, err := c.xfer.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := httpx.ExpectStatus("PUT shard", resp, 200, 201); err != nil {
			return err
		}
		word, err := readWord(resp.Body)
		if err != nil {
			return err
		}
		want := h.Sum(nil)
		if !strings.EqualFold(word, hex.EncodeToString(want)) {
			return fmt.Errorf("сервер вернул хеш %s, ожидался %x: загрузка повреждена", word, want)
		}
		sum = want
		return nil
	})
	if err != nil {
		return mapErr(err)
	}

	code, err := c.addFile(ctx, p, src.Size, mtime.Unix(), sum)
	if err != nil {
		return mapErr(err)
	}
	if err := addResult(code, p); err != nil {
		return err
	}
	return provider.VerifySize(ctx, c, p, src.Size, 8, c.PollDelay)
}

// Download открывает файл с offset; length < 0 — до конца.
// Полная загрузка сверяет mrhash прочитанных байтов с хешем, который вернуло облако.
func (c *Client) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	if length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	var want []byte
	if offset == 0 && length < 0 {
		if it, err := c.statItem(ctx, p); err != nil {
			return nil, statErr(err)
		} else if h, err := mrhash.DecodeString(it.Hash); err == nil {
			want = h
		}
	}
	rc, err := c.openRange(ctx, p, offset, length)
	if err != nil {
		return nil, mapErr(err)
	}
	end := int64(-1)
	if length >= 0 {
		end = offset + length
	}
	rr := &resumeReader{ctx: ctx, c: c, p: p, pos: offset, end: end, rc: rc, left: maxResumes}
	if want != nil {
		return &hashCheck{ReadCloser: rr, h: mrhash.New(), want: want}, nil
	}
	return rr, nil
}

// openRange открывает поток с offset; length < 0 — до конца. Сервер загрузки выбирается на каждый вызов.
func (c *Client) openRange(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := c.call(ctx, func(tok string) error {
		server, err := c.dispatch(ctx, "/d")
		if err != nil {
			return err
		}
		q := url.Values{"client_id": {clientID}, "token": {tok}}
		u := server + "/" + url.PathEscape(strings.TrimLeft(encodeName(provider.Clean(p)), "/")) + "?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return err
		}
		c.setUA(req)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/octet-stream")
		if offset > 0 || length >= 0 {
			rng := "bytes=" + strconv.FormatInt(offset, 10) + "-"
			if length >= 0 {
				rng += strconv.FormatInt(offset+length-1, 10)
			}
			req.Header.Set("Range", rng)
		}
		resp, err := c.xfer.Do(req)
		if err != nil {
			return err
		}
		if err := httpx.ExpectStatus("GET download", resp, 200, 206); err != nil {
			return err
		}
		rc = resp.Body
		if resp.StatusCode == 200 && offset > 0 { // сервер проигнорировал Range
			if err := provider.SkipBytes(rc, offset); err != nil {
				rc.Close()
				return err
			}
		}
		if resp.StatusCode == 200 && length >= 0 {
			rc = provider.Limit(rc, length)
		}
		return nil
	})
	return rc, err
}

// maxResumes — сколько раз продолжать скачивание с места обрыва (как endHandler в rclone).
const maxResumes = 3

// resumeReader при обрыве соединения посреди файла открывает поток заново с последнего полученного байта.
type resumeReader struct {
	ctx  context.Context
	c    *Client
	p    string
	pos  int64 // сколько байт файла уже отдано
	end  int64 // граница диапазона (не включая); -1 — до конца файла
	rc   io.ReadCloser
	left int
}

func (r *resumeReader) Read(b []byte) (int, error) {
	n, err := r.rc.Read(b)
	r.pos += int64(n)
	if err == nil || err == io.EOF || r.left == 0 || r.ctx.Err() != nil {
		return n, err
	}
	if r.end >= 0 && r.pos >= r.end {
		return n, io.EOF
	}
	r.left--
	r.rc.Close()
	length := int64(-1)
	if r.end >= 0 {
		length = r.end - r.pos
	}
	rc, oerr := r.c.openRange(r.ctx, r.p, r.pos, length)
	if oerr != nil {
		return n, oerr
	}
	r.rc = rc
	return n, nil
}

func (r *resumeReader) Close() error { return r.rc.Close() }

// hashCheck считает mrhash читаемых байтов и на конце потока сверяет его с ожидаемым.
type hashCheck struct {
	io.ReadCloser
	h    hash.Hash
	want []byte
}

func (r *hashCheck) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	_, _ = r.h.Write(p[:n])
	if err == io.EOF && !bytes.Equal(r.h.Sum(nil), r.want) {
		return n, fmt.Errorf("контрольная сумма mrhash не совпала: файл повреждён при скачивании")
	}
	return n, err
}

// Publish открывает публичную ссылку на файл или папку и возвращает её.
func (c *Client) Publish(ctx context.Context, p string) (string, error) {
	form := url.Values{"home": {encodeName(provider.Clean(p))}, "email": {c.login}, "x-email": {c.login}}
	var id string
	if err := c.apiCall(ctx, "POST", "/api/m1/file/publish", nil, form, &id); err != nil {
		return "", mapErr(err)
	}
	if id == "" {
		return "", errors.New("Облако Mail.ru не вернуло публичную ссылку")
	}
	return PublicLinkURL + id, nil
}

// Delete удаляет файл или папку рекурсивно.
func (c *Client) Delete(ctx context.Context, p string) error {
	form := url.Values{"home": {encodeName(provider.Clean(p))}}
	return mapErr(c.apiCall(ctx, "POST", "/api/m1/file/remove", nil, form, nil))
}

// rename переименовывает или перемещает ресурс по полным путям.
func (c *Client) rename(ctx context.Context, from, to string) error {
	var w binWriter
	w.u64(opRename)
	w.u64(0)
	w.str(encodeName(from))
	w.u64(0)
	w.str(encodeName(to))
	w.u64(0)
	code, err := c.binOp(ctx, &w)
	if err != nil {
		return mapErr(err)
	}
	switch code {
	case 0:
		return nil
	case 1:
		return provider.ErrNotFound
	case 4:
		return provider.ErrExists
	}
	return fmt.Errorf("Облако Mail.ru: переименование %q, код %d", from, code)
}

// replaceDst готовит путь назначения. Если там уже есть файл, он сначала переносится во временное имя.
// put создаёт новый файл по пути to. Сбой put возвращает старый файл на место.
func (c *Client) replaceDst(ctx context.Context, to string, overwrite bool, put func() error) error {
	if err := provider.MkdirAll(ctx, c, path.Dir(to)); err != nil {
		return err
	}
	_, err := c.Stat(ctx, to)
	switch {
	case errors.Is(err, provider.ErrNotFound):
		return put()
	case err != nil:
		return err
	case !overwrite:
		return provider.ErrExists
	}
	bak := path.Join(path.Dir(to), fmt.Sprintf(".cb-old-%d-%s", time.Now().UnixNano(), path.Base(to)))
	if err := c.rename(ctx, to, bak); err != nil {
		return err
	}
	if err := put(); err != nil {
		if rerr := c.rename(ctx, bak, to); rerr != nil {
			return fmt.Errorf("%w; прежний файл сохранён как %s: %v", err, bak, rerr)
		}
		return err
	}
	_ = c.Delete(ctx, bak) // новый файл уже на месте; ошибка очистки не мешает
	return nil
}

// Move перемещает или переименовывает ресурс.
func (c *Client) Move(ctx context.Context, from, to string, overwrite bool) error {
	from, to = provider.Clean(from), provider.Clean(to)
	return c.replaceDst(ctx, to, overwrite, func() error { return c.rename(ctx, from, to) })
}

// Copy копирует ресурс на стороне Облака. Сервер создаёт копию под временным именем — её переименовывают.
func (c *Client) Copy(ctx context.Context, from, to string, overwrite bool) error {
	from, to = provider.Clean(from), provider.Clean(to)
	return c.replaceDst(ctx, to, overwrite, func() error {
		form := url.Values{
			"home":     {encodeName(from)},
			"folder":   {encodeName(path.Dir(to))},
			"email":    {c.login},
			"x-email":  {c.login},
			"conflict": {"rename"},
		}
		var tmp string
		if err := c.apiCall(ctx, "POST", "/api/m1/file/copy", nil, form, &tmp); err != nil {
			return mapErr(err)
		}
		tmpPath := provider.Clean(decodeName(tmp))
		if tmpPath == to {
			return nil
		}
		if err := c.rename(ctx, tmpPath, to); err != nil {
			_ = c.Delete(ctx, tmpPath)
			return err
		}
		return nil
	})
}

var _ provider.Provider = (*Client)(nil)

// SetQuirks включает особенности сервера, как quirks в rclone: atomicmkdir, unknowndirs.
// binlist не поддерживается: rclone отключает его по умолчанию и считает нестабильным.
func (c *Client) SetQuirks(opt string) error {
	for flag := range strings.SplitSeq(opt, ",") {
		switch strings.ToLower(strings.TrimSpace(flag)) {
		case "":
		case "atomicmkdir":
			c.atomicMkdir = true
		case "unknowndirs":
			c.unknownDirs = true
		case "binlist":
			return errors.New("quirk binlist не поддерживается: бинарный листинг не проверен на реальном сервере")
		default:
			return fmt.Errorf("неизвестный quirk %q", flag)
		}
	}
	return nil
}
