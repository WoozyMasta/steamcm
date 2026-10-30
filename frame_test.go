// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	want := Frame{
		EMsg:   6403,
		Proto:  true,
		Header: []byte{0x08, 0x01, 0x50, 0x02},
		Body:   []byte{0x08, 0xea, 0x0c},
	}

	encoded, err := EncodeFrame(want, FrameLimits{})
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}

	wantWire := []byte{
		0x03, 0x19, 0x00, 0x80,
		0x04, 0x00, 0x00, 0x00,
		0x08, 0x01, 0x50, 0x02,
		0x08, 0xea, 0x0c,
	}
	if !bytes.Equal(encoded, wantWire) {
		t.Fatalf("EncodeFrame() = %x, want %x", encoded, wantWire)
	}

	got, err := DecodeFrame(encoded, FrameLimits{})
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}
	if got.EMsg != want.EMsg ||
		!got.Proto ||
		!bytes.Equal(got.Header, want.Header) ||
		!bytes.Equal(got.Body, want.Body) {
		t.Fatalf("DecodeFrame() = %#v, want %#v", got, want)
	}
}

func TestDecodeFrameRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		limits FrameLimits
	}{
		{
			name:  "truncated",
			input: []byte{1, 2, 3},
		},
		{
			name:  "missing protobuf flag",
			input: []byte{3, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			name:  "truncated header",
			input: []byte{3, 0, 0, 0x80, 4, 0, 0, 0, 1},
		},
		{
			name:   "header limit",
			input:  []byte{3, 0, 0, 0x80, 4, 0, 0, 0, 1, 2, 3, 4},
			limits: FrameLimits{MaxHeaderSize: 3},
		},
		{
			name:   "frame limit",
			input:  make([]byte, 12),
			limits: FrameLimits{MaxFrameSize: 8},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeFrame(tt.input, tt.limits); err == nil {
				t.Fatal("DecodeFrame() error = nil, want error")
			}
		})
	}
}

func TestDecodeLegacyFrameFormats(t *testing.T) {
	t.Run("extended client header", func(t *testing.T) {
		encoded := make([]byte, 39)
		binary.LittleEndian.PutUint32(encoded[0:4], EMsgClientLoggedOff)
		encoded[4] = 36

		binary.LittleEndian.PutUint16(encoded[5:7], 2)
		binary.LittleEndian.PutUint64(encoded[7:15], 101)
		binary.LittleEndian.PutUint64(encoded[15:23], 202)
		encoded[23] = 0xef

		binary.LittleEndian.PutUint64(encoded[24:32], 303)
		binary.LittleEndian.PutUint32(encoded[32:36], 0xfffffff0)
		copy(encoded[36:], []byte{1, 2, 3})

		frame, err := DecodeFrame(encoded, FrameLimits{})
		if err != nil {
			t.Fatalf("DecodeFrame() error = %v", err)
		}

		message, err := decodeMessage(frame)
		if err != nil {
			t.Fatalf("decodeMessage() error = %v", err)
		}

		if frame.Proto ||
			message.EMsg != EMsgClientLoggedOff ||
			message.Header.JobIDTarget != 101 ||
			message.Header.JobIDSource != 202 ||
			message.Header.SteamID != 303 ||
			message.Header.ClientSessionID != -16 ||
			!bytes.Equal(message.Body, []byte{1, 2, 3}) {
			t.Fatalf("decoded legacy frame = %#v / %#v", frame, message)
		}
	})

	t.Run("basic header", func(t *testing.T) {
		encoded := make([]byte, 22)
		binary.LittleEndian.PutUint32(encoded[0:4], EMsgClientHeartBeat)
		binary.LittleEndian.PutUint64(encoded[4:12], 11)
		binary.LittleEndian.PutUint64(encoded[12:20], 22)
		copy(encoded[20:], []byte{7, 8})

		frame, err := DecodeFrame(encoded, FrameLimits{})
		if err != nil {
			t.Fatalf("DecodeFrame() error = %v", err)
		}

		message, err := decodeMessage(frame)
		if err != nil || frame.Proto || message.Header.JobIDTarget != 11 ||
			message.Header.JobIDSource != 22 || !bytes.Equal(message.Body, []byte{7, 8}) {
			t.Fatalf("decoded basic legacy frame = %#v / %#v, %v", frame, message, err)
		}
	})
}

func TestDecodeLegacyFrameRejectsInvalidExtendedHeader(t *testing.T) {
	encoded := make([]byte, 36)
	binary.LittleEndian.PutUint32(encoded[0:4], EMsgClientLoggedOff)
	encoded[4] = 36
	binary.LittleEndian.PutUint16(encoded[5:7], 2)
	if _, err := DecodeFrame(encoded, FrameLimits{}); err == nil {
		t.Fatal("DecodeFrame() error = nil, want invalid extended header canary")
	}
}

func TestEncodeFrameRejectsLimits(t *testing.T) {
	if _, err := EncodeFrame(
		Frame{
			EMsg:   1,
			Proto:  true,
			Header: make([]byte, 4),
		},
		FrameLimits{
			MaxHeaderSize: 3,
		},
	); err == nil {
		t.Fatal("EncodeFrame() error = nil, want header limit error")
	}

	if _, err := EncodeFrame(
		Frame{
			EMsg:  1,
			Proto: true,
			Body:  make([]byte, 5),
		},
		FrameLimits{
			MaxFrameSize: 8,
		}); err == nil {
		t.Fatal("EncodeFrame() error = nil, want frame limit error")
	}
}
