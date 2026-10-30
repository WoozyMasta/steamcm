// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import "testing"

func BenchmarkEncodeFrame64KiB(b *testing.B) {
	frame := Frame{
		EMsg:   EMsgClientHeartBeat,
		Proto:  true,
		Header: make([]byte, 64),
		Body:   make([]byte, 64<<10),
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(frame.Header) + len(frame.Body) + 8))
	b.ResetTimer()
	for range b.N {
		encoded, err := EncodeFrame(frame, FrameLimits{})
		if err != nil || len(encoded) == 0 {
			b.Fatalf("EncodeFrame() length=%d, error=%v", len(encoded), err)
		}
	}
}

func BenchmarkDecodeFrame64KiB(b *testing.B) {
	encoded := mustEncodedFrame(b, Frame{
		EMsg:   EMsgClientHeartBeat,
		Proto:  true,
		Header: make([]byte, 64),
		Body:   make([]byte, 64<<10),
	})

	b.ReportAllocs()
	b.SetBytes(int64(len(encoded)))
	b.ResetTimer()
	for range b.N {
		frame, err := DecodeFrame(encoded, FrameLimits{})
		if err != nil || len(frame.Body) != 64<<10 {
			b.Fatalf("DecodeFrame() body=%d, error=%v", len(frame.Body), err)
		}
	}
}

func BenchmarkDecodeMulti1000Children(b *testing.B) {
	child := mustEncodedFrame(b, Frame{
		EMsg:  EMsgClientHeartBeat,
		Proto: true,
		Body:  []byte{1, 2, 3},
	})
	children := make([][]byte, 1000)
	for index := range children {
		children[index] = child
	}
	payload := appendLengthPrefixed(children...)

	for _, compressed := range []bool{false, true} {
		name := "plain"
		if compressed {
			name = "gzip"
		}
		frame := mustMultiFrame(b, payload, compressed)

		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for range b.N {
				frames, err := DecodeMulti(frame, MultiLimits{})
				if err != nil || len(frames) != len(children) {
					b.Fatalf("DecodeMulti() frames=%d, error=%v", len(frames), err)
				}
			}
		})
	}
}
