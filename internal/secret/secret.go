// Package secret шифрует секреты в конфиге (AES-256-GCM, ключ в файле).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "enc1:"

// Box шифрует и расшифровывает строки.
type Box struct{ aead cipher.AEAD }

// LoadOrCreate читает ключ из файла или создаёт новый (права 0600).
func LoadOrCreate(keyPath string) (*Box, error) {
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(keyPath, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("повреждён файл ключа " + keyPath)
	}
	return New(key)
}

// New создаёт Box из 32-байтного ключа.
func New(key []byte) (*Box, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Box{aead: g}, nil
}

// Seal шифрует строку. Пустая строка остаётся пустой.
func (b *Box) Seal(plain string) string {
	if plain == "" || strings.HasPrefix(plain, prefix) {
		return plain
	}
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce)
	ct := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(ct)
}

// Open расшифровывает строку; значение без префикса возвращается как есть.
func (b *Box) Open(s string) (string, error) {
	if !strings.HasPrefix(s, prefix) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("секрет повреждён")
	}
	pt, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("не удалось расшифровать секрет (изменился secret.key?)")
	}
	return string(pt), nil
}
