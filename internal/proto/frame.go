package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	TypeHello  byte = 1
	TypePacket byte = 2
	TypePing   byte = 3
	TypePong   byte = 4
	TypeError  byte = 5
	TypeRoster byte = 6

	MaxPayload = 1 << 20
)

var magic = [4]byte{'M', 'S', 'H', '1'}

type Frame struct {
	Type    byte
	Payload []byte
}

func Write(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	var header [10]byte
	copy(header[0:4], magic[:])
	header[4] = 1
	header[5] = typ
	binary.BigEndian.PutUint32(header[6:10], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func Read(r io.Reader) (Frame, error) {
	var header [10]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	if [4]byte(header[0:4]) != magic {
		return Frame{}, errors.New("bad frame magic")
	}
	if header[4] != 1 {
		return Frame{}, fmt.Errorf("unsupported frame version %d", header[4])
	}
	n := binary.BigEndian.Uint32(header[6:10])
	if n > MaxPayload {
		return Frame{}, fmt.Errorf("payload too large: %d", n)
	}
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Type: header[5], Payload: payload}, nil
}
