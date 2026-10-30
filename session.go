// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

// steamHeader aliases the generated CM protobuf header for internal use.
type steamHeader = steamproto.CMsgProtoBufHeader

// steamLogon aliases the generated CM logon request.
type steamLogon = steamproto.CMsgClientLogon

// steamLogonResponse aliases the generated CM logon response.
type steamLogonResponse = steamproto.CMsgClientLogonResponse

// SessionState contains the identity and timing values assigned by CM.
type SessionState struct {
	SteamID           uint64
	HeartbeatInterval time.Duration
	SessionID         int32
	CellID            uint32
}

// Session is an authenticated CM session.
type Session struct {
	// readerErrValue stores the first terminal reader or heartbeat failure.
	readerErrValue error
	// closeErr preserves the result of closing the transport.
	closeErr error
	// ctx is canceled when the session shuts down.
	ctx context.Context
	// WebSocketConn is the transport used by this authenticated session.
	WebSocketConn *WebSocketConn
	// cancel stops session workers and unblocks their I/O.
	cancel context.CancelFunc
	// events carries unmatched inbound messages to ReadMessage.
	events chan Message
	// jobs routes correlated replies to their waiting Call invocations.
	jobs *pendingJobs
	// readerDone closes when the frame dispatcher exits.
	readerDone chan struct{}
	// SessionState exposes the Steam identity and heartbeat settings negotiated at logon.
	SessionState
	// frameLimits apply to all session frames.
	frameLimits FrameLimits
	// heartbeat tracks the optional periodic heartbeat worker.
	heartbeat sync.WaitGroup
	// reader tracks the single inbound frame dispatcher.
	reader sync.WaitGroup
	// closeOnce makes transport and worker shutdown idempotent.
	closeOnce sync.Once
	// readerErrMu protects readerErrValue across workers and callers.
	readerErrMu sync.Mutex
}

// newSession initializes queues and cancellation state for a logged-on client.
func newSession(conn *WebSocketConn, state SessionState, limits FrameLimits) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		WebSocketConn: conn,
		SessionState:  state,
		frameLimits:   limits,
		ctx:           ctx,
		cancel:        cancel,
		readerDone:    make(chan struct{}),
		events:        make(chan Message, 16),
		jobs:          newPendingJobs(),
	}
}

