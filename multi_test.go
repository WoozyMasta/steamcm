// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"testing"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func TestDecodeMulti(t *testing.T) {
	child1 := mustEncodedFrame(t, Frame{EMsg: EMsgClientHeartBeat, Proto: true, Body: []byte{1}})
	child2 := mustEncodedFrame(t, Frame{EMsg: EMsgClientLogon, Proto: true, Body: []byte{2}})
	payload := appendLengthPrefixed(child1, child2)
	frame := mustMultiFrame(t, payload, false)

	got, err := DecodeMulti(frame, MultiLimits{})
	if err != nil {
		t.Fatalf("DecodeMulti() error = %v", err)
	}
	if len(got) != 2 || got[0].EMsg != EMsgClientHeartBeat || got[1].EMsg != EMsgClientLogon {
		t.Fatalf("DecodeMulti() = %#v, want two child frames", got)
	}
}

func TestDecodeCompressedMulti(t *testing.T) {
	child := mustEncodedFrame(t, Frame{EMsg: EMsgClientHeartBeat, Proto: true, Body: []byte{1, 2, 3}})
	payload := appendLengthPrefixed(child)
	frame := mustMultiFrame(t, payload, true)
	got, err := DecodeMulti(frame, MultiLimits{})
	if err != nil {
		t.Fatalf("DecodeMulti() error = %v", err)
	}
	if len(got) != 1 || !bytes.Equal(got[0].Body, []byte{1, 2, 3}) {
		t.Fatalf("DecodeMulti() = %#v, want one child", got)
	}
}

func TestDecodeNestedMulti(t *testing.T) {
	leaf := mustEncodedFrame(t, Frame{EMsg: EMsgClientHeartBeat, Proto: true, Body: []byte{1}})
	nested := mustEncodedFrame(t, mustMultiFrame(t, appendLengthPrefixed(leaf), false))
	outer := mustMultiFrame(t, appendLengthPrefixed(nested), false)

	got, err := DecodeMulti(outer, MultiLimits{MaxDepth: 2})
	if err != nil {
		t.Fatalf("DecodeMulti() error = %v", err)
	}
	if len(got) != 1 || got[0].EMsg != EMsgClientHeartBeat || !bytes.Equal(got[0].Body, []byte{1}) {
		t.Fatalf("DecodeMulti() = %#v, want one expanded heartbeat", got)
	}
}

func TestDecodeMultiRejectsNestingBeyondLimit(t *testing.T) {
	leaf := mustEncodedFrame(t, Frame{EMsg: EMsgClientHeartBeat, Proto: true, Body: []byte{1}})
	nested := mustEncodedFrame(t, mustMultiFrame(t, appendLengthPrefixed(leaf), false))
	outer := mustMultiFrame(t, appendLengthPrefixed(nested), false)

	if _, err := DecodeMulti(outer, MultiLimits{MaxDepth: 1}); err == nil {
		t.Fatal("DecodeMulti() error = nil, want nesting depth error")
	}
}

func TestDecodeMultiPreservesLegacyChildren(t *testing.T) {
	child := make([]byte, 22)
	binary.LittleEndian.PutUint32(child[0:4], EMsgClientHeartBeat)
	binary.LittleEndian.PutUint64(child[4:12], NoJobID)
	binary.LittleEndian.PutUint64(child[12:20], NoJobID)
	child[20], child[21] = 4, 5

	got, err := DecodeMulti(mustMultiFrame(t, appendLengthPrefixed(child), false), MultiLimits{})
	if err != nil {
		t.Fatalf("DecodeMulti() error = %v", err)
	}
	if len(got) != 1 || got[0].Proto || got[0].EMsg != EMsgClientHeartBeat ||
		!bytes.Equal(got[0].Body, []byte{4, 5}) {
		t.Fatalf("DecodeMulti() = %#v, want one legacy heartbeat child", got)
	}
}

func TestDecodeMultiRejectsBounds(t *testing.T) {
	child := mustEncodedFrame(t, Frame{EMsg: EMsgClientHeartBeat, Proto: true, Body: []byte{1}})
	frame := mustMultiFrame(t, appendLengthPrefixed(child), false)

	if _, err := DecodeMulti(frame, MultiLimits{
		MaxMessages: 0,
		FrameLimits: FrameLimits{MaxFrameSize: len(child) - 1},
	}); err == nil {
		t.Fatal("DecodeMulti() error = nil, want child size error")
	}
	if _, err := DecodeMulti(frame, MultiLimits{MaxMessages: 1}); err != nil {
		t.Fatalf("DecodeMulti() with one child limit error = %v", err)
	}

	tooMany := appendLengthPrefixed(child, child)
	if _, err := DecodeMulti(mustMultiFrame(t, tooMany, false), MultiLimits{MaxMessages: 1}); err == nil {
		t.Fatal("DecodeMulti() error = nil, want child count error")
	}
}

func mustEncodedFrame(t testing.TB, frame Frame) []byte {
	t.Helper()
	encoded, err := EncodeFrame(frame, FrameLimits{})
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}

	return encoded
}

func appendLengthPrefixed(children ...[]byte) []byte {
	var dst []byte
	for _, child := range children {
		var size [4]byte
		//nolint:gosec // Test fixtures are small in-memory CM frames.
		binary.LittleEndian.PutUint32(size[:], uint32(len(child)))
		dst = append(dst, size[:]...)
		dst = append(dst, child...)
	}

	return dst
}

func mustMultiFrame(t testing.TB, payload []byte, compressed bool) Frame {
	t.Helper()
	body := payload
	var size uint32

	if compressed {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		if _, err := writer.Write(payload); err != nil {
			t.Fatalf("gzip.Write() error = %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("gzip.Close() error = %v", err)
		}

		body = buffer.Bytes()
		//nolint:gosec // Test fixtures are small in-memory CM payloads.
		size = uint32(len(payload))
	}

	multi, err := proto.Marshal(&steamproto.CMsgMulti{
		SizeUnzipped: uint32Value(size),
		MessageBody:  body,
	})
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}

	return Frame{EMsg: EMsgMulti, Proto: true, Body: multi}
}
