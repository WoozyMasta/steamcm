// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
)

// NoJobID is the reserved Steam value used when a message has no job ID.
const NoJobID uint64 = ^uint64(0)

// defaultMaxPendingJobs bounds concurrent correlated requests per session.
const defaultMaxPendingJobs = 1024

// Header is the public subset of CMsgProtoBufHeader used by CM jobs.
type Header struct {
	TargetJobName   string
	ErrorMessage    string
	SteamID         uint64
	JobIDSource     uint64
	JobIDTarget     uint64
	ClientSessionID int32
	EResult         int32
	HasEResult      bool
}

// Message is a decoded inbound CM message with an opaque protobuf body.
type Message struct {
	Body   []byte
	Header Header
	EMsg   uint32
}

// JobRequest describes one protobuf CM job.
// Body must contain the request protobuf payload;
// response decoding belongs to the transport-specific package.
type JobRequest struct {
	TargetJobName  string
	Body           []byte
	EMsg           uint32
	ExpectedEMsg   uint32
	RequireEResult bool
}

// jobResult carries either a correlated response or a session failure.
type jobResult struct {
	err     error   // err reports transport or session failure for this job.
	message Message // message is the response matched to the job ID.
}

// pendingJobs maps outbound job IDs to their waiting response channels.
type pendingJobs struct {
	entries map[uint64]chan<- jobResult // entries contains active jobs; mu protects every map access.
	nextID  atomic.Uint64               // nextID allocates monotonically increasing job IDs.
	max     int                         // max caps the number of simultaneously registered jobs.
	mu      sync.Mutex                  // mu protects entries while response delivery races with cancellation.
}

// newPendingJobs initializes an empty registry with the package limit.
func newPendingJobs() *pendingJobs {
	jobs := &pendingJobs{
		max:     defaultMaxPendingJobs,
		entries: make(map[uint64]chan<- jobResult),
	}
	jobs.nextID.Store(1)

	return jobs
}

// add registers a response channel and returns the job ID used for correlation.
func (p *pendingJobs) add(ch chan<- jobResult) (uint64, error) {
	id := p.nextID.Add(1) - 1
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.entries) >= p.max {
		return 0, fmt.Errorf("CM pending job limit %d exceeded", p.max)
	}

	p.entries[id] = ch
	return id, nil
}

// remove unregisters a completed or canceled job.
func (p *pendingJobs) remove(id uint64) {
	p.mu.Lock()
	delete(p.entries, id)
	p.mu.Unlock()
}

// deliver routes a response without blocking the session reader.
func (p *pendingJobs) deliver(message Message) bool {
	p.mu.Lock()
	ch := p.entries[message.Header.JobIDTarget]
	p.mu.Unlock()

	if ch == nil {
		return false
	}

	select {
	case ch <- jobResult{message: message}:
		return true
	default:
		return false
	}
}

// fail removes all pending jobs and notifies their waiters of session failure.
func (p *pendingJobs) fail(err error) {
	p.mu.Lock()

	entries := make([]chan<- jobResult, 0, len(p.entries))
	for id, ch := range p.entries {
		delete(p.entries, id)
		entries = append(entries, ch)
	}

	p.mu.Unlock()

	// Notify after unlocking so waiting calls cannot hold up new registrations.
	for _, ch := range entries {
		select {
		case ch <- jobResult{err: err}:
		default:
		}
	}
}

// call sends a job and waits for its response, cancellation, or session shutdown.
func (s *Session) call(ctx context.Context, request JobRequest) (Message, error) {
	if ctx == nil {
		return Message{}, errors.New("CM job context is nil")
	}
	if request.EMsg == 0 {
		return Message{}, errors.New("CM job EMsg is zero")
	}
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}

	resultCh := make(chan jobResult, 1)
	jobID, err := s.jobs.add(resultCh)
	if err != nil {
		return Message{}, err
	}
	defer s.jobs.remove(jobID)

	header := &steamHeader{
		Steamid:         uint64Pointer(s.SteamID),
		ClientSessionid: int32Pointer(s.SessionID),
		JobidSource:     uint64Pointer(jobID),
		JobidTarget:     uint64Pointer(NoJobID),
	}

	if request.TargetJobName != "" {
		header.TargetJobName = stringValue(request.TargetJobName)
	}

	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return Message{}, fmt.Errorf("marshal CM job header: %w", err)
	}

	frame := Frame{EMsg: request.EMsg, Proto: true, Header: headerBytes, Body: append([]byte(nil), request.Body...)}
	if err := s.WebSocketConn.WriteFrame(ctx, frame, s.frameLimits); err != nil {
		s.jobs.remove(jobID)
		return Message{}, err
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			return Message{}, result.err
		}
		if request.ExpectedEMsg != 0 && result.message.EMsg != request.ExpectedEMsg {
			return Message{}, fmt.Errorf("CM job response EMsg = %d, want %d", result.message.EMsg, request.ExpectedEMsg)
		}

		if err := validateJobResult(request, result.message.Header); err != nil {
			return result.message, err
		}

		return result.message, nil

	case <-ctx.Done():
		s.jobs.remove(jobID)
		return Message{}, ctx.Err()

	case <-s.done():
		s.jobs.remove(jobID)
		return Message{}, s.readerError()
	}
}

// validateJobResult enforces required EResult presence and success status.
func validateJobResult(request JobRequest, header Header) error {
	if request.RequireEResult && !header.HasEResult {
		return MissingEResultError{}
	}
	if header.HasEResult && header.EResult != EResultOK {
		return &JobError{EResult: header.EResult, Message: header.ErrorMessage}
	}

	return nil
}

// Call submits a correlated CM job and returns its opaque protobuf response.
func (s *Session) Call(ctx context.Context, request JobRequest) (Message, error) {
	return s.call(ctx, request)
}