// ReadMessage reads one unmatched inbound CM message from the session event
// queue. Correlated job responses are consumed by Call.
func (s *Session) ReadMessage(ctx context.Context) (Message, error) {
	if s == nil || s.WebSocketConn == nil {
		return Message{}, errors.New("CM session is nil")
	}

	if ctx == nil {
		return Message{}, errors.New("CM message context is nil")
	}

	select {
	case message := <-s.events:
		return message, nil

	case <-s.readerDone:
		return Message{}, s.readerError()

	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

// Close stops heartbeat and closes the underlying CM connection.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}

	s.closeOnce.Do(func() {
		s.cancel()
		s.closeErr = s.WebSocketConn.Close()
		s.reader.Wait()
		s.heartbeat.Wait()
	})

	return s.closeErr
}

// startReader dispatches frames received during logon, then reads until shutdown.
func (s *Session) startReader(initial []Frame) {
	s.reader.Add(1)
	go func() {
		defer s.reader.Done()
		defer close(s.readerDone)
		if err := s.dispatchFrames(initial); err != nil {
			s.setReaderError(err)
			s.jobs.fail(err)
			return
		}

		for {
			frame, err := s.WebSocketConn.readFrameBlocking(s.ctx, s.frameLimits)
			if err != nil {
				s.setReaderError(err)
				s.jobs.fail(err)
				return
			}

			frames := []Frame{frame}
			if frame.EMsg == EMsgMulti {
				frames, err = DecodeMulti(frame, MultiLimits{FrameLimits: s.frameLimits})
				if err != nil {
					s.setReaderError(err)
					s.jobs.fail(err)
					return
				}
			}

			if err := s.dispatchFrames(frames); err != nil {
				s.setReaderError(err)
				s.jobs.fail(err)
				return
			}
		}
	}()
}

// dispatchFrames routes job replies and queues unmatched messages for callers.
func (s *Session) dispatchFrames(frames []Frame) error {
	for _, frame := range frames {
		if frame.EMsg == EMsgClientLoggedOff {
			if !frame.Proto {
				err := &LogoffError{}
				s.jobs.fail(err)
				return err
			}

			loggedOff := &steamproto.CMsgClientLoggedOff{}
			if err := decodeProtoMessage(frame, &steamHeader{}, loggedOff); err != nil {
				return fmt.Errorf("decode CM logoff: %w", err)
			}

			err := &LogoffError{EResult: loggedOff.GetEresult(), HasEResult: true}
			s.jobs.fail(err)
			return err
		}

		message, err := decodeMessage(frame)
		if err != nil {
			return err
		}
		if s.jobs.deliver(message) {
			continue
		}

		select {
		case s.events <- message:
		default:
			// Unmatched events are best-effort; never stall response dispatch.
		}
	}

	return nil
}

// decodeMessage extracts the common header and copies an inbound frame body.
func decodeMessage(frame Frame) (Message, error) {
	if !frame.Proto {
		return decodeLegacyMessage(frame)
	}

	header := &steamHeader{}
	if err := proto.Unmarshal(frame.Header, header); err != nil {
		return Message{}, fmt.Errorf("decode CM message header: %w", err)
	}

	return Message{
		EMsg: frame.EMsg,
		Header: Header{
			SteamID:         header.GetSteamid(),
			ClientSessionID: header.GetClientSessionid(),
			JobIDSource:     header.GetJobidSource(),
			JobIDTarget:     header.GetJobidTarget(),
			TargetJobName:   header.GetTargetJobName(),
			EResult:         header.GetEresult(),
			ErrorMessage:    header.GetErrorMessage(),
			HasEResult:      header.Eresult != nil,
		},
		Body: append([]byte(nil), frame.Body...),
	}, nil
}

// decodeLegacyMessage decodes the two supported fixed-size legacy headers.
func decodeLegacyMessage(frame Frame) (Message, error) {
	var header Header
	switch len(frame.Header) {
	case 16:
		header.JobIDTarget = binary.LittleEndian.Uint64(frame.Header[0:8])
		header.JobIDSource = binary.LittleEndian.Uint64(frame.Header[8:16])

	case 32:
		if frame.Header[0] != 36 ||
			binary.LittleEndian.Uint16(frame.Header[1:3]) != 2 ||
			frame.Header[19] != 0xef {
			return Message{}, errors.New("CM extended legacy message header is invalid")
		}

		header.JobIDTarget = binary.LittleEndian.Uint64(frame.Header[3:11])
		header.JobIDSource = binary.LittleEndian.Uint64(frame.Header[11:19])
		header.SteamID = binary.LittleEndian.Uint64(frame.Header[20:28])
		header.ClientSessionID = int32FromBits(binary.LittleEndian.Uint32(frame.Header[28:32]))

	default:
		return Message{}, fmt.Errorf("CM legacy message header has invalid size %d", len(frame.Header))
	}

	return Message{
		EMsg:   frame.EMsg,
		Header: header,
		Body:   append([]byte(nil), frame.Body...),
	}, nil
}

// int32FromBits preserves the two's-complement bit pattern without overflow.
func int32FromBits(value uint32) int32 {
	if value&0x80000000 == 0 {
		// #nosec G115 -- The sign bit is clear, so this value fits in int32.
		return int32(value)
	}

	// #nosec G115 -- Complementing first keeps the conversion within int32 range.
	return -int32(^value) - 1
}

// setReaderError records only the first asynchronous session failure.
func (s *Session) setReaderError(err error) {
	s.readerErrMu.Lock()
	if s.readerErrValue == nil {
		s.readerErrValue = err
	}
	s.readerErrMu.Unlock()
}

// readerError returns the recorded failure or context cancellation on clean stop.
func (s *Session) readerError() error {
	s.readerErrMu.Lock()
	defer s.readerErrMu.Unlock()
	if s.readerErrValue != nil {
		return s.readerErrValue
	}

	return context.Canceled
}

// startHeartbeat sends periodic CM heartbeats when the server requests them.
func (s *Session) startHeartbeat() {
	if s.HeartbeatInterval <= 0 {
		return
	}

	s.heartbeat.Add(1)
	go func() {
		defer s.heartbeat.Done()
		ticker := time.NewTicker(s.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				header := &steamHeader{
					Steamid:         uint64Pointer(s.SteamID),
					ClientSessionid: int32Pointer(s.SessionID),
					JobidSource:     uint64Pointer(NoJobID),
					JobidTarget:     uint64Pointer(NoJobID),
				}

				frame, err := encodeProtoMessage(EMsgClientHeartBeat, header, newHeartbeat())
				if err != nil {
					s.fail(fmt.Errorf("encode CM heartbeat: %w", err))
					return
				}

				if err := s.WebSocketConn.WriteFrame(s.ctx, frame, s.frameLimits); err != nil {
					s.fail(fmt.Errorf("write CM heartbeat: %w", err))
					return
				}

			case <-s.done():
				return
			}
		}
	}()
}

// fail records a worker error and closes the transport to unblock its peer.
func (s *Session) fail(err error) {
	s.setReaderError(err)
	s.jobs.fail(err)
	s.cancel()
	_ = s.WebSocketConn.Close()
}

// done returns the signal closed when the reader exits.
func (s *Session) done() <-chan struct{} {
	return s.readerDone
}
