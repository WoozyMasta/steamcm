// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

// Package auth implements SteamClient authentication over a pre-login CM
// connection.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	cm "github.com/woozymasta/steamcm"
	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

const (
	// getPasswordRSAPublicKeyMethod fetches Steam's temporary password key.
	getPasswordRSAPublicKeyMethod = "Authentication.GetPasswordRSAPublicKey#1"
	// beginCredentialsMethod starts username/password authentication.
	beginCredentialsMethod = "Authentication.BeginAuthSessionViaCredentials#1"
	// beginQRMethod starts mobile-app QR authentication.
	beginQRMethod = "Authentication.BeginAuthSessionViaQR#1"
	// updateGuardCodeMethod submits an email or device code.
	updateGuardCodeMethod = "Authentication.UpdateAuthSessionWithSteamGuardCode#1"
	// pollAuthStatusMethod checks whether a pending attempt completed.
	pollAuthStatusMethod = "Authentication.PollAuthSessionStatus#1"

	// maxTokenBytes bounds refresh tokens returned by Steam.
	maxTokenBytes = 64 << 10
	// maxRSAKeyBits caps the public key accepted from Steam.
	maxRSAKeyBits = 4096
	// minRSAKeyBits rejects password keys below the supported security floor.
	minRSAKeyBits = 2048
	// minPollInterval prevents a malformed response from causing a tight poll loop.
	minPollInterval = 250 * time.Millisecond
	// maxPollIntervalSecs rejects unreasonable server-provided poll delays.
	maxPollIntervalSecs = 60
)

// Transport is the subset of CM used for SteamClient authentication.
type Transport interface {
	CallUnauthenticated(context.Context, string, []byte) (cm.Message, error)
}

// Client performs SteamClient authentication through an unauthenticated CM connection.
// The caller owns and closes the connection.
type Client struct {
	// transport sends SteamClient calls over the pre-login CM connection.
	transport Transport
}

// Credentials contains account credentials for one login attempt.
type Credentials struct {
	AccountName string
	DeviceName  string
	Password    []byte
}

// String redacts the password when credentials are formatted for diagnostics.
func (Credentials) String() string {
	return "{AccountName:<redacted> Password:<redacted> DeviceName:<redacted>}"
}

// GoString redacts the password when credentials are formatted with %#v.
func (Credentials) GoString() string { return "auth.Credentials{<redacted>}" }

// Confirmation is one Steam Guard option offered for an authentication attempt.
// Confirmation options are alternatives; only one is needed.
type Confirmation struct {
	Message string
	Type    ConfirmationType
}

// ConfirmationType identifies a Steam Guard option.
type ConfirmationType int32

const (
	// ConfirmationNone indicates no additional Steam Guard step is required.
	ConfirmationNone ConfirmationType = 1
	// ConfirmationEmailCode requests a code sent to the account email.
	ConfirmationEmailCode ConfirmationType = 2
	// ConfirmationDeviceCode requests a code from an authenticated device.
	ConfirmationDeviceCode ConfirmationType = 3
	// ConfirmationDeviceConfirmation requests approval in the Steam mobile app.
	ConfirmationDeviceConfirmation ConfirmationType = 4
)

// Token contains the result needed to create an individual CM session.
type Token struct {
	AccountName  string `json:"account_name" yaml:"account_name"`
	RefreshToken string `json:"-"            yaml:"-"`
	GuardData    string `json:"-"            yaml:"-"`
	SteamID      uint64 `json:"steam_id"     yaml:"steam_id"`
}

// String never includes token or Guard data.
func (Token) String() string {
	return "{SteamID:<redacted> AccountName:<redacted> RefreshToken:<redacted> GuardData:<redacted>}"
}

// GoString never includes token or Guard data.
func (Token) GoString() string { return "auth.Token{<redacted>}" }

// PollResult describes whether authentication is pending or complete.
type PollResult struct {
	Token             *Token
	ChallengeURL      string
	NextPollInterval  time.Duration
	Pending           bool
	RemoteInteraction bool
}

