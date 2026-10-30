// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// protobufMessageFlag marks an EMsg envelope as using protobuf headers.
const protobufMessageFlag uint32 = 1 << 31

const (
	// defaultMaxFrameSize caps one decoded CM frame.
	defaultMaxFrameSize = 16 << 20
	// defaultMaxHeaderSize caps the decoded CM header section.
	defaultMaxHeaderSize = 64 << 10
	// maxUint32Int64 is the largest uint32 value represented as int64.
	maxUint32Int64 = int64(1<<32 - 1)
)

// Frame is one CM message after WebSocket framing has been removed.
type Frame struct {
	Header []byte
	Body   []byte
	EMsg   uint32
	Proto  bool
}

// FrameLimits bounds allocations performed by DecodeFrame.
type FrameLimits struct {
	MaxFrameSize  int
	MaxHeaderSize int
}

// withDefaults fills unset limits without changing explicit positive caps.
func (l FrameLimits) withDefaults() FrameLimits {
	if l.MaxFrameSize <= 0 {
		l.MaxFrameSize = defaultMaxFrameSize
	}
	if l.MaxHeaderSize <= 0 {
		l.MaxHeaderSize = defaultMaxHeaderSize
	}

	return l
}

// EncodeFrame serializes a CM frame using the little-endian CM envelope.
func EncodeFrame(frame Frame, limits FrameLimits) ([]byte, error) {
	limits = limits.withDefaults()

	if frame.EMsg&protobufMessageFlag != 0 {
		return nil, errors.New("CM EMsg must not contain the protobuf flag")
	}
	if frame.EMsg > ^protobufMessageFlag {
		return nil, fmt.Errorf("CM EMsg is out of range: %d", frame.EMsg)
	}
	if len(frame.Header) > limits.MaxHeaderSize {
		return nil, fmt.Errorf("CM header exceeds %d bytes", limits.MaxHeaderSize)
	}
	if int64(len(frame.Header)) > maxUint32Int64 {
		return nil, errors.New("CM header length exceeds uint32")
	}

	totalSize := 8 + len(frame.Header) + len(frame.Body)
	if totalSize > limits.MaxFrameSize {
		return nil, fmt.Errorf("CM frame exceeds %d bytes", limits.MaxFrameSize)
	}

	encoded := make([]byte, totalSize)
	emsg := frame.EMsg
	if frame.Proto {
		emsg |= protobufMessageFlag
	}

	binary.LittleEndian.PutUint32(encoded[0:4], emsg)
	//nolint:gosec // bounded above by maxUint32Int64
	binary.LittleEndian.PutUint32(encoded[4:8], uint32(len(frame.Header)))
	copy(encoded[8:], frame.Header)
	copy(encoded[8+len(frame.Header):], frame.Body)

	return encoded, nil
}

// DecodeFrame parses one complete CM envelope and copies its payloads.
func DecodeFrame(encoded []byte, limits FrameLimits) (Frame, error) {
	limits = limits.withDefaults()

	if len(encoded) < 8 {
		return Frame{}, errors.New("CM frame is truncated")
	}
	if len(encoded) > limits.MaxFrameSize {
		return Frame{}, fmt.Errorf("CM frame exceeds %d bytes", limits.MaxFrameSize)
	}

	rawEMsg := binary.LittleEndian.Uint32(encoded[0:4])
	headerSize := binary.LittleEndian.Uint32(encoded[4:8])
	if rawEMsg&protobufMessageFlag == 0 {
		return decodeLegacyFrame(encoded, rawEMsg)
	}

	if int64(limits.MaxHeaderSize) > maxUint32Int64 {
		return Frame{}, errors.New("CM maximum header size exceeds uint32")
	}
	if int64(headerSize) > int64(limits.MaxHeaderSize) {
		return Frame{}, fmt.Errorf("CM header exceeds %d bytes", limits.MaxHeaderSize)
	}
	if uint64(8)+uint64(headerSize) > uint64(len(encoded)) {
		return Frame{}, errors.New("CM frame header is truncated")
	}

	headerEnd := 8 + int(headerSize)

	return Frame{
		EMsg:   rawEMsg &^ protobufMessageFlag,
		Proto:  true,
		Header: append([]byte(nil), encoded[8:headerEnd]...),
		Body:   append([]byte(nil), encoded[headerEnd:]...),
	}, nil
}

// decodeLegacyFrame parses the fixed-size legacy header variants still used by CM.
func decodeLegacyFrame(encoded []byte, rawEMsg uint32) (Frame, error) {
	if len(encoded) >= 36 && encoded[4] == 36 && binary.LittleEndian.Uint16(encoded[5:7]) == 2 {
		// The extended header's marker guards the offsets used below.
		if encoded[23] != 0xef {
			return Frame{}, errors.New("CM extended legacy header has an invalid canary")
		}

		return Frame{
			EMsg:   rawEMsg,
			Header: append([]byte(nil), encoded[4:36]...),
			Body:   append([]byte(nil), encoded[36:]...),
		}, nil
	}

	if len(encoded) < 20 {
		return Frame{}, errors.New("CM legacy frame is truncated")
	}

	return Frame{
		EMsg:   rawEMsg,
		Header: append([]byte(nil), encoded[4:20]...),
		Body:   append([]byte(nil), encoded[20:]...),
	}, nil
}
