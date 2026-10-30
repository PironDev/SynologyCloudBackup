package mrhash

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"testing"
)

func TestSmallFileIsPadded(t *testing.T) {
	got := Sum([]byte("abc"))
	want := append([]byte("abc"), make([]byte, Size-3)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("короткий файл: %x, ожидалось %x", got, want)
	}
}

func TestLargeFileIsSHA1OfPrefixAndLength(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 5) // 50 байт — больше Size
	h := sha1.New()
	h.Write([]byte("mrCloud"))
	h.Write(data)
	h.Write([]byte(strconv.Itoa(len(data))))
	if got, want := Sum(data), h.Sum(nil); !bytes.Equal(got, want) {
		t.Fatalf("большой файл: %x, ожидалось %x", got, want)
	}
}

func TestStreamingMatchesOneShot(t *testing.T) {
	data := bytes.Repeat([]byte("streaming"), 100)
	h := New()
	for i := 0; i < len(data); i += 7 {
		end := min(i+7, len(data))
		h.Write(data[i:end])
	}
	if !bytes.Equal(h.Sum(nil), Sum(data)) {
		t.Fatal("потоковый хеш отличается от разового")
	}
}

func TestDecodeString(t *testing.T) {
	if _, err := DecodeString(hex.EncodeToString(make([]byte, Size))); err != nil {
		t.Fatalf("корректный хеш отклонён: %v", err)
	}
	if _, err := DecodeString("zz"); err != ErrInvalidHash {
		t.Fatalf("ожидался ErrInvalidHash, получено %v", err)
	}
}
