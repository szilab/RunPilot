// Package guacd implements the small, bounded part of the Guacamole protocol
// RunPilot needs to negotiate an RDP connection with guacd.
package guacd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxElementLength = 1024 * 1024

type Instruction struct {
	Opcode string
	Args   []string
}

func EncodeInstruction(opcode string, args ...string) ([]byte, error) {
	if opcode == "" {
		return nil, errors.New("guacamole opcode is required")
	}
	parts := append([]string{opcode}, args...)
	return encodeElements(parts)
}

func encodeElements(parts []string) ([]byte, error) {
	var out strings.Builder
	for i, value := range parts {
		if len(value) > MaxElementLength || !utf8.ValidString(value) {
			return nil, errors.New("invalid guacamole element")
		}
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(strconv.Itoa(len(value)))
		out.WriteByte('.')
		out.WriteString(value)
	}
	out.WriteByte(';')
	return []byte(out.String()), nil
}

func WriteInstruction(w io.Writer, opcode string, args ...string) error {
	data, err := EncodeInstruction(opcode, args...)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func DecodeInstruction(r *bufio.Reader) (Instruction, error) {
	var values []string
	for {
		length, err := readLength(r)
		if err != nil {
			return Instruction{}, err
		}
		if length > MaxElementLength {
			return Instruction{}, errors.New("guacamole element exceeds limit")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(r, data); err != nil {
			return Instruction{}, fmt.Errorf("truncated guacamole element: %w", err)
		}
		if !utf8.Valid(data) {
			return Instruction{}, errors.New("guacamole element is not UTF-8")
		}
		separator, err := r.ReadByte()
		if err != nil {
			return Instruction{}, fmt.Errorf("truncated guacamole instruction: %w", err)
		}
		if separator != ',' && separator != ';' {
			return Instruction{}, errors.New("invalid guacamole separator")
		}
		values = append(values, string(data))
		if separator == ';' {
			break
		}
	}
	if len(values) == 0 {
		return Instruction{}, errors.New("guacamole opcode is required")
	}
	return Instruction{Opcode: values[0], Args: values[1:]}, nil
}

// TunnelReady returns Guacamole's WebSocket tunnel control instruction. It is
// consumed by guacamole-common-js and must never be forwarded to guacd.
func TunnelReady(id string) ([]byte, error) {
	if id == "" || len(id) > MaxElementLength || !utf8.ValidString(id) {
		return nil, errors.New("invalid Guacamole tunnel ID")
	}
	return encodeElements([]string{"", id})
}

// TunnelKeepalive returns a Guacamole WebSocket control instruction. The
// browser consumes it as tunnel activity and does not forward it to guacd.
func TunnelKeepalive() ([]byte, error) {
	return encodeElements([]string{"", "ping", "server"})
}

// ForwardClientInstructions forwards normal browser instructions to guacd and
// consumes the empty-opcode UUID control. It returns any ping controls that
// must be echoed to the browser to keep its WebSocket tunnel alive.
func ForwardClientInstructions(data []byte, destination io.Writer) ([]byte, error) {
	reader := bufio.NewReader(bytes.NewReader(data))
	var controls bytes.Buffer
	for {
		// Buffered() only reports bytes already fetched from the underlying
		// reader. A new bufio.Reader backed by a WebSocket message starts at
		// zero even when that message contains data, so probe it instead.
		if _, err := reader.Peek(1); err != nil {
			if errors.Is(err, io.EOF) {
				return controls.Bytes(), nil
			}
			return nil, err
		}
		instruction, err := DecodeInstruction(reader)
		if err != nil {
			return nil, err
		}
		if instruction.Opcode == "" {
			if len(instruction.Args) > 0 && instruction.Args[0] == "ping" {
				reply, err := encodeElements(append([]string{""}, instruction.Args...))
				if err != nil {
					return nil, err
				}
				controls.Write(reply)
			}
			continue
		}
		encoded, err := EncodeInstruction(instruction.Opcode, instruction.Args...)
		if err != nil {
			return nil, err
		}
		if _, err := destination.Write(encoded); err != nil {
			return nil, err
		}
	}
}

func readLength(r *bufio.Reader) (int, error) {
	var digits strings.Builder
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b == '.' {
			break
		}
		if b < '0' || b > '9' || digits.Len() >= 8 {
			return 0, errors.New("invalid guacamole element length")
		}
		digits.WriteByte(b)
	}
	if digits.Len() == 0 {
		return 0, errors.New("missing guacamole element length")
	}
	length, err := strconv.Atoi(digits.String())
	if err != nil {
		return 0, errors.New("invalid guacamole element length")
	}
	return length, nil
}