// Attempt tracks a single Steam authentication flow.
type Attempt struct {
	// transport carries all calls for this authentication flow.
	transport Transport
	// account is the expected account name; QR flows leave it empty.
	account string
	// guardData is Steam's persisted authenticator context for the result token.
	guardData string
	// challengeURL is the current validated QR approval URL.
	challengeURL string
	// requestID correlates status polls with the original attempt.
	requestID []byte
	// guards contains the alternative confirmation methods offered by Steam.
	guards []Confirmation
	// clientID may be replaced by Steam while the attempt is pending.
	clientID uint64
	// steamID is known for credential flow and may be recovered from the QR token.
	steamID uint64
	// interval is the validated server-provided polling delay.
	interval time.Duration
	// mu protects mutable attempt state exposed to concurrent readers.
	mu sync.Mutex
	// busy prevents overlapping network operations on the same attempt.
	busy bool
	// done prevents operations after a refresh token has been issued.
	done bool
}

// New creates an authentication client using the supplied pre-login CM
// transport.
func New(transport Transport) (*Client, error) {
	if transport == nil {
		return nil, errors.New("steam auth transport is nil")
	}

	return &Client{transport: transport}, nil
}

// BeginCredentials starts a persistent SteamClient authentication attempt.
func (c *Client) BeginCredentials(ctx context.Context, credentials Credentials) (*Attempt, error) {
	if c == nil || c.transport == nil {
		return nil, errors.New("steam auth client is nil")
	}
	if ctx == nil {
		return nil, errors.New("steam auth context is nil")
	}

	account := strings.TrimSpace(credentials.AccountName)
	if account == "" || len(account) > 128 {
		return nil, errors.New("steam account name is empty or too long")
	}
	if len(credentials.Password) == 0 {
		return nil, errors.New("steam password is empty")
	}

	deviceName := strings.TrimSpace(credentials.DeviceName)
	if deviceName == "" || len(deviceName) > 128 {
		return nil, errors.New("steam device name is empty or too long")
	}

	publicKeyResponse := &steamproto.CAuthentication_GetPasswordRSAPublicKey_Response{}
	if err := c.call(ctx, getPasswordRSAPublicKeyMethod,
		&steamproto.CAuthentication_GetPasswordRSAPublicKey_Request{AccountName: stringPointer(account)},
		publicKeyResponse); err != nil {
		return nil, fmt.Errorf("get Steam password encryption key: %w", err)
	}

	publicKey, err := parsePublicKey(publicKeyResponse)
	if err != nil {
		return nil, err
	}

	passwordBytes := append([]byte(nil), credentials.Password...)
	ciphertext, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey.key, passwordBytes)

	// Clear the temporary plaintext copy whether encryption succeeded or failed.
	for index := range passwordBytes {
		passwordBytes[index] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("encrypt Steam password: %w", err)
	}

	device := &steamproto.CAuthentication_DeviceDetails{
		DeviceFriendlyName: stringPointer(deviceName),
		PlatformType:       steamproto.EAuthTokenPlatformType_EAuthTokenPlatformTypeSteamClient.Enum(),
		OsType:             int32Pointer(clientOSTypeAuth()),
	}
	request := &steamproto.CAuthentication_BeginAuthSessionViaCredentials_Request{
		DeviceFriendlyName:  stringPointer(deviceName),
		AccountName:         stringPointer(account),
		EncryptedPassword:   stringPointer(base64.StdEncoding.EncodeToString(ciphertext)),
		EncryptionTimestamp: uint64Pointer(publicKeyResponse.GetTimestamp()),
		RememberLogin:       boolPointer(true),
		PlatformType:        steamproto.EAuthTokenPlatformType_EAuthTokenPlatformTypeSteamClient.Enum(),
		Persistence:         steamproto.ESessionPersistence_ESessionPersistencePersistent.Enum(),
		WebsiteId:           stringPointer("Unknown"),
		DeviceDetails:       device,
		QosLevel:            int32Pointer(2),
	}
	response := &steamproto.CAuthentication_BeginAuthSessionViaCredentials_Response{}

	if err := c.call(ctx, beginCredentialsMethod, request, response); err != nil {
		return nil, fmt.Errorf("begin Steam credential authentication: %w", err)
	}
	if response.GetClientId() == 0 || len(response.GetRequestId()) == 0 || len(response.GetRequestId()) > 128 {
		return nil, errors.New("steam authentication response has invalid attempt identifiers")
	}
	if response.GetSteamid() == 0 {
		return nil, errors.New("steam authentication response did not identify the account")
	}

	interval, err := pollInterval(response.Interval)
	if err != nil {
		return nil, err
	}

	// Steam lists alternatives; retain each usable choice for caller selection.
	guards := make([]Confirmation, 0, len(response.GetAllowedConfirmations()))
	for _, guard := range response.GetAllowedConfirmations() {
		if guard == nil {
			continue
		}

		guards = append(guards, Confirmation{
			Type:    ConfirmationType(guard.GetConfirmationType()),
			Message: guard.GetAssociatedMessage(),
		})
	}

	return &Attempt{
		transport: c.transport,
		account:   account,
		clientID:  response.GetClientId(),
		requestID: append([]byte(nil), response.GetRequestId()...),
		steamID:   response.GetSteamid(),
		interval:  interval,
		guards:    guards,
	}, nil
}

