package cloud

import (
	"bytes"
	"encoding/binary"
)

// Коды операций бинарного протокола метасервера.
const (
	opAddFile      = 103 // фиксирует загруженный файл по хешу
	opRename       = 105 // переименование или перемещение по полным путям
	opCreateFolder = 106
)

// binWriter пишет команду бинарного протокола: varint-числа и строки с нулём в конце.
type binWriter struct{ b bytes.Buffer }

func (w *binWriter) u64(v int64) {
	var a [binary.MaxVarintLen64]byte
	w.b.Write(a[:binary.PutUvarint(a[:], uint64(v))])
}

func (w *binWriter) str(s string) {
	w.u64(int64(len(s) + 1))
	w.b.WriteString(s)
	w.b.WriteByte(0)
}

func (w *binWriter) raw(p []byte) { w.b.Write(p) }
