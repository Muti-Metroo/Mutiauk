// Package expose publishes local services (folders or TCP targets) to
// the upstream Muti Metroo agent over a persistent multiplexed SOCKS5 stream
// (the CmdExpose extension).
//
// Wire protocol: each message is one frame.
//
//	[u8 type][u64 stream_id big-endian][payload...]
//
// Control frames use stream_id=0. JSON payloads are documented per
// frame type in the FrameType constants below. Only the server
// (Muti Metroo) opens streams; the client (Mutiauk) responds.
package expose

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// FrameType identifies the kind of frame on the wire.
type FrameType uint8

const (
	FrameRegister      FrameType = 0x01 // entry -> muti-metroo
	FrameRegisterAck   FrameType = 0x02 // muti-metroo -> entry
	FrameUnregister    FrameType = 0x03 // entry -> muti-metroo
	FrameUnregisterAck FrameType = 0x04 // muti-metroo -> entry
	FrameOpen          FrameType = 0x10 // muti-metroo -> entry
	FrameOpenAck       FrameType = 0x11 // entry -> muti-metroo
	FrameData          FrameType = 0x20 // both
	FrameCloseWrite    FrameType = 0x21 // both
	FrameClose         FrameType = 0x22 // both
	FramePing          FrameType = 0xF0 // both
	FramePong          FrameType = 0xF1 // both
)

// String returns a human-readable name for diagnostics. Unknown types
// print as their hex value.
func (t FrameType) String() string {
	switch t {
	case FrameRegister:
		return "REGISTER"
	case FrameRegisterAck:
		return "REGISTER_ACK"
	case FrameUnregister:
		return "UNREGISTER"
	case FrameUnregisterAck:
		return "UNREGISTER_ACK"
	case FrameOpen:
		return "OPEN"
	case FrameOpenAck:
		return "OPEN_ACK"
	case FrameData:
		return "DATA"
	case FrameCloseWrite:
		return "CLOSE_WRITE"
	case FrameClose:
		return "CLOSE"
	case FramePing:
		return "PING"
	case FramePong:
		return "PONG"
	default:
		return fmt.Sprintf("0x%02x", uint8(t))
	}
}

// HeaderSize is the fixed prefix length of every frame.
const HeaderSize = 1 + 8

// MaxPayloadSize caps a single frame payload at 1 MiB. The far side
// is expected to chunk larger writes; we reject oversize frames to
// bound buffer growth on each session.
const MaxPayloadSize = 1 << 20

// Frame is a decoded wire frame. Payload aliases the underlying read
// buffer; copy it before yielding control if the caller needs to keep
// it past the next read.
type Frame struct {
	Type     FrameType
	StreamID uint64
	Payload  []byte
}

// EncodeFrame serializes a frame to bytes suitable for a single
// message. Returns an error if payload exceeds MaxPayloadSize.
func EncodeFrame(typ FrameType, streamID uint64, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayloadSize {
		return nil, fmt.Errorf("expose: payload too large (%d > %d)", len(payload), MaxPayloadSize)
	}
	out := make([]byte, HeaderSize+len(payload))
	out[0] = byte(typ)
	binary.BigEndian.PutUint64(out[1:9], streamID)
	copy(out[HeaderSize:], payload)
	return out, nil
}

// DecodeFrame parses a single message. The returned Payload
// slice aliases data; the caller must not retain it past the next read.
func DecodeFrame(data []byte) (Frame, error) {
	if len(data) < HeaderSize {
		return Frame{}, errors.New("expose: frame too short")
	}
	if len(data)-HeaderSize > MaxPayloadSize {
		return Frame{}, fmt.Errorf("expose: payload too large (%d > %d)", len(data)-HeaderSize, MaxPayloadSize)
	}
	return Frame{
		Type:     FrameType(data[0]),
		StreamID: binary.BigEndian.Uint64(data[1:9]),
		Payload:  data[HeaderSize:],
	}, nil
}

// RegisterEntry is one item in a REGISTER payload. Comment is purely
// informational; Muti Metroo may surface it in its admin UI.
type RegisterEntry struct {
	Key     string `json:"key"`
	Comment string `json:"comment,omitempty"`
}

// RegisterPayload is the JSON body of a FrameRegister frame.
type RegisterPayload struct {
	Forwards []RegisterEntry `json:"forwards"`
}

// AckResult is one entry in a REGISTER_ACK or UNREGISTER_ACK payload.
type AckResult struct {
	Key   string `json:"key"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// AckPayload is the JSON body of REGISTER_ACK and UNREGISTER_ACK.
type AckPayload struct {
	Results []AckResult `json:"results"`
}

// UnregisterPayload is the JSON body of FrameUnregister.
type UnregisterPayload struct {
	Keys []string `json:"keys"`
}

// OpenPayload is the JSON body of FrameOpen sent by Muti Metroo to ask the
// client to accept a new stream for the given routing key. RemoteAddr
// is the upstream listener's view of the originating peer; the client
// uses it for logs only.
type OpenPayload struct {
	Key        string `json:"key"`
	RemoteAddr string `json:"remote_addr,omitempty"`
}

// OpenAckPayload is the JSON body of FrameOpenAck.
type OpenAckPayload struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// MarshalJSONFrame is a convenience for control frames whose payload
// is JSON. Returns the encoded frame ready to write.
func MarshalJSONFrame(typ FrameType, streamID uint64, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("expose: marshal %s payload: %w", typ, err)
	}
	return EncodeFrame(typ, streamID, body)
}
