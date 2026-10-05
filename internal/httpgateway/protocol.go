// Package httpgateway implements bounded HTTP exchanges on an owned byte stream.
package httpgateway

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	Version             = 1
	Start          byte = 1
	Body           byte = 2
	End            byte = 3
	Cancel         byte = 4
	ResponseStart  byte = 5
	ResponseBody   byte = 6
	ResponseEnd    byte = 7
	ResponseError  byte = 8
	UploadCredit   byte = 9
	DownloadCredit byte = 10
	Chunk               = 16 << 10
	Window              = 4 * Chunk
	MaxExchanges        = 16
	MaxMetadata         = Chunk
)

type Frame struct {
	Type byte
	ID   uint32
	Data []byte
}

func ReadFrame(r io.Reader) (Frame, error) {
	var header [10]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	n := binary.BigEndian.Uint32(header[6:])
	id := binary.BigEndian.Uint32(header[2:6])
	if header[0] != Version || n > Chunk || id == 0 {
		return Frame{}, errors.New("invalid HTTP tunnel frame")
	}
	f := Frame{Type: header[1], ID: id, Data: make([]byte, n)}
	_, err := io.ReadFull(r, f.Data)
	return f, err
}
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.Data) > Chunk || f.ID == 0 {
		return errors.New("invalid HTTP tunnel frame")
	}
	var header [10]byte
	header[0] = Version
	header[1] = f.Type
	binary.BigEndian.PutUint32(header[2:6], f.ID)
	binary.BigEndian.PutUint32(header[6:], uint32(len(f.Data)))
	for _, data := range [][]byte{header[:], f.Data} {
		for len(data) > 0 {
			n, err := w.Write(data)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			data = data[n:]
		}
	}
	return nil
}
func Credit(n int) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(n))
	return out
}
