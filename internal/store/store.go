// Package store хранит конфигурацию: аккаунты, задания, настройки.
// Секретные поля шифруются на диске, в памяти лежат открытыми.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"nascloud/internal/auth"
	"nascloud/internal/secret"
)

// Account — подключённый аккаунт облака.
type Account struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Slug   string            `json:"slug"` // путь аккаунта в WebDAV-шлюзе
	Fields map[string]string `json:"fields"`
}

// ArchiveOpts — параметры режима «архив».
type ArchiveOpts struct {
	PartSizeMB int    `json:"partSizeMB"` // 0 — не разбивать
	Keep       int    `json:"keep"`       // 0 — хранить все
	Policy     string `json:"policy"`     // пусто или oldest — удалять старые; smart — умная ротация
	NoCompress bool   `json:"noCompress"` // true — писать tar без gzip
}

// Job — задание выгрузки.
type Job struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	AccountID   string      `json:"accountId"`
	Enabled     bool        `json:"enabled"`
	Mode        string      `json:"mode"` // mirror | archive
	Sources     []string    `json:"sources"`
	Excludes    []string    `json:"excludes"`
	RemoteDir   string      `json:"remoteDir"`
	Schedule    string      `json:"schedule"` // cron, пусто — только вручную
	Concurrency int         `json:"concurrency"`
	Archive     ArchiveOpts `json:"archive"`
	// Verify — сверка после загрузки: size | hash (по умолчанию) | full.
	Verify string `json:"verify,omitempty"`
	// Notify — уведомить DSM о результате запуска (synonotify).
	Notify bool `json:"notify,omitempty"`
}

// Settings — общие настройки.
type Settings struct {
	TmpDir string `json:"tmpDir"` // пусто — <data>/tmp
	// Globals — общие для всех аккаунтов поля (например, ID приложения Яндекса).
	Globals map[string]string `json:"globals,omitempty"`
	// LocalRoots — папки NAS, доступные в проводнике. Пусто — общие папки томов /volume*/ .
	LocalRoots []string `json:"localRoots,omitempty"`
	// Telemetry — согласие на отправку обезличенных отчётов об ошибках (nil — ещё не спрашивали).
	Telemetry *bool `json:"telemetry,omitempty"`
	// InstallID — случайный анонимный идентификатор установки для отчётов.
	InstallID string `json:"installId,omitempty"`
}

// Config — содержимое config.json.
type Config struct {
	Version         int       `json:"version"`
	PasswordHash    string    `json:"passwordHash"`
	GatewayPassword string    `json:"gatewayPassword"`
	Settings        Settings  `json:"settings"`
	Accounts        []Account `json:"accounts"`
	Jobs            []Job     `json:"jobs"`
}

// Store — потокобезопасное хранилище конфигурации.
type Store struct {
	mu         sync.RWMutex
	path       string
	cfg        Config
	box        *secret.Box
	secretKeys map[string]bool
}

// NewID возвращает случайный идентификатор.
func NewID() string { return auth.RandomToken(6) }

// Open читает конфигурацию или создаёт пустую.
func Open(dir string, box *secret.Box, secretKeys []string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "config.json"), box: box, secretKeys: map[string]bool{}}
	for _, k := range secretKeys {
		s.secretKeys[k] = true
	}
	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.cfg = Config{Version: 1, GatewayPassword: auth.RandomToken(12)}
		return s, s.save(s.cfg)
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.cfg); err != nil {
		return nil, fmt.Errorf("config.json повреждён: %w", err)
	}
	var oerr error
	if s.cfg.GatewayPassword, oerr = box.Open(s.cfg.GatewayPassword); oerr != nil {
		return nil, oerr
	}
	for i := range s.cfg.Accounts {
		for k, v := range s.cfg.Accounts[i].Fields {
			if s.secretKeys[k] {
				pt, err := box.Open(v)
				if err != nil {
					return nil, fmt.Errorf("аккаунт %q: %w", s.cfg.Accounts[i].Name, err)
				}
				s.cfg.Accounts[i].Fields[k] = pt
			}
		}
	}
	if s.cfg.GatewayPassword == "" {
		s.cfg.GatewayPassword = auth.RandomToken(12)
		_ = s.save(s.cfg)
	}
	return s, nil
}

func clone(c Config) Config {
	b, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return out
}

// Get возвращает копию конфигурации.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cfg)
}

// Update применяет изменение к копии и сохраняет, если fn не вернула ошибку.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	work := clone(s.cfg)
	if err := fn(&work); err != nil {
		return err
	}
	if err := s.save(work); err != nil {
		return err
	}
	s.cfg = work
	return nil
}

func (s *Store) save(c Config) error {
	disk := clone(c)
	disk.GatewayPassword = s.box.Seal(disk.GatewayPassword)
	for i := range disk.Accounts {
		for k, v := range disk.Accounts[i].Fields {
			if s.secretKeys[k] {
				disk.Accounts[i].Fields[k] = s.box.Seal(v)
			}
		}
	}
	raw, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Account ищет аккаунт по ID.
func (c Config) Account(id string) (Account, bool) {
	for _, a := range c.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

// Job ищет задание по ID.
func (c Config) Job(id string) (Job, bool) {
	for _, j := range c.Jobs {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}
