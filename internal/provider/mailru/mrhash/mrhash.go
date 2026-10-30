// Package mrhash считает хеш Облака Mail.ru — изменённый SHA-1. Порт
// backend/mailru/mrhash из rclone (лицензия MIT).
//
// Файл не длиннее SHA-1 блока (20 байт): хеш — его данные, дополненные нулями.
// Большой файл: SHA-1 от «mrCloud» + данные + десятичная длина данных.
package mrhash

import (
	"crypto/sha1"
	"encoding"
	"encoding/hex"
	"errors"
	"hash"
	"strconv"
)

// Size — длина хеша в байтах.
const Size = sha1.Size

const startString = "mrCloud"

// ErrInvalidHash возвращается для хеша неверной длины или формата.
var ErrInvalidHash = errors.New("неверный хеш Облака Mail.ru")

type digest struct {
	total int       // байт записано
	sha   hash.Hash // SHA-1 данных с префиксом
	small []byte    // первые байты, пока файл короче Size
}

// New возвращает hash.Hash, считающий хеш Облака Mail.ru.
func New() hash.Hash {
	d := &digest{}
	d.Reset()
	return d
}

// Write добавляет данные в хеш. Не возвращает ошибок.
func (d *digest) Write(p []byte) (int, error) {
	n, _ := d.sha.Write(p)
	d.total += n
	if d.total <= Size {
		d.small = append(d.small, p...)
	}
	return n, nil
}

// Sum дописывает хеш к b. Состояние хеша не меняется.
func (d *digest) Sum(b []byte) []byte {
	if d.total <= Size {
		padded := make([]byte, Size)
		copy(padded, d.small)
		return append(b, padded...)
	}
	clone := sha1.New()
	state, _ := d.sha.(encoding.BinaryMarshaler).MarshalBinary()
	_ = clone.(encoding.BinaryUnmarshaler).UnmarshalBinary(state)
	_, _ = clone.Write([]byte(strconv.Itoa(d.total)))
	return clone.Sum(b)
}

// Reset возвращает хеш в начальное состояние.
func (d *digest) Reset() {
	d.sha = sha1.New()
	_, _ = d.sha.Write([]byte(startString))
	d.total = 0
	d.small = nil
}

// Size возвращает длину хеша.
func (d *digest) Size() int { return Size }

// BlockSize возвращает размер блока SHA-1.
func (d *digest) BlockSize() int { return sha1.BlockSize }

// Sum возвращает хеш Облака Mail.ru для всех данных.
func Sum(data []byte) []byte {
	h := New()
	_, _ = h.Write(data)
	return h.Sum(nil)
}

// DecodeString переводит hex-строку в хеш. Неверная длина — ErrInvalidHash.
func DecodeString(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != Size {
		return nil, ErrInvalidHash
	}
	return b, nil
}

var _ hash.Hash = (*digest)(nil)
