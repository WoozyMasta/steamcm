// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

const (
	// EMsgClientHello identifies the initial CM client hello message.
	EMsgClientHello uint32 = 9805

	// EMsgServiceMethodCallFromClientNonAuthed identifies a pre-login UM call.
	EMsgServiceMethodCallFromClientNonAuthed uint32 = 9804

	// maxUnauthenticatedNotifications bounds unrelated messages while waiting for a reply.
	maxUnauthenticatedNotifications int = 64
)

// CallUnauthenticated calls a Steam Unified Messaging method before CM
// logon. Calls are serialized because this operation owns socket reads.
func (c *Client) CallUnauthenticated(ctx context.Context, method string, body []byte) (Message, error) {
	if ctx == nil {
		return Message{}, errors.New("CM unauthenticated call context is nil")
	}
	if method == "" {
		return Message{}, errors.New("CM unauthenticated method is empty")
	}
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	if c == nil {
		return Message{}, errors.New("CM client is nil")
	}

	c.preAuthMu.Lock()
	defer c.preAuthMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}

	c.mu.Lock()
	conn := c.conn
	closed := c.closed
	logonStarted := c.logonStarted || c.session != nil
	c.mu.Unlock()

	if closed {
		return Message{}, errors.New("CM client is closed")
	}
	if conn == nil {
		return Message{}, errors.New("CM client is not connected")
	}
	if logonStarted {
		return Message{}, errors.New("CM unauthenticated call is unavailable after logon starts")
	}

	if !c.preAuthHello {
		// Send hello once per connection before any unauthenticated service calls.
		hello, err := encodeProtoMessage(EMsgClientHello, &steamHeader{
			JobidSource: uint64Pointer(NoJobID),
			JobidTarget: uint64Pointer(NoJobID),
		}, &steamproto.CMsgClientHello{ProtocolVersion: uint32Value(anonymousLogonProtocolVersion)})
		if err != nil {
			return Message{}, fmt.Errorf("encode CM hello: %w", err)
		}

		if err := conn.WriteFrame(ctx, hello, c.frameLimits); err != nil {
			_ = c.Close()
			return Message{}, fmt.Errorf("send CM hello: %w", err)
		}

		c.preAuthHello = true
	}

	jobID := c.nextJobID
	c.nextJobID++
	headerBytes, err := proto.Marshal(&steamHeader{
		JobidSource:   uint64Pointer(jobID),
		JobidTarget:   uint64Pointer(NoJobID),
		TargetJobName: stringValue(method),
	})
	if err != nil {
		return Message{}, fmt.Errorf("encode unauthenticated CM header: %w", err)
	}

	request := Frame{
		EMsg:   EMsgServiceMethodCallFromClientNonAuthed,
		Proto:  true,
		Header: headerBytes,
		Body:   append([]byte(nil), body...),
	}
	if err := conn.WriteFrame(ctx, request, c.frameLimits); err != nil {
		_ = c.Close()
		return Message{}, fmt.Errorf("send unauthenticated CM call: %w", err)
	}

	for notifications := 0; notifications <= maxUnauthenticatedNotifications; {
		frame, err := conn.ReadFrame(ctx, c.frameLimits)
		if err != nil {
			_ = c.Close()
			return Message{}, fmt.Errorf("read unauthenticated CM response: %w", err)
		}

		frames := []Frame{frame}
		if frame.EMsg == EMsgMulti {
			frames, err = DecodeMulti(frame, MultiLimits{FrameLimits: c.frameLimits})
			if err != nil {
				_ = c.Close()
				return Message{}, fmt.Errorf("decode unauthenticated CM Multi: %w", err)
			}
		}

		for _, candidate := range frames {
			if candidate.EMsg != EMsgServiceMethodResponse {
				// Ignore bounded notifications, but keep reading for this call's reply.
				notifications++
				if notifications > maxUnauthenticatedNotifications {
					_ = c.Close()
					return Message{}, errors.New("too many unrelated messages during unauthenticated CM call")
				}

				continue
			}

			message, err := decodeMessage(candidate)
			if err != nil {
				_ = c.Close()
				return Message{}, fmt.Errorf("decode unauthenticated CM message: %w", err)
			}

			if message.Header.JobIDTarget != jobID {
				notifications++
				if notifications > maxUnauthenticatedNotifications {
					_ = c.Close()
					return Message{}, errors.New("too many unrelated messages during unauthenticated CM call")
				}

				continue
			}

			if !message.Header.HasEResult {
				return message, MissingEResultError{}
			}
			if message.Header.EResult != EResultOK {
				return message, &JobError{EResult: message.Header.EResult, Message: message.Header.ErrorMessage}
			}

			return message, nil
		}
	}

	return Message{}, errors.New("unreachable unauthenticated CM response state")
}
