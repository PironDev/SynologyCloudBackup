// Package provider описывает единый контракт хранилища, общий для всех
// вендорских сборок (Яндекс.Диск, Облако Mail.ru).
package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

var (
	ErrNotFound            = errors.New("не найдено на удалённом диске")
	ErrExists              = errors.New("уже существует на удалённом диске")
	ErrAuth                = errors.New("ошибка авторизации (проверьте токен или пароль)")
	ErrInsufficientStorage = errors.New("на удалённом диске не хватает места")
)

// Entry — файл или папка на удалённом диске. Path всегда абсолютный, с '/'.
type Entry struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
	MD5     string // hex, может быть пустым
}

// Source — источник данных для загрузки. Open может вызываться несколько раз
// (повторные попытки).
type Source struct {
	Size int64
	// ModTime — время изменения источника; нулевое значение — не задано.
	ModTime time.Time
	Open    func() (io.ReadCloser, error)
}

// AccountInfo — результат проверки подключения.
type AccountInfo struct {
	Title string `json:"title"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
}

// Provider — операции над удалённым диском.
type Provider interface {
	Check(ctx context.Context) (AccountInfo, error)
	List(ctx context.Context, dir string) ([]Entry, error)
	Stat(ctx context.Context, p string) (Entry, error)
	// Mkdir идемпотентен: существующая папка — не ошибка. Если родителя нет,
	// возвращает ErrNotFound.
	Mkdir(ctx context.Context, p string) error
	// Upload перезаписывает файл.
	Upload(ctx context.Context, p string, src Source) error
	// Download читает файл с offset; length < 0 — до конца.
	Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error)
	// Delete удаляет файл или папку рекурсивно.
	Delete(ctx context.Context, p string) error
	Move(ctx context.Context, from, to string, overwrite bool) error
	Copy(ctx context.Context, from, to string, overwrite bool) error
}

// Field описывает поле формы аккаунта (UI строится по этой схеме).
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
	Secret      bool   `json:"secret"`
	// Global — поле хранится один раз в настройках и общее для всех аккаунтов.
	Global bool `json:"global,omitempty"`
	// Options — если задано, поле рисуется как выпадающий список; Default — значение по умолчанию.
	Options []Option `json:"options,omitempty"`
	Default string   `json:"default,omitempty"`
}

// Option — вариант выбора в поле-списке.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Step — шаг инструкции. В Text ссылки вида https://… в интерфейсе становятся кликабельными.
type Step struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Descriptor — всё, что отличает вендора: название, поля аккаунта, подсказка,
// конструктор драйвера.
type Descriptor struct {
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Fields []Field `json:"fields"`
	Help   string  `json:"help"`
	// Steps — та же инструкция по подключению, разбитая на шаги (для отображения таймлайном).
	Steps []Step `json:"steps,omitempty"`
	// AuthURL — шаблон ссылки для получения токена; {key} заменяется значением поля аккаунта.
	AuthURL string                                             `json:"authUrl,omitempty"`
	New     func(settings map[string]string) (Provider, error) `json:"-"`
}

// SecretKeys возвращает ключи секретных полей.
func (d Descriptor) SecretKeys() []string {
	var out []string
	for _, f := range d.Fields {
		if f.Secret {
			out = append(out, f.Key)
		}
	}
	return out
}

// Clean приводит путь к абсолютному каноническому виду.
func Clean(p string) string { return path.Clean("/" + p) }

// Join склеивает элементы пути.
func Join(elem ...string) string { return Clean(strings.Join(elem, "/")) }

// MkdirAll создаёт папку вместе с родителями.
func MkdirAll(ctx context.Context, p Provider, dir string) error {
	dir = Clean(dir)
	if dir == "/" {
		return nil
	}
	err := p.Mkdir(ctx, dir)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := MkdirAll(ctx, p, path.Dir(dir)); err != nil {
		return err
	}
	return p.Mkdir(ctx, dir)
}

// VerifySize убеждается, что файл на диске имеет ожидаемый размер.
func VerifySize(ctx context.Context, p Provider, remote string, size int64, tries int, delay time.Duration) error {
	var last error
	for i := 0; i < tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		e, err := p.Stat(ctx, remote)
		if err != nil {
			last = err
			continue
		}
		if e.Size == size {
			return nil
		}
		last = fmt.Errorf("размер на диске %d, ожидалось %d", e.Size, size)
	}
	return fmt.Errorf("загрузка не подтверждена: %w", last)
}

// LimitedReadCloser ограничивает чтение, закрывая исходное тело.
type LimitedReadCloser struct {
	io.Reader
	io.Closer
}

// Limit возвращает ReadCloser, отдающий не более n байт.
func Limit(rc io.ReadCloser, n int64) io.ReadCloser {
	return LimitedReadCloser{Reader: io.LimitReader(rc, n), Closer: rc}
}

// SkipBytes пропускает n байт (когда сервер проигнорировал Range).
func SkipBytes(r io.Reader, n int64) error {
	if n <= 0 {
		return nil
	}
	_, err := io.CopyN(io.Discard, r, n)
	return err
}

// DefaultUA — User-Agent по умолчанию, как у rclone v1.76.0 (backend/mailru ставит его же).
const DefaultUA = "rclone/v1.76.0"

// UA возвращает User-Agent: заданный пользователем или DefaultUA.
func UA(custom string) string {
	if c := strings.TrimSpace(custom); c != "" {
		return c
	}
	return DefaultUA
}

// Trasher — провайдеры, умеющие переносить ресурс в корзину облака (восстанавливается оттуда).
type Trasher interface {
	// Trash переносит файл или папку в корзину облака.
	Trash(ctx context.Context, p string) error
}

// TrashEmptier — провайдеры, умеющие очистить корзину облака.
type TrashEmptier interface {
	EmptyTrash(ctx context.Context) error
}

// Publisher — провайдеры, умеющие делать публичные ссылки на файлы.
type Publisher interface {
	// Publish открывает публичный доступ и возвращает ссылку.
	Publish(ctx context.Context, p string) (string, error)
	// Unpublish закрывает публичный доступ.
	Unpublish(ctx context.Context, p string) error
}

// CommonFields — поля, общие для всех облаков: шифрование на стороне клиента и нарезка больших файлов.
func CommonFields() []Field {
	return []Field{
		{Key: "enc_password", Label: "Пароль шифрования", Secret: true, Placeholder: "пусто — без шифрования",
			Help: "Если задан, все файлы шифруются на NAS (AES-256-GCM) до отправки и расшифровываются при скачивании; в облаке лежат файлы с суффиксом .cbenc. Имена файлов и папок не шифруются. Пароль нельзя восстановить: потеряете его — файлы не открыть. Не меняйте пароль у аккаунта с уже загруженными файлами."},
		{Key: "split_mb", Label: "Резать файлы крупнее, МБ", Placeholder: "0 — не резать (минимум 16)",
			Help: "Файлы больше указанного размера загружаются частями (.cbp0001, .cbp0002…) и склеиваются при скачивании; в проводнике и File Station виден один файл. Пригодится при лимите облака на размер файла (у Яндекс.Диска без подписки 1 ГБ, то есть 1000)."},
	}
}
