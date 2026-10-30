// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

// Package cm implements Steam Connection Manager transport and sessions.
package cm

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

// maxRefreshTokenBytes bounds the refresh token accepted by LogonToken.
const maxRefreshTokenBytes = 64 << 10

// clientConfig collects validated options before a Client is allocated.
type clientConfig struct {
	// webSocketOptions are applied to the underlying WebSocket dialer.
	webSocketOptions []WebSocketOption
	// frameLimits bound CM frame encoding and decoding.
	frameLimits FrameLimits
}

// Client manages one CM WebSocket connection.
type Client struct {
	// conn is the single active CM transport.
	conn *WebSocketConn
	// session is set after a successful logon.
	session *Session
	// webSocketOptions configure the transport created by Connect.
	webSocketOptions []WebSocketOption
	// frameLimits apply to all messages sent or received by this client.
	frameLimits FrameLimits
	// mu protects connection, session, and lifecycle state.
	mu sync.Mutex
	// preAuthMu serializes pre-logon calls with the logon handshake.
	preAuthMu sync.Mutex
	// preAuthHello records whether the initial unauthenticated hello was sent.
	preAuthHello bool
	// logonStarted prevents concurrent or repeated logon attempts.
	logonStarted bool
	// closed prevents new operations after Close.
	closed bool
	// nextJobID assigns correlation IDs to unauthenticated requests.
	nextJobID uint64
}

// ClientOption configures a CM client.
type ClientOption func(*clientConfig) error

// WithWebSocketOption passes an option to the CM WebSocket dialer.
func WithWebSocketOption(option WebSocketOption) ClientOption {
	return func(config *clientConfig) error {
		if option == nil {
			return errors.New("CM WebSocket option is nil")
		}

		config.webSocketOptions = append(config.webSocketOptions, option)
		return nil
	}
}

// WithFrameLimits configures CM frame decoding and encoding limits.
func WithFrameLimits(limits FrameLimits) ClientOption {
	return func(config *clientConfig) error {
		config.frameLimits = limits
		return nil
	}
}

// New creates a disconnected CM client.
func New(opts ...ClientOption) (*Client, error) {
	config := clientConfig{}
	for _, opt := range opts {
		if opt == nil {
			continue
		}

		if err := opt(&config); err != nil {
			return nil, fmt.Errorf("configure CM client: %w", err)
		}
	}

	return &Client{
		webSocketOptions: config.webSocketOptions,
		frameLimits:      config.frameLimits,
		nextJobID:        1,
	}, nil
}

// Connect opens a WebSocket to endpoint.
// The client supports one active connection at a time.
func (c *Client) Connect(ctx context.Context, endpoint Endpoint) error {
	if ctx == nil {
		return errors.New("CM connect context is nil")
	}

	if endpoint.Address == "" {
		return errors.New("CM endpoint address is empty")
	}

	conn, err := Dial(ctx, endpoint.Address, c.webSocketOptions...)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return errors.New("CM client is closed")
	}

	if c.conn != nil {
		c.mu.Unlock()
		_ = conn.Close()
		return errors.New("CM client is already connected")
	}

	c.conn = conn
	c.mu.Unlock()

	return nil
}

// LogonAnonymous performs the anonymous ClientLogon exchange
// and starts the session heartbeat when the server supplies a positive interval.
func (c *Client) LogonAnonymous(ctx context.Context) (*Session, error) {
	if ctx == nil {
		return nil, errors.New("CM logon context is nil")
	}

	c.preAuthMu.Lock()
	defer c.preAuthMu.Unlock()

	conn, err := c.beginLogon()
	if err != nil {
		return nil, err
	}

	request, err := encodeProtoMessage(EMsgClientLogon, &steamHeader{
		Steamid:         uint64Pointer(anonymousSteamID),
		ClientSessionid: int32Pointer(0),
		JobidSource:     uint64Pointer(NoJobID),
		JobidTarget:     uint64Pointer(NoJobID),
	}, newAnonymousLogon())
	if err != nil {
		return nil, fmt.Errorf("encode anonymous CM logon: %w", err)
	}

	return c.exchangeLogon(ctx, conn, request, 0)
}

// TokenLogonDetails contains the identity
// and SteamClient refresh token for an individual CM logon.
// LoginID must be unique for concurrent sessions from the same public IP;
// zero asks the client to generate one.
type TokenLogonDetails struct {
	AccountName  string
	RefreshToken string `json:"-" yaml:"-"`
	SteamID      uint64
	LoginID      uint32
}

// String redacts the refresh token when details are formatted.
func (TokenLogonDetails) String() string {
	return "{SteamID:<redacted> AccountName:<redacted> RefreshToken:<redacted> LoginID:<redacted>}"
}

// GoString redacts the refresh token when details are formatted with %#v.
func (TokenLogonDetails) GoString() string {
	return "cm.TokenLogonDetails{<redacted>}"
}

