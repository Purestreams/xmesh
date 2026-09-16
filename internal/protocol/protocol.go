package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	Version            = 2
	SMuxVersion        = 1
	MaxControlFrame    = 64 << 10
	MaxDatagramPayload = 4 << 10
	MaxDatagramFrame   = MaxDatagramPayload + MaxControlFrame + 2
)

type Type string

const (
	TypeRegister Type = "register"
	TypeReady    Type = "ready"
	TypeTCP      Type = "tcp_connect"
	TypeUDP      Type = "udp_associate"
	TypeResponse Type = "response"
	TypePing     Type = "ping"
	TypePong     Type = "pong"
)

type Message struct {
	Type       Type   `json:"type"`
	Version    int    `json:"version"`
	AgentID    string `json:"agent_id,omitempty"`
	LinkID     string `json:"link_id,omitempty"`
	Token      string `json:"token,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	GrantID    string `json:"grant_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	Success    bool   `json:"success,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

func WriteMessage(w io.Writer, message Message) error {
	b, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode control message: %w", err)
	}
	if len(b) > MaxControlFrame {
		return errors.New("control message exceeds limit")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(b)))
	if err := writeAll(w, size[:]); err != nil {
		return err
	}
	return writeAll(w, b)
}

func ReadMessage(r io.Reader) (Message, error) {
	var message Message
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return message, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > MaxControlFrame {
		return message, fmt.Errorf("invalid control frame size %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return message, err
	}
	if err := json.Unmarshal(b, &message); err != nil {
		return message, fmt.Errorf("decode control message: %w", err)
	}
	return message, nil
}

type Datagram struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Payload []byte `json:"-"`
}

func WriteDatagram(w io.Writer, datagram Datagram) error {
	if datagram.Port < 1 || datagram.Port > 65535 || datagram.Host == "" {
		return errors.New("invalid datagram target")
	}
	if len(datagram.Payload) > MaxDatagramPayload {
		return errors.New("datagram payload exceeds limit")
	}
	meta, err := json.Marshal(struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}{datagram.Host, datagram.Port})
	if err != nil {
		return err
	}
	if len(meta) > 65535 {
		return errors.New("datagram metadata exceeds limit")
	}
	total := 2 + len(meta) + len(datagram.Payload)
	var header [6]byte
	binary.BigEndian.PutUint32(header[:4], uint32(total))
	binary.BigEndian.PutUint16(header[4:], uint16(len(meta)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	if err := writeAll(w, meta); err != nil {
		return err
	}
	return writeAll(w, datagram.Payload)
}

func ReadDatagram(r io.Reader) (Datagram, error) {
	var datagram Datagram
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return datagram, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n < 2 || n > MaxDatagramFrame {
		return datagram, fmt.Errorf("invalid datagram frame size %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return datagram, err
	}
	metaSize := int(binary.BigEndian.Uint16(b[:2]))
	if metaSize == 0 || metaSize > len(b)-2 {
		return datagram, errors.New("invalid datagram metadata size")
	}
	if err := json.Unmarshal(b[2:2+metaSize], &datagram); err != nil {
		return datagram, fmt.Errorf("decode datagram metadata: %w", err)
	}
	if datagram.Host == "" || datagram.Port < 1 || datagram.Port > 65535 {
		return datagram, errors.New("invalid datagram endpoint")
	}
	datagram.Payload = b[2+metaSize:]
	return datagram, nil
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