// BeginQR starts a persistent SteamClient authentication attempt
// that uses mobile-app QR approval instead of account credentials.
func (c *Client) BeginQR(ctx context.Context, deviceName string) (*Attempt, error) {
	if c == nil || c.transport == nil {
		return nil, errors.New("steam auth client is nil")
	}
	if ctx == nil {
		return nil, errors.New("steam auth context is nil")
	}

	deviceName = strings.TrimSpace(deviceName)
	if deviceName == "" || len(deviceName) > 128 {
		return nil, errors.New("steam device name is empty or too long")
	}

	device := &steamproto.CAuthentication_DeviceDetails{
		DeviceFriendlyName: stringPointer(deviceName),
		PlatformType:       steamproto.EAuthTokenPlatformType_EAuthTokenPlatformTypeSteamClient.Enum(),
		OsType:             int32Pointer(clientOSTypeAuth()),
	}
	request := &steamproto.CAuthentication_BeginAuthSessionViaQR_Request{
		DeviceFriendlyName: stringPointer(deviceName),
		PlatformType:       steamproto.EAuthTokenPlatformType_EAuthTokenPlatformTypeSteamClient.Enum(),
		DeviceDetails:      device,
		WebsiteId:          stringPointer("Unknown"),
	}
	response := &steamproto.CAuthentication_BeginAuthSessionViaQR_Response{}

	if err := c.call(ctx, beginQRMethod, request, response); err != nil {
		return nil, fmt.Errorf("begin Steam QR authentication: %w", err)
	}
	if response.GetClientId() == 0 || len(response.GetRequestId()) == 0 || len(response.GetRequestId()) > 128 {
		return nil, errors.New("steam QR authentication response has invalid attempt identifiers")
	}
	if !validChallengeURL(response.GetChallengeUrl()) {
		return nil, errors.New("steam QR authentication response has an invalid challenge URL")
	}

	interval, err := pollInterval(response.Interval)
	if err != nil {
		return nil, err
	}

	return &Attempt{
		transport:    c.transport,
		clientID:     response.GetClientId(),
		requestID:    append([]byte(nil), response.GetRequestId()...),
		interval:     interval,
		challengeURL: response.GetChallengeUrl(),
	}, nil
}

// Confirmations returns a copy of the Steam Guard options offered by Steam.
func (a *Attempt) Confirmations() []Confirmation {
	if a == nil {
		return nil
	}

	return append([]Confirmation(nil), a.guards...)
}

// ChallengeURL returns the current Steam mobile-app QR challenge URL.
func (a *Attempt) ChallengeURL() string {
	if a == nil {
		return ""
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	return a.challengeURL
}

// SubmitCode submits an email or device Steam Guard code for this attempt.
func (a *Attempt) SubmitCode(ctx context.Context, kind ConfirmationType, code string) error {
	if err := a.beginOperation(ctx); err != nil {
		return err
	}
	defer a.endOperation()

	if kind != ConfirmationEmailCode && kind != ConfirmationDeviceCode {
		return &UnsupportedConfirmationError{Type: kind}
	}
	if !a.offers(kind) {
		return &UnsupportedConfirmationError{Type: kind}
	}

	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return errors.New("steam Guard code is empty or too long")
	}

	request := &steamproto.CAuthentication_UpdateAuthSessionWithSteamGuardCode_Request{
		ClientId: uint64Pointer(a.clientID),
		Steamid:  uint64Pointer(a.steamID),
		Code:     stringPointer(code),
		CodeType: steamproto.EAuthSessionGuardType(kind).Enum(),
	}
	response := &steamproto.CAuthentication_UpdateAuthSessionWithSteamGuardCode_Response{}

	if err := call(ctx, a.transport, updateGuardCodeMethod, request, response); err != nil {
		return fmt.Errorf("submit Steam Guard code: %w", err)
	}

	return nil
}