// LogonToken starts an individual SteamClient session using a refresh token.
func (c *Client) LogonToken(ctx context.Context, details TokenLogonDetails) (*Session, error) {
	if ctx == nil {
		return nil, errors.New("CM logon context is nil")
	}

	accountName := strings.TrimSpace(details.AccountName)
	if details.SteamID == 0 ||
		accountName == "" ||
		accountName != details.AccountName ||
		len(details.AccountName) > 128 ||
		details.RefreshToken == "" ||
		len(details.RefreshToken) > maxRefreshTokenBytes {
		return nil, errors.New("CM token logon details are incomplete or outside supported bounds")
	}

	if !validRefreshTokenShape(details.RefreshToken) {
		return nil, errors.New("CM refresh token has an unsupported format")
	}

	c.preAuthMu.Lock()
	defer c.preAuthMu.Unlock()

	conn, err := c.beginLogon()
	if err != nil {
		return nil, err
	}

	loginID := details.LoginID
	if loginID == 0 {
		loginID, err = randomLoginID()
		if err != nil {
			return nil, fmt.Errorf("generate CM login ID: %w", err)
		}
	}

	request, err := encodeProtoMessage(EMsgClientLogon, &steamHeader{
		Steamid:         uint64Pointer(details.SteamID),
		ClientSessionid: int32Pointer(0),
		JobidSource:     uint64Pointer(NoJobID),
		JobidTarget:     uint64Pointer(NoJobID),
	}, &steamLogon{
		ProtocolVersion:               uint32Value(anonymousLogonProtocolVersion),
		ShouldRememberPassword:        boolValue(true),
		ClientLanguage:                stringValue("english"),
		ClientOsType:                  uint32Value(clientOSType()),
		AccountName:                   stringValue(details.AccountName),
		ObfuscatedPrivateIp:           &steamproto.CMsgIPAddress{Ip: &steamproto.CMsgIPAddress_V4{V4: loginID}},
		DeprecatedObfuscatedPrivateIp: uint32Value(loginID),
		SupportsRateLimitResponse:     boolValue(true),
		AccessToken:                   stringValue(details.RefreshToken),
	})
	if err != nil {
		return nil, fmt.Errorf("encode individual CM logon: %w", err)
	}

	return c.exchangeLogon(ctx, conn, request, details.SteamID)
}

// validRefreshTokenShape checks the token's compact three-part character form.
// It does not verify the token with Steam; LogonToken performs that check.
func validRefreshTokenShape(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}

	for _, part := range parts {
		if part == "" {
			return false
		}

		for _, char := range part {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-", char) {
				return false
			}
		}
	}

	return true
}

// exchangeLogon reads until it finds the logon response
// and transfers any following frames from that packet to the new session reader.
func (c *Client) exchangeLogon(ctx context.Context, conn *WebSocketConn, request Frame, expectedSteamID uint64) (*Session, error) {
	if err := conn.WriteFrame(ctx, request, c.frameLimits); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("send CM logon: %w", err)
	}

	for {
		frame, err := conn.ReadFrame(ctx, c.frameLimits)
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("read CM logon response: %w", err)
		}

		frames := []Frame{frame}
		if frame.EMsg == EMsgMulti {
			frames, err = DecodeMulti(frame, MultiLimits{FrameLimits: c.frameLimits})
			if err != nil {
				_ = c.Close()
				return nil, fmt.Errorf("decode CM logon Multi: %w", err)
			}
		}

		for index, candidate := range frames {
			if candidate.EMsg != EMsgClientLogOnResponse {
				continue
			}

			// A Multi packet may carry ordinary session messages after logon.
			session, finishErr := c.finishLogon(conn, candidate, expectedSteamID, frames[index+1:])
			if finishErr != nil {
				_ = c.Close()
			}

			return session, finishErr
		}
	}
}

// Close closes the active CM connection.
func (c *Client) Close() error {
	c.mu.Lock()

	c.closed = true
	conn := c.conn
	session := c.session

	c.mu.Unlock()

	if conn == nil {
		return nil
	}
	if session != nil {
		return session.Close()
	}

	return conn.Close()
}

// beginLogon atomically claims the connected transport for one logon attempt.
func (c *Client) beginLogon() (*WebSocketConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, errors.New("CM client is closed")
	}
	if c.conn == nil {
		return nil, errors.New("CM client is not connected")
	}
	if c.logonStarted || c.session != nil {
		return nil, errors.New("CM client logon has already started")
	}

	c.logonStarted = true
	return c.conn, nil
}

// finishLogon validates the response identity and starts background session workers.
func (c *Client) finishLogon(conn *WebSocketConn, frame Frame, expectedSteamID uint64, pending []Frame) (*Session, error) {
	header := &steamHeader{}
	response := &steamLogonResponse{}
	if err := decodeProtoMessage(frame, header, response); err != nil {
		return nil, fmt.Errorf("decode CM logon response: %w", err)
	}

	eresult := response.GetEresult()
	if header.Eresult != nil {
		// The protobuf header is authoritative when it explicitly carries EResult.
		eresult = header.GetEresult()
	}
	if eresult != EResultOK {
		return nil, &LogonError{EResult: eresult, Message: header.GetErrorMessage()}
	}

	steamID := header.GetSteamid()
	if steamID == 0 {
		// Some CM responses put the authenticated ID only in the response body.
		steamID = response.GetClientSuppliedSteamid()
	}
	if steamID == 0 {
		return nil, errors.New("CM logon response did not contain SteamID")
	}
	if expectedSteamID != 0 && steamID != expectedSteamID {
		return nil, errors.New("CM logon response SteamID does not match requested account")
	}

	session := newSession(conn, SessionState{
		SteamID:           steamID,
		SessionID:         header.GetClientSessionid(),
		CellID:            response.GetCellId(),
		HeartbeatInterval: seconds(response.GetHeartbeatSeconds()),
	}, c.frameLimits)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("CM client was closed during logon")
	}

	c.session = session
	c.mu.Unlock()
	session.startReader(pending)
	session.startHeartbeat()

	return session, nil
}

// seconds converts a positive CM heartbeat interval to a duration.
func seconds(value int32) time.Duration {
	if value <= 0 {
		return 0
	}

	return time.Duration(value) * time.Second
}

// randomLoginID returns a non-zero obfuscation value for individual logon.
func randomLoginID() (uint32, error) {
	for {
		var bytes [4]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return 0, err
		}

		if value := binary.LittleEndian.Uint32(bytes[:]); value != 0 {
			return value, nil
		}
	}
}
