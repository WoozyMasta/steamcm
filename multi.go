// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

const (
	// defaultMaxMultiBytes caps decompressed bytes from nested Multi messages.
	defaultMaxMultiBytes = 64 << 20

	// defaultMaxMultiMessages caps the total expanded child frame count.
	defaultMaxMultiMessages = 4096

	// defaultMaxMultiDepth prevents recursive Multi nesting from exhausting stack.
	defaultMaxMultiDepth = 8
)

// MultiLimits bounds decompression and recursive multipart expansion.
type MultiLimits struct {
	MaxUncompressedBytes int
	MaxMessages          int
	MaxDepth             int
	FrameLimits          FrameLimits
}

// withDefaults applies safe expansion limits to unset fields.
func (l MultiLimits) withDefaults() MultiLimits {
	if l.MaxUncompressedBytes <= 0 {
		l.MaxUncompressedBytes = defaultMaxMultiBytes
	}
	if l.MaxMessages <= 0 {
		l.MaxMessages = defaultMaxMultiMessages
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = defaultMaxMultiDepth
	}

	return l
}

// DecodeMulti expands one EMsg.Multi frame into its child frames.
func DecodeMulti(frame Frame, limits MultiLimits) ([]Frame, error) {
	return decodeMulti(frame, limits.withDefaults(), 1)
}

// decodeMulti expands a Multi frame without reapplying defaults to remaining limits.
func decodeMulti(frame Frame, limits MultiLimits, depth int) ([]Frame, error) {
	if depth > limits.MaxDepth {
		return nil, fmt.Errorf("CM.Multi nesting exceeds depth %d", limits.MaxDepth)
	}
	if frame.EMsg != EMsgMulti {
		return nil, fmt.Errorf("CM frame is not EMsg.Multi: %d", frame.EMsg)
	}
	if !frame.Proto {
		return nil, errors.New("CM EMsg.Multi is not protobuf encoded")
	}

	var multi steamproto.CMsgMulti
	if err := proto.Unmarshal(frame.Body, &multi); err != nil {
		return nil, fmt.Errorf("unmarshal CM.Multi: %w", err)
	}

	payload, err := expandMultiPayload(
		multi.GetMessageBody(),
		int(multi.GetSizeUnzipped()),
		limits.MaxUncompressedBytes,
	)
	if err != nil {
		return nil, err
	}

	return decodeMultiChildren(payload, limits, depth)
}

// expandMultiPayload copies plain payloads or safely decompresses gzip payloads.
func expandMultiPayload(payload []byte, expectedSize, maxSize int) ([]byte, error) {
	if expectedSize == 0 {
		// A zero uncompressed-size field denotes an uncompressed Multi body.
		if len(payload) > maxSize {
			return nil, fmt.Errorf("CM.Multi payload exceeds %d bytes", maxSize)
		}

		return append([]byte(nil), payload...), nil
	}

	if expectedSize > maxSize {
		return nil, fmt.Errorf("CM.Multi uncompressed size exceeds %d bytes", maxSize)
	}

	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("open CM.Multi gzip payload: %w", err)
	}

	decompressed, readErr := io.ReadAll(io.LimitReader(reader, int64(maxSize)+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read CM.Multi gzip payload: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close CM.Multi gzip payload: %w", closeErr)
	}

	if len(decompressed) != expectedSize {
		return nil, fmt.Errorf("CM.Multi uncompressed size = %d, want %d", len(decompressed), expectedSize)
	}

	return decompressed, nil
}

// decodeMultiChildren parses length-prefixed frames and recursively expands Multi.
func decodeMultiChildren(payload []byte, limits MultiLimits, depth int) ([]Frame, error) {
	children := make([]Frame, 0)
	for offset := 0; offset < len(payload); {
		if len(payload)-offset < 4 {
			return nil, errors.New("CM.Multi child length is truncated")
		}

		childSize := int(binary.LittleEndian.Uint32(payload[offset : offset+4]))
		offset += 4
		if childSize <= 0 || childSize > limits.FrameLimits.withDefaults().MaxFrameSize {
			return nil, fmt.Errorf("CM.Multi child size %d is invalid", childSize)
		}
		if childSize > len(payload)-offset {
			return nil, errors.New("CM.Multi child frame is truncated")
		}

		child, err := DecodeFrame(payload[offset:offset+childSize], limits.FrameLimits)
		if err != nil {
			return nil, fmt.Errorf("decode CM.Multi child: %w", err)
		}

		offset += childSize
		if len(children) >= limits.MaxMessages {
			return nil, fmt.Errorf("CM.Multi child count exceeds %d", limits.MaxMessages)
		}

		if child.EMsg == EMsgMulti {
			// Nested calls share the remaining message and depth budgets.
			nestedLimits := limits
			nestedLimits.MaxMessages -= len(children)
			nested, err := decodeMulti(child, nestedLimits, depth+1)
			if err != nil {
				return nil, err
			}

			children = append(children, nested...)
			continue
		}

		children = append(children, child)
	}

	return children, nil
}
