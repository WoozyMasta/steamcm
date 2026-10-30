// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import "testing"

func FuzzDecodeFrame(f *testing.F) {
	seed, err := EncodeFrame(Frame{
		EMsg:   EMsgClientHeartBeat,
		Proto:  true,
		Header: []byte{0x08, 0x01},
		Body:   []byte{0x10, 0x02},
	}, FrameLimits{})
	if err != nil {
		f.Fatal(err)
	}

	f.Add(seed)
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})

	f.Fuzz(func(_ *testing.T, input []byte) {
		_, _ = DecodeFrame(input, FrameLimits{MaxFrameSize: 1 << 20, MaxHeaderSize: 1 << 16})
	})
}