// PollOnce checks whether Steam completed this authentication attempt.
func (a *Attempt) PollOnce(ctx context.Context) (PollResult, error) {
	if err := a.beginOperation(ctx); err != nil {
		return PollResult{}, err
	}
	defer a.endOperation()

	request := &steamproto.CAuthentication_PollAuthSessionStatus_Request{
		ClientId:  uint64Pointer(a.clientID),
		RequestId: append([]byte(nil), a.requestID...),
	}
	response := &steamproto.CAuthentication_PollAuthSessionStatus_Response{}

	if err := call(ctx, a.transport, pollAuthStatusMethod, request, response); err != nil {
		return PollResult{}, fmt.Errorf("poll Steam authentication: %w", err)
	}
	if response.GetNewClientId() != 0 {
		a.clientID = response.GetNewClientId()
	}
	if response.GetNewGuardData() != "" {
		a.guardData = response.GetNewGuardData()
	}

	if response.GetNewChallengeUrl() != "" {
		if !validChallengeURL(response.GetNewChallengeUrl()) {
			return PollResult{}, errors.New("steam returned an invalid QR challenge URL")
		}
		a.mu.Lock()
		a.challengeURL = response.GetNewChallengeUrl()
		a.mu.Unlock()
	}

	if response.GetRefreshToken() == "" {
		return PollResult{
			Pending:           true,
			RemoteInteraction: response.GetHadRemoteInteraction(),
			NextPollInterval:  a.interval,
			ChallengeURL:      a.ChallengeURL(),
		}, nil
	}
	if len(response.GetRefreshToken()) > maxTokenBytes {
		return PollResult{}, errors.New("steam returned an oversized refresh token")
	}
	if !validRefreshTokenShape(response.GetRefreshToken()) {
		return PollResult{}, errors.New("steam returned a refresh token with an unsupported format")
	}
	if response.GetAccountName() == "" ||
		(a.account != "" && !strings.EqualFold(response.GetAccountName(), a.account)) {
		return PollResult{}, errors.New("steam authentication response account name is missing or inconsistent")
	}

	steamID := a.steamID
	if steamID == 0 {
		// QR attempts do not know the account ID until Steam issues the refresh token.
		parsedSteamID, err := steamIDFromRefreshToken(response.GetRefreshToken())
		if err != nil {
			return PollResult{}, err
		}

		steamID = parsedSteamID
	}

	a.done = true

	return PollResult{
		RemoteInteraction: response.GetHadRemoteInteraction(),
		Token: &Token{
			SteamID:      steamID,
			AccountName:  response.GetAccountName(),
			RefreshToken: response.GetRefreshToken(),
			GuardData:    a.guardData,
		},
	}, nil
}

// steamIDFromRefreshToken reads the SteamID claim needed for QR logon results.
func steamIDFromRefreshToken(token string) (uint64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, errors.New("steam QR token has an unsupported format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, errors.New("steam QR token identity is malformed")
	}

	var claims struct {
		Subject string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Subject == "" {
		return 0, errors.New("steam QR token does not contain account identity")
	}

	steamID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || steamID == 0 {
		return 0, errors.New("steam QR token has an invalid account identity")
	}

	return steamID, nil
}

// validChallengeURL accepts only HTTPS challenge links hosted by Steam's s.team domain.
func validChallengeURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil &&
		parsed.Scheme == "https" &&
		strings.EqualFold(parsed.Hostname(), "s.team") &&
		parsed.User == nil &&
		parsed.Path != "" &&
		parsed.RawQuery == "" &&
		parsed.Fragment == ""
}

