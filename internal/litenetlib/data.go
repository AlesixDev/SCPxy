package litenetlib

import (
	"encoding/binary"
	"errors"
	"math"
)

const stringBufferMaxLength = 65535

var (
	ErrTruncated  = errors.New("litenetlib: truncated data")
	ErrStringSize = errors.New("litenetlib: string too long")
)

type Writer struct {
	buf []byte
}

func NewWriter() *Writer {
	return &Writer{buf: make([]byte, 0, 128)}
}

func NewWriterFrom(data []byte) *Writer {
	out := make([]byte, len(data))
	copy(out, data)

	return &Writer{buf: out}
}

func (w *Writer) Bytes() []byte {
	return w.buf
}

func (w *Writer) Len() int {
	return len(w.buf)
}

func (w *Writer) PutByte(v byte) {
	w.buf = append(w.buf, v)
}

func (w *Writer) PutBool(v bool) {
	if v {
		w.PutByte(1)
		return
	}

	w.PutByte(0)
}

func (w *Writer) PutUint16(v uint16) {
	w.buf = binary.LittleEndian.AppendUint16(w.buf, v)
}

func (w *Writer) PutInt32(v int32) {
	w.buf = binary.LittleEndian.AppendUint32(w.buf, uint32(v))
}

func (w *Writer) PutInt64(v int64) {
	w.buf = binary.LittleEndian.AppendUint64(w.buf, uint64(v))
}

func (w *Writer) PutRaw(v []byte) {
	w.buf = append(w.buf, v...)
}

func (w *Writer) PutBytesWithLength(v []byte) {
	w.PutInt32(int32(len(v)))
	w.PutRaw(v)
}

func (w *Writer) PutString(v string) {
	if v == "" {
		w.PutUint16(0)
		return
	}

	raw := []byte(v)

	if len(raw) >= stringBufferMaxLength {
		w.PutUint16(0)
		return
	}

	w.PutUint16(uint16(len(raw) + 1))
	w.PutRaw(raw)
}

type Reader struct {
	buf []byte
	pos int
}

func NewReader(data []byte) *Reader {
	return &Reader{buf: data}
}

func (r *Reader) Position() int {
	return r.pos
}

func (r *Reader) Remaining() int {
	return len(r.buf) - r.pos
}

func (r *Reader) Rest() []byte {
	return r.buf[r.pos:]
}

func (r *Reader) take(n int) ([]byte, error) {
	if n < 0 || r.Remaining() < n {
		return nil, ErrTruncated
	}

	out := r.buf[r.pos : r.pos+n]
	r.pos += n

	return out, nil
}

func (r *Reader) Byte() (byte, error) {
	b, err := r.take(1)

	if err != nil {
		return 0, err
	}

	return b[0], nil
}

func (r *Reader) Bool() (bool, error) {
	b, err := r.Byte()

	if err != nil {
		return false, err
	}

	return b != 0, nil
}

func (r *Reader) Uint16() (uint16, error) {
	b, err := r.take(2)

	if err != nil {
		return 0, err
	}

	return binary.LittleEndian.Uint16(b), nil
}

func (r *Reader) Int32() (int32, error) {
	b, err := r.take(4)

	if err != nil {
		return 0, err
	}

	return int32(binary.LittleEndian.Uint32(b)), nil
}

func (r *Reader) Int64() (int64, error) {
	b, err := r.take(8)

	if err != nil {
		return 0, err
	}

	return int64(binary.LittleEndian.Uint64(b)), nil
}

func (r *Reader) BytesWithLength() ([]byte, error) {
	n, err := r.Int32()

	if err != nil {
		return nil, err
	}

	if n < 0 || int64(n) > int64(math.MaxInt32) {
		return nil, ErrTruncated
	}

	return r.take(int(n))
}

func (r *Reader) String() (string, error) {
	size, err := r.Uint16()

	if err != nil {
		return "", err
	}

	if size == 0 {
		return "", nil
	}

	if int(size)-1 >= stringBufferMaxLength {
		return "", ErrStringSize
	}

	raw, err := r.take(int(size) - 1)

	if err != nil {
		return "", err
	}

	return string(raw), nil
}
