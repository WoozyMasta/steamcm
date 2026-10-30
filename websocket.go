// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// defaultWebSocketHandshakeTimeout bounds the HTTP upgrade handshake.
	defaultWebSocketHandshakeTimeout = 10 * time.Second
	// defaultWebSocketMessageSize caps a single WebSocket message.
	defaultWebSocketMessageSize = 16 << 20
	// defaultWebSocketIOTimeout bounds I/O without an earlier context deadline.
	defaultWebSocketIOTimeout = 15 * time.Second
)

// WebSocketConn carries framed CM messages over a WebSocket.
// One goroutine may read while one goroutine writes;
// writes are serialized internally.
type WebSocketConn struct {
	// conn is the Gorilla WebSocket transport.
	conn *websocket.Conn
	// messageSize is the configured maximum WebSocket message size.
	messageSize int64
	// ioTimeout applies when an operation context has no earlier deadline.
	ioTimeout time.Duration
	// writeMu enforces Gorilla's single-writer requirement.
	writeMu sync.Mutex
	// closeMu protects the idempotent close state.
	closeMu sync.Mutex
	// closed records that the underlying connection has been closed.
	closed bool
}

// webSocketConfig stores dialer settings before a connection is opened.
type webSocketConfig struct {
	header           map[string][]string // header contains additional HTTP upgrade headers.
	handshakeTimeout time.Duration       // handshakeTimeout limits the WebSocket HTTP upgrade.
	messageSize      int64               // messageSize caps each received or sent binary message.
	ioTimeout        time.Duration       // ioTimeout bounds an individual read or write.
}

// WebSocketOption configures a CM WebSocket connection.
type WebSocketOption func(*webSocketConfig) error

// WithHandshakeTimeout bounds the WebSocket HTTP handshake.
func WithHandshakeTimeout(timeout time.Duration) WebSocketOption {
	return func(config *webSocketConfig) error {
		if timeout <= 0 {
			return errors.New("WebSocket handshake timeout must be positive")
		}

		config.handshakeTimeout = timeout
		return nil
	}
}

// WithMessageSize limits one incoming WebSocket message.
func WithMessageSize(size int64) WebSocketOption {
	return func(config *webSocketConfig) error {
		if size <= 0 {
			return errors.New("WebSocket message size must be positive")
		}

		config.messageSize = size
		return nil
	}
}

// WithIOTimeout bounds one WebSocket read or write
// when the context has no earlier deadline.
func WithIOTimeout(timeout time.Duration) WebSocketOption {
	return func(config *webSocketConfig) error {
		if timeout <= 0 {
			return errors.New("WebSocket I/O timeout must be positive")
		}

		config.ioTimeout = timeout
		return nil
	}
}

// WithHeader adds a header to the WebSocket handshake.
func WithHeader(key, value string) WebSocketOption {
	return func(config *webSocketConfig) error {
		if key == "" {
			return errors.New("WebSocket header name is empty")
		}

		if config.header == nil {
			config.header = make(map[string][]string)
		}
		config.header[key] = append(config.header[key], value)

		return nil
	}
}

// Dial opens a CM WebSocket. address may be a directory endpoint
// such as "cmp-fra.steamserver.net:27019" or a complete ws(s) URL.
func Dial(ctx context.Context, address string, opts ...WebSocketOption) (*WebSocketConn, error) {
	if ctx == nil {
		return nil, errors.New("CM WebSocket context is nil")
	}

	config := webSocketConfig{
		handshakeTimeout: defaultWebSocketHandshakeTimeout,
		messageSize:      defaultWebSocketMessageSize,
		ioTimeout:        defaultWebSocketIOTimeout,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}

		if err := opt(&config); err != nil {
			return nil, fmt.Errorf("configure CM WebSocket: %w", err)
		}
	}

	target, err := websocketURL(address)
	if err != nil {
		return nil, err
	}

	dialer := websocket.Dialer{HandshakeTimeout: config.handshakeTimeout}
	conn, response, err := dialer.DialContext(ctx, target, cloneHeader(config.header))
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("dial CM WebSocket: %w", err)
	}

	conn.SetReadLimit(config.messageSize)
	return &WebSocketConn{
		conn:        conn,
		messageSize: config.messageSize,
		ioTimeout:   config.ioTimeout,
	}, nil
}

// ReadFrame reads and decodes one binary CM WebSocket message.
func (c *WebSocketConn) ReadFrame(ctx context.Context, limits FrameLimits) (Frame, error) {
	return c.readFrame(ctx, limits, true)
}

