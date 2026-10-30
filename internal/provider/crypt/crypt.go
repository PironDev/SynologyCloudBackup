// Package crypt — прозрачное шифрование файлов на стороне клиента поверх любого провайдера.
//
// Формат файла (суффикс .cbenc): "CBE1" | соль файла (16 байт) | блоки AES-256-GCM.
// Каждый блок — до 64 КиБ открытого текста + 16 байт тега. Ключ файла = HKDF(мастер-ключ, соль файла),
// мастер-ключ = PBKDF2-HMAC-SHA256(пароль). Номер блока входит в nonce, признак «последний блок» — в AAD,
// поэтому перестановка, обрезка и подмена блоков обнаруживаются. Имена файлов и структура папок НЕ шифруются.
package crypt

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"nascloud/internal/provider"
)

const (
	// Suffix добавляется к имени зашифрованного файла в облаке.
	Suffix  = ".cbenc"
	magic   = "CBE1"
	saltLen = 16
	hdrLen  = len(magic) + saltLen
	chunk   = 64 << 10
	tagLen  = 16
	kdfSalt = "nascloud-cbe-v1"
	kdfIter = 200000
)

// ErrDecrypt — неверный пароль или повреждённые данные.
var ErrDecrypt = errors.New("не удалось расшифровать файл: неверный пароль шифрования или файл повреждён")

// EncSize — размер зашифрованного файла по размеру открытого.
func EncSize(plain int64) int64 {
	n := (plain + chunk - 1) / chunk
	if n < 1 {
		n = 1
	}
	return int64(hdrLen) + plain + n*tagLen
}

// PlainSize — размер открытого текста по размеру зашифрованного файла.
func PlainSize(enc int64) (int64, error) {
	body := enc - int64(hdrLen)
	if body < tagLen {
		return 0, ErrDecrypt
	}
	full := int64(chunk + tagLen)
	n := (body + full - 1) / full
	plain := body - n*tagLen
	if plain < 0 {
		return 0, ErrDecrypt
	}
	return plain, nil
}

// FS — провайдер-обёртка.
type FS struct {
	in     provider.Provider
	master []byte
}

// Wrap оборачивает провайдера. Пароль не должен быть пустым.
func Wrap(in provider.Provider, password string) (*FS, error) {
	mk, err := pbkdf2.Key(sha256.New, password, []byte(kdfSalt), kdfIter, 32)
	if err != nil {
		return nil, err
	}
	return &FS{in: in, master: mk}, nil
}

func (f *FS) aead(salt []byte) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, f.master, salt, "cbe-file-key", 32)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func nonce(i uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], i)
	return n
}

// ---------- шифрование потока ----------

type encReader struct {
	src      io.ReadCloser
	aead     cipher.AEAD
	out      []byte
	cur, nxt []byte
	curN     int
	counter  uint64
	started  bool
	done     bool
}

func fill(r io.Reader, b []byte) (int, error) {
	n, err := io.ReadFull(r, b)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil
	}
	return n, err
}

func (e *encReader) Read(p []byte) (int, error) {
	for len(e.out) == 0 {
		if e.done {
			return 0, io.EOF
		}
		if !e.started {
			e.started = true
			n, err := fill(e.src, e.cur)
			if err != nil {
				return 0, err
			}
			e.curN = n
		}
		nxtN := 0
		if e.curN == chunk {
			n, err := fill(e.src, e.nxt)
			if err != nil {
				return 0, err
			}
			nxtN = n
		}
		final := nxtN == 0
		aad := []byte{0}
		if final {
			aad[0] = 1
		}
		e.out = e.aead.Seal(nil, nonce(e.counter), e.cur[:e.curN], aad)
		e.counter++
		if final {
			e.done = true
		} else {
			e.cur, e.nxt = e.nxt, e.cur
			e.curN = nxtN
		}
	}
	n := copy(p, e.out)
	e.out = e.out[n:]
	return n, nil
}

func (e *encReader) Close() error { return e.src.Close() }

type multiRC struct {
	io.Reader
	c io.Closer
}

func (m multiRC) Close() error { return m.c.Close() }

// encrypt шифрует поток с заданной солью файла. Соль выбирается один раз на загрузку,
// поэтому повторное открытие источника (повторные попытки, нарезка на части) даёт тот же шифртекст.
func (f *FS) encrypt(src io.ReadCloser, salt []byte) (io.ReadCloser, error) {
	a, err := f.aead(salt)
	if err != nil {
		src.Close()
		return nil, err
	}
	hdr := append([]byte(magic), salt...)
	er := &encReader{src: src, aead: a, cur: make([]byte, chunk), nxt: make([]byte, chunk), out: nil}
	return multiRC{io.MultiReader(strings.NewReader(string(hdr)), er), src}, nil
}

// ---------- расшифровка ----------

type decReader struct {
	src   io.ReadCloser
	aead  cipher.AEAD
	idx   uint64
	total uint64 // число блоков
	skip  int    // сколько байт пропустить в первом блоке
	buf   []byte
	left  int64 // сколько ещё отдать (-1 — без ограничения)
	err   error
}

