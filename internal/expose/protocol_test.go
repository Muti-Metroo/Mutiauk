package expose

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		typ     FrameType
		stream  uint64
		payload []byte
	}{
		{"register", FrameRegister, 0, []byte(`{"forwards":[{"key":"a"}]}`)},
		{"open", FrameOpen, 42, []byte(`{"key":"a","remote_addr":"1.2.3.4:5"}`)},
		{"data large", FrameData, 7, bytes.Repeat([]byte{0xAB}, 16384)},
		{"data empty", FrameData, 7, nil},
		{"close", FrameClose, 99, nil},
		{"ping", FramePing, 0, []byte{0, 0, 0, 0, 0, 0, 0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := EncodeFrame(tc.typ, tc.stream, tc.payload)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := DecodeFrame(frame)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Type != tc.typ {
				t.Errorf("type: got %s want %s", got.Type, tc.typ)
			}
			if got.StreamID != tc.stream {
				t.Errorf("stream: got %d want %d", got.StreamID, tc.stream)
			}
			if !bytes.Equal(got.Payload, tc.payload) {
				t.Errorf("payload mismatch (len got=%d want=%d)", len(got.Payload), len(tc.payload))
			}
		})
	}
}

func TestEncodeRejectsOversizedPayload(t *testing.T) {
	_, err := EncodeFrame(FrameData, 1, bytes.Repeat([]byte{0}, MaxPayloadSize+1))
	if err == nil {
		t.Fatal("expected error for oversized payload")
	}
}

func TestDecodeRejectsTooShort(t *testing.T) {
	_, err := DecodeFrame([]byte{0x01, 0x00})
	if err == nil {
		t.Fatal("expected error for short frame")
	}
}

func TestMarshalJSONFrameProducesValidJSON(t *testing.T) {
	frame, err := MarshalJSONFrame(FrameRegister, 0, RegisterPayload{
		Forwards: []RegisterEntry{{Key: "k1", Comment: "hi"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var rp RegisterPayload
	if err := json.Unmarshal(got.Payload, &rp); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if len(rp.Forwards) != 1 || rp.Forwards[0].Key != "k1" {
		t.Errorf("decoded payload mismatch: %+v", rp)
	}
}

func TestFrameTypeString(t *testing.T) {
	cases := map[FrameType]string{
		FrameRegister:   "REGISTER",
		FrameOpen:       "OPEN",
		FrameData:       "DATA",
		FrameClose:      "CLOSE",
		FramePing:       "PING",
		FramePong:       "PONG",
		FrameType(0xCC): "0xcc",
	}
	for typ, want := range cases {
		got := typ.String()
		if !strings.EqualFold(got, want) {
			t.Errorf("FrameType(%v).String(): got %q want %q", uint8(typ), got, want)
		}
	}
}