// readFrameBlocking reads without imposing ioTimeout; context cancellation still unblocks it.
func (c *WebSocketConn) readFrameBlocking(ctx context.Context, limits FrameLimits) (Frame, error) {
	return c.readFrame(ctx, limits, false)
}

// readFrame reads one binary message and optionally applies the configured I/O timeout.
func (c *WebSocketConn) readFrame(ctx context.Context, limits FrameLimits, applyTimeout bool) (Frame, error) {
	if ctx == nil {
		return Frame{}, errors.New("CM WebSocket read context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Frame{}, err
	}

	if applyTimeout {
		if err := c.setReadDeadline(ctx); err != nil {
			return Frame{}, err
		}
	} else if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		return Frame{}, err
	}

	readDone := make(chan struct{})
	watcherDone := make(chan struct{})

	// Gorilla reads block on socket deadlines, so cancellation advances the deadline.
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = c.conn.SetReadDeadline(time.Now())
		case <-readDone:
		}
	}()
	messageType, payload, err := c.conn.ReadMessage()
	close(readDone)
	<-watcherDone

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Frame{}, ctxErr
		}
		var networkErr net.Error
		if errors.As(err, &networkErr) && networkErr.Timeout() {
			// The socket deadline can fire before the context timer marks itself done.
			if deadline, hasDeadline := ctx.Deadline(); hasDeadline && !time.Now().Before(deadline) {
				return Frame{}, context.DeadlineExceeded
			}
		}

		return Frame{}, fmt.Errorf("read CM WebSocket message: %w", err)
	}

	if messageType != websocket.BinaryMessage {
		return Frame{}, fmt.Errorf("unexpected CM WebSocket message type %d", messageType)
	}
	if int64(len(payload)) > c.messageSize {
		return Frame{}, fmt.Errorf("CM WebSocket message exceeds %d bytes", c.messageSize)
	}

	frame, err := DecodeFrame(payload, limits)
	if err != nil {
		return Frame{}, fmt.Errorf("decode CM WebSocket frame: %w", err)
	}

	return frame, nil
}

// WriteFrame encodes and sends one binary CM WebSocket message.
func (c *WebSocketConn) WriteFrame(ctx context.Context, frame Frame, limits FrameLimits) error {
	if ctx == nil {
		return errors.New("CM WebSocket write context is nil")
	}

	encoded, err := EncodeFrame(frame, limits)
	if err != nil {
		return fmt.Errorf("encode CM WebSocket frame: %w", err)
	}
	if int64(len(encoded)) > c.messageSize {
		return fmt.Errorf("CM WebSocket frame exceeds %d bytes", c.messageSize)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}

	if err := c.conn.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return fmt.Errorf("write CM WebSocket message: %w", err)
	}

	return nil
}

// Close closes the WebSocket connection.
func (c *WebSocketConn) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close CM WebSocket: %w", err)
	}

	return nil
}

// websocketURL expands a directory host:port or validates a complete ws(s) URL.
func websocketURL(address string) (string, error) {
	if strings.HasPrefix(address, "ws://") || strings.HasPrefix(address, "wss://") {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("invalid CM WebSocket URL %q", address)
		}

		if parsed.Path == "" || parsed.Path == "/" {
			parsed.Path = "/cmsocket/"
		}

		return parsed.String(), nil
	}

	if address == "" || strings.ContainsAny(address, "/?#") {
		return "", fmt.Errorf("invalid CM endpoint %q", address)
	}

	return "wss://" + address + "/cmsocket/", nil
}

// cloneHeader prevents caller mutations from changing headers during a dial.
func cloneHeader(values map[string][]string) http.Header {
	if len(values) == 0 {
		return nil
	}

	result := make(http.Header, len(values))
	for key, entries := range values {
		result[key] = append([]string(nil), entries...)
	}

	return result
}

// setReadDeadline uses the earlier configured timeout or context deadline.
func (c *WebSocketConn) setReadDeadline(ctx context.Context) error {
	deadline := time.Now().Add(c.ioTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}

	return c.conn.SetReadDeadline(deadline)
}

// setWriteDeadline uses the earlier configured timeout or context deadline.
func (c *WebSocketConn) setWriteDeadline(ctx context.Context) error {
	deadline := time.Now().Add(c.ioTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}

	return c.conn.SetWriteDeadline(deadline)
}