func (d *decReader) Read(p []byte) (int, error) {
	if d.left == 0 {
		return 0, io.EOF
	}
	for len(d.buf) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.left == 0 || d.idx >= d.total {
			return 0, io.EOF
		}
		ct := make([]byte, chunk+tagLen)
		n, err := io.ReadFull(d.src, ct)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			d.err = err
			continue
		}
		if n < tagLen {
			d.err = ErrDecrypt
			continue
		}
		aad := []byte{0}
		if d.idx == d.total-1 {
			aad[0] = 1
		}
		pt, oerr := d.aead.Open(nil, nonce(d.idx), ct[:n], aad)
		if oerr != nil {
			d.err = ErrDecrypt
			continue
		}
		d.idx++
		if d.skip > 0 {
			if d.skip > len(pt) {
				d.skip = len(pt)
			}
			pt = pt[d.skip:]
			d.skip = 0
		}
		d.buf = pt
	}
	b := d.buf
	if d.left >= 0 && int64(len(b)) > d.left {
		b = b[:d.left]
	}
	n := copy(p, b)
	d.buf = d.buf[n:]
	if d.left >= 0 {
		d.left -= int64(n)
	}
	return n, nil
}

func (d *decReader) Close() error { return d.src.Close() }

// ---------- провайдер ----------

func (f *FS) Check(ctx context.Context) (provider.AccountInfo, error) { return f.in.Check(ctx) }

func plainEntry(e provider.Entry) provider.Entry {
	if e.IsDir || !strings.HasSuffix(e.Name, Suffix) {
		return e
	}
	if sz, err := PlainSize(e.Size); err == nil {
		e.Size = sz
	}
	e.Name = strings.TrimSuffix(e.Name, Suffix)
	e.Path = strings.TrimSuffix(e.Path, Suffix)
	e.MD5 = ""
	return e
}

func (f *FS) List(ctx context.Context, dir string) ([]provider.Entry, error) {
	es, err := f.in.List(ctx, dir)
	for i := range es {
		es[i] = plainEntry(es[i])
	}
	return es, err
}

// real определяет, под каким именем файл лежит в облаке.
func (f *FS) real(ctx context.Context, p string) (provider.Entry, bool, error) {
	p = provider.Clean(p)
	if p != "/" {
		e, err := f.in.Stat(ctx, p+Suffix)
		if err == nil && !e.IsDir {
			return e, true, nil
		}
		if err != nil && !errors.Is(err, provider.ErrNotFound) {
			return provider.Entry{}, false, err
		}
	}
	e, err := f.in.Stat(ctx, p)
	return e, false, err
}

func (f *FS) Stat(ctx context.Context, p string) (provider.Entry, error) {
	e, enc, err := f.real(ctx, p)
	if err != nil {
		return provider.Entry{}, err
	}
	if enc {
		return plainEntry(e), nil
	}
	return e, nil
}

func (f *FS) Mkdir(ctx context.Context, p string) error { return f.in.Mkdir(ctx, p) }

// UploadVerified шифрует поток и загружает его со сверкой (хеш считается по зашифрованным данным).
func (f *FS) UploadVerified(ctx context.Context, p string, src provider.Source, mode string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	enc := provider.Source{Size: EncSize(src.Size), ModTime: src.ModTime, Open: func() (io.ReadCloser, error) {
		rc, err := src.Open()
		if err != nil {
			return nil, err
		}
		return f.encrypt(rc, salt)
	}}
	return provider.UploadVerified(ctx, f.in, provider.Clean(p)+Suffix, enc, mode)
}

func (f *FS) Upload(ctx context.Context, p string, src provider.Source) error {
	_, err := f.UploadVerified(ctx, p, src, provider.VerifySizeOnly)
	return err
}

func (f *FS) Download(ctx context.Context, p string, offset, length int64) (io.ReadCloser, error) {
	e, enc, err := f.real(ctx, p)
	if err != nil {
		return nil, err
	}
	if !enc {
		return f.in.Download(ctx, p, offset, length)
	}
	plain, err := PlainSize(e.Size)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= plain {
		return io.NopCloser(strings.NewReader("")), nil
	}
	rp := provider.Clean(p) + Suffix
	h, err := f.in.Download(ctx, rp, 0, int64(hdrLen))
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, hdrLen)
	_, err = io.ReadFull(h, hdr)
	h.Close()
	if err != nil || string(hdr[:len(magic)]) != magic {
		return nil, ErrDecrypt
	}
	a, err := f.aead(hdr[len(magic):])
	if err != nil {
		return nil, err
	}
	first := uint64(offset / chunk)
	total := uint64((plain + chunk - 1) / chunk)
	if total == 0 {
		total = 1
	}
	body, err := f.in.Download(ctx, rp, int64(hdrLen)+int64(first)*int64(chunk+tagLen), -1)
	if err != nil {
		return nil, err
	}
	left := int64(-1)
	if length >= 0 {
		left = length
	}
	return &decReader{src: body, aead: a, idx: first, total: total, skip: int(offset % chunk), left: left}, nil
}

func (f *FS) Delete(ctx context.Context, p string) error {
	_, enc, err := f.real(ctx, p)
	if err != nil {
		return err
	}
	if enc {
		return f.in.Delete(ctx, provider.Clean(p)+Suffix)
	}
	return f.in.Delete(ctx, p)
}

func (f *FS) moveCopy(ctx context.Context, from, to string, overwrite, move bool) error {
	_, enc, err := f.real(ctx, from)
	if err != nil {
		return err
	}
	if enc {
		from, to = provider.Clean(from)+Suffix, provider.Clean(to)+Suffix
	}
	if move {
		return f.in.Move(ctx, from, to, overwrite)
	}
	return f.in.Copy(ctx, from, to, overwrite)
}

func (f *FS) Move(ctx context.Context, from, to string, overwrite bool) error {
	return f.moveCopy(ctx, from, to, overwrite, true)
}

func (f *FS) Copy(ctx context.Context, from, to string, overwrite bool) error {
	return f.moveCopy(ctx, from, to, overwrite, false)
}

var _ provider.Provider = (*FS)(nil)