// beginOperation validates the attempt and excludes concurrent network calls.
func (a *Attempt) beginOperation(ctx context.Context) error {
	if a == nil || a.transport == nil {
		return errors.New("steam authentication attempt is nil")
	}
	if ctx == nil {
		return errors.New("steam auth context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.done {
		return errors.New("steam authentication attempt is complete")
	}
	if a.busy {
		return errors.New("steam authentication attempt already has an operation in progress")
	}

	a.busy = true
	return nil
}

// endOperation releases the attempt for its next caller operation.
func (a *Attempt) endOperation() {
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()
}

// offers reports whether Steam listed this Guard method for the attempt.
func (a *Attempt) offers(kind ConfirmationType) bool {
	for _, confirmation := range a.guards {
		if confirmation.Type == kind {
			return true
		}
	}

	return false
}

// publicKey holds the parsed RSA key used to encrypt a Steam password.
type publicKey struct {
	// key is the validated modulus and exponent from Steam's key response.
	key *rsa.PublicKey
}

// parsePublicKey validates the server-supplied RSA parameters before use.
func parsePublicKey(response *steamproto.CAuthentication_GetPasswordRSAPublicKey_Response) (publicKey, error) {
	if response == nil || response.Timestamp == nil || response.GetTimestamp() == 0 {
		return publicKey{}, errors.New("steam password key response is missing timestamp")
	}

	modulusText := response.GetPublickeyMod()
	exponentText := response.GetPublickeyExp()
	if len(modulusText) < minRSAKeyBits/4 || len(modulusText) > maxRSAKeyBits/4 ||
		len(exponentText) == 0 || len(exponentText) > 8 {
		return publicKey{}, errors.New("steam password key response has invalid key size")
	}

	modulusBytes, err := hex.DecodeString(modulusText)
	if err != nil {
		return publicKey{}, errors.New("steam password key modulus is not valid hexadecimal")
	}

	exponentBytes, err := hex.DecodeString(exponentText)
	if err != nil {
		return publicKey{}, errors.New("steam password key exponent is not valid hexadecimal")
	}

	n := new(big.Int).SetBytes(modulusBytes)
	e := new(big.Int).SetBytes(exponentBytes)
	if n.BitLen() < minRSAKeyBits ||
		n.BitLen() > maxRSAKeyBits ||
		!e.IsInt64() ||
		e.Int64() < 3 ||
		e.Int64() > math.MaxUint32 ||
		e.Bit(0) == 0 {
		return publicKey{}, errors.New("steam password key parameters are outside supported bounds")
	}

	return publicKey{key: &rsa.PublicKey{N: n, E: int(e.Int64())}}, nil
}

// pollInterval validates Steam's interval and applies a small lower bound.
func pollInterval(value *float32) (time.Duration, error) {
	if value == nil ||
		math.IsNaN(float64(*value)) ||
		math.IsInf(float64(*value), 0) ||
		*value <= 0 ||
		*value > maxPollIntervalSecs {
		return 0, errors.New("steam authentication response has invalid polling interval")
	}

	interval := time.Duration(float64(*value) * float64(time.Second))

	return max(interval, minPollInterval), nil
}

// validRefreshTokenShape checks compact token syntax, not token authenticity.
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

// call sends one typed protobuf request using this client's transport.
func (c *Client) call(ctx context.Context, method string, request, response proto.Message) error {
	return call(ctx, c.transport, method, request, response)
}

// call marshals a request, invokes Steam Unified Messaging, and decodes its reply.
func call(ctx context.Context, transport Transport, method string, request, response proto.Message) error {
	body, err := proto.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal Steam auth request: %w", err)
	}

	message, err := transport.CallUnauthenticated(ctx, method, body)
	if err != nil {
		return classifyRemoteError(err)
	}
	if err := proto.Unmarshal(message.Body, response); err != nil {
		return fmt.Errorf("decode Steam auth response: %w", err)
	}

	return nil
}

// clientOSTypeAuth maps the runtime platform to Steam authentication's OS code.
func clientOSTypeAuth() int32 {
	switch runtime.GOOS {
	case "windows":
		return 16

	case "darwin":
		return -102

	case "linux":
		return -203

	default:
		return -1
	}
}

// stringPointer sets optional protobuf string fields, including empty values.
func stringPointer(value string) *string {
	return &value
}

// int32Pointer sets optional protobuf int32 fields, including zero values.
func int32Pointer(value int32) *int32 {
	return &value
}

// uint64Pointer sets optional protobuf uint64 fields, including zero values.
func uint64Pointer(value uint64) *uint64 {
	return &value
}

// boolPointer sets optional protobuf bool fields, including false values.
func boolPointer(value bool) *bool {
	return &value
}
