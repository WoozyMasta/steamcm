// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"testing"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func FuzzDecodeMulti(f *testing.F) {
	seed, err := proto.Marshal(&steamproto.CMsgMulti{})
	if err != nil {
		f.Fatal(err)
	}

	f.Add(seed)
	f.Add([]byte{0x12, 0x01, 0x00})

	child, err := EncodeFrame(Frame{
		EMsg:  EMsgClientHeartBeat,
		Proto: true,
		Body:  []byte{1},
	}, FrameLimits{})
	if err != nil {
		f.Fatal(err)
	}
	nestedBody, err := proto.Marshal(&steamproto.CMsgMulti{
		MessageBody: appendLengthPrefixed(child),
	})
	if err != nil {
		f.Fatal(err)
	}
	nested, err := EncodeFrame(Frame{
		EMsg:  EMsgMulti,
		Proto: true,
		Body:  nestedBody,
	}, FrameLimits{})
	if err != nil {
		f.Fatal(err)
	}
	nestedSeed, err := proto.Marshal(&steamproto.CMsgMulti{MessageBody: appendLengthPrefixed(nested)})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(nestedSeed)

	f.Fuzz(func(_ *testing.T, input []byte) {
		_, _ = DecodeMulti(Frame{EMsg: EMsgMulti, Proto: true, Body: input}, MultiLimits{
			MaxUncompressedBytes: 1 << 20,
			MaxMessages:          128,
			MaxDepth:             4,
			FrameLimits: FrameLimits{
				MaxFrameSize:  1 << 20,
				MaxHeaderSize: 1 << 16,
			},
		})
	})
}
