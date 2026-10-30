// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	cm "github.com/woozymasta/steamcm"
	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
	"google.golang.org/protobuf/proto"
)

func TestCredentialGuardAndPollingFlow(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	transport := &authTestTransport{t: t}
	transport.call = func(method string, body []byte) ([]byte, error) {
		switch method {
		case getPasswordRSAPublicKeyMethod:
			var request steamproto.CAuthentication_GetPasswordRSAPublicKey_Request
			if err := proto.Unmarshal(body, &request); err != nil || request.GetAccountName() != "test-account" {
				t.Errorf("RSA request = %v, err = %v", &request, err)
			}

			return marshalAuth(t, &steamproto.CAuthentication_GetPasswordRSAPublicKey_Response{
				PublickeyMod: stringPointer(hex.EncodeToString(privateKey.N.Bytes())),
				PublickeyExp: stringPointer("010001"),
				Timestamp:    uint64Pointer(987654),
			}), nil

		case beginCredentialsMethod:
			var request steamproto.CAuthentication_BeginAuthSessionViaCredentials_Request
			if err := proto.Unmarshal(body, &request); err != nil {
				return nil, err
			}

			ciphertext, err := base64.StdEncoding.DecodeString(request.GetEncryptedPassword())
			if err != nil {
				return nil, err
			}

			password, err := rsa.DecryptPKCS1v15(rand.Reader, privateKey, ciphertext)
			if err != nil || string(password) != "test-password" {
				return nil, errors.New("encrypted password did not decrypt to the synthetic password")
			}

			if request.GetEncryptionTimestamp() != 987654 || !request.GetRememberLogin() ||
				request.GetPlatformType() != steamproto.EAuthTokenPlatformType_EAuthTokenPlatformTypeSteamClient ||
				request.GetPersistence() != steamproto.ESessionPersistence_ESessionPersistencePersistent ||
				request.GetDeviceDetails().GetPlatformType() != steamproto.EAuthTokenPlatformType_EAuthTokenPlatformTypeSteamClient {
				t.Errorf("begin credentials request has unexpected platform or persistence: %v", &request)
			}

			return marshalAuth(t, &steamproto.CAuthentication_BeginAuthSessionViaCredentials_Response{
				ClientId:  uint64Pointer(10),
				RequestId: []byte{1, 2, 3},
				Interval:  float32Pointer(0.05),
				Steamid:   uint64Pointer(76561198000000001),
				AllowedConfirmations: []*steamproto.CAuthentication_AllowedConfirmation{{
					ConfirmationType:  steamproto.EAuthSessionGuardType_EAuthSessionGuardTypeEmailCode.Enum(),
					AssociatedMessage: stringPointer("a***@example.test"),
				}, {
					ConfirmationType: steamproto.EAuthSessionGuardType_EAuthSessionGuardTypeDeviceConfirmation.Enum(),
				}},
			}), nil

		case updateGuardCodeMethod:
			var request steamproto.CAuthentication_UpdateAuthSessionWithSteamGuardCode_Request
			if err := proto.Unmarshal(body, &request); err != nil || request.GetClientId() != 10 ||
				request.GetSteamid() != 76561198000000001 || request.GetCode() != "12345" ||
				request.GetCodeType() != steamproto.EAuthSessionGuardType_EAuthSessionGuardTypeEmailCode {
				return nil, fmt.Errorf("unexpected Guard request: %v: %w", &request, err)
			}

			return marshalAuth(t, &steamproto.CAuthentication_UpdateAuthSessionWithSteamGuardCode_Response{}), nil

		case pollAuthStatusMethod:
			var request steamproto.CAuthentication_PollAuthSessionStatus_Request
			if err := proto.Unmarshal(body, &request); err != nil {
				return nil, err
			}

			if len(transport.pollIDs) == 0 {
				transport.pollIDs = append(transport.pollIDs, request.GetClientId())
				return marshalAuth(t, &steamproto.CAuthentication_PollAuthSessionStatus_Response{
					NewClientId:          uint64Pointer(20),
					HadRemoteInteraction: boolPointer(true),
				}), nil
			}

			transport.pollIDs = append(transport.pollIDs, request.GetClientId())
			return marshalAuth(t, &steamproto.CAuthentication_PollAuthSessionStatus_Response{
				RefreshToken: stringPointer("synthetic.header.signature"),
				AccountName:  stringPointer("TEST-ACCOUNT"),
				NewGuardData: stringPointer("synthetic-guard-data"),
			}), nil

		default:
			return nil, fmt.Errorf("unexpected authentication method %q", method)
		}
	}

	client, err := New(transport)
	if err != nil {
		t.Fatal(err)
	}

	attempt, err := client.BeginCredentials(context.Background(), Credentials{
		AccountName: "test-account",
		Password:    []byte("test-password"),
		DeviceName:  "test-host",
	})
	if err != nil {
		t.Fatalf("BeginCredentials() error = %v", err)
	}
	if confirmations := attempt.Confirmations(); len(confirmations) != 2 || confirmations[0].Type != ConfirmationEmailCode {
		t.Fatalf("Confirmations() = %#v", confirmations)
	}
	if err := attempt.SubmitCode(context.Background(), ConfirmationEmailCode, "12345"); err != nil {
		t.Fatalf("SubmitCode() error = %v", err)
	}
	if err := attempt.SubmitCode(context.Background(), ConfirmationDeviceConfirmation, "12345"); err == nil {
		t.Fatal("SubmitCode() accepted unsupported confirmation type")
	}

	pending, err := attempt.PollOnce(context.Background())
	if err != nil || !pending.Pending || !pending.RemoteInteraction || pending.NextPollInterval != minPollInterval {
		t.Fatalf("first PollOnce() = %#v, %v", pending, err)
	}

	completed, err := attempt.PollOnce(context.Background())
	if err != nil || completed.Pending || completed.Token == nil {
		t.Fatalf("second PollOnce() = %#v, %v", completed, err)
	}
	if completed.Token.SteamID != 76561198000000001 || completed.Token.AccountName != "TEST-ACCOUNT" ||
		completed.Token.GuardData != "synthetic-guard-data" {
		t.Fatalf("completed token = %#v", completed.Token)
	}
	if len(transport.pollIDs) != 2 || transport.pollIDs[0] != 10 || transport.pollIDs[1] != 20 {
		t.Fatalf("poll client IDs = %v, want [10 20]", transport.pollIDs)
	}

	formatted := fmt.Sprintf("%#v %v", *completed.Token, Credentials{Password: []byte("test-password")})
	for _, secret := range []string{"synthetic.header.signature", "synthetic-guard-data", "test-password"} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("formatted diagnostics expose secret %q: %s", secret, formatted)
		}
	}
	if _, err := attempt.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce() succeeded after completion")
	}
}

func TestQRAuthenticationFlow(t *testing.T) {
	steamID := uint64(76561198000000001)
	refreshToken := "header." + base64.RawURLEncoding.EncodeToString(
		[]byte(`{"sub":"76561198000000001"}`),
	) + ".signature"
	transport := &authTestTransport{t: t}
	transport.call = func(method string, body []byte) ([]byte, error) {
		switch method {
		case beginQRMethod:
			var request steamproto.CAuthentication_BeginAuthSessionViaQR_Request
			if err := proto.Unmarshal(body, &request); err != nil {
				return nil, err
			}

			if request.GetPlatformType() != steamproto.EAuthTokenPlatformType_EAuthTokenPlatformTypeSteamClient ||
				request.GetDeviceDetails().GetDeviceFriendlyName() != "test-host" {
				return nil, errors.New("unexpected QR authentication request")
			}

			return marshalAuth(t, &steamproto.CAuthentication_BeginAuthSessionViaQR_Response{
				ClientId: uint64Pointer(10), RequestId: []byte{1, 2, 3},
				ChallengeUrl: stringPointer("https://s.team/q/synthetic-challenge"),
				Interval:     float32Pointer(0.25),
			}), nil

		case pollAuthStatusMethod:
			var request steamproto.CAuthentication_PollAuthSessionStatus_Request
			if err := proto.Unmarshal(body, &request); err != nil {
				return nil, err
			}

			if len(transport.pollIDs) == 0 {
				transport.pollIDs = append(transport.pollIDs, request.GetClientId())
				return marshalAuth(t, &steamproto.CAuthentication_PollAuthSessionStatus_Response{
					NewClientId:     uint64Pointer(20),
					NewChallengeUrl: stringPointer("https://s.team/q/updated-challenge"),
				}), nil
			}

			transport.pollIDs = append(transport.pollIDs, request.GetClientId())
			return marshalAuth(t, &steamproto.CAuthentication_PollAuthSessionStatus_Response{
				RefreshToken: stringPointer(refreshToken), AccountName: stringPointer("qr-account"),
			}), nil

		default:
			return nil, fmt.Errorf("unexpected authentication method %q", method)
		}
	}

	client, err := New(transport)
	if err != nil {
		t.Fatal(err)
	}

	attempt, err := client.BeginQR(context.Background(), "test-host")
	if err != nil {
		t.Fatalf("BeginQR() error = %v", err)
	}
	if attempt.ChallengeURL() != "https://s.team/q/synthetic-challenge" {
		t.Fatalf("ChallengeURL() = %q", attempt.ChallengeURL())
	}

	pending, err := attempt.PollOnce(context.Background())
	if err != nil || !pending.Pending || pending.ChallengeURL != "https://s.team/q/updated-challenge" {
		t.Fatalf("first PollOnce() = %#v, %v", pending, err)
	}

	completed, err := attempt.PollOnce(context.Background())
	if err != nil || completed.Token == nil {
		t.Fatalf("second PollOnce() = %#v, %v", completed, err)
	}
	if completed.Token.SteamID != steamID || completed.Token.AccountName != "qr-account" {
		t.Fatalf("QR token = %#v", completed.Token)
	}
	if len(transport.pollIDs) != 2 || transport.pollIDs[0] != 10 || transport.pollIDs[1] != 20 {
		t.Fatalf("poll client IDs = %v", transport.pollIDs)
	}
}

func TestValidChallengeURLRequiresSteamHost(t *testing.T) {
	for _, test := range []struct {
		url   string
		valid bool
	}{
		{url: "https://s.team/q/challenge", valid: true},
		{url: "http://s.team/q/challenge"},
		{url: "https://example.test/q/challenge"},
		{url: "https://s.team.evil.test/q/challenge"},
		{url: "https://user@s.team/q/challenge"},
		{url: "https://s.team/q/challenge?token=secret"},
	} {
		if got := validChallengeURL(test.url); got != test.valid {
			t.Errorf("validChallengeURL(%q) = %t, want %t", test.url, got, test.valid)
		}
	}
}

func TestParsePublicKeyRejectsWeakAndMalformedKey(t *testing.T) {
	tests := []struct {
		name     string
		response *steamproto.CAuthentication_GetPasswordRSAPublicKey_Response
	}{
		{
			name:     "missing timestamp",
			response: &steamproto.CAuthentication_GetPasswordRSAPublicKey_Response{},
		},
		{
			name: "malformed modulus",
			response: &steamproto.CAuthentication_GetPasswordRSAPublicKey_Response{
				PublickeyMod: stringPointer("not-hex"),
				PublickeyExp: stringPointer("010001"),
				Timestamp:    uint64Pointer(1),
			},
		},
		{
			name: "weak modulus",
			response: &steamproto.CAuthentication_GetPasswordRSAPublicKey_Response{
				PublickeyMod: stringPointer(hex.EncodeToString(big.NewInt(65537).Bytes())),
				PublickeyExp: stringPointer("010001"),
				Timestamp:    uint64Pointer(1),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parsePublicKey(test.response); err == nil {
				t.Fatal("parsePublicKey() error = nil")
			}
		})
	}
}

func TestClassifyRemoteAuthenticationErrors(t *testing.T) {
	tests := []struct {
		result   int32
		category FailureCategory
	}{
		{result: 5, category: FailureInvalidCredentials},
		{result: 18, category: FailureInvalidCredentials},
		{result: 65, category: FailureGuardCodeRejected},
		{result: 71, category: FailureGuardCodeRejected},
		{result: 88, category: FailureGuardCodeRejected},
		{result: 27, category: FailureAttemptExpired},
		{result: 84, category: FailureRateLimited},
		{result: 29, category: FailureDuplicateRequest},
		{result: 15, category: FailureUnclassified},
	}

	for _, test := range tests {
		err := classifyRemoteError(&cm.JobError{EResult: test.result})
		var remote *RemoteError
		if !errors.As(err, &remote) ||
			remote.EResult != test.result ||
			remote.Category != test.category {
			t.Errorf("classifyRemoteError(EResult %d) = %v, want %s", test.result, err, test.category)
		}
	}
}

type authTestTransport struct {
	t       *testing.T
	call    func(string, []byte) ([]byte, error)
	pollIDs []uint64
}

func (f *authTestTransport) CallUnauthenticated(_ context.Context, method string, body []byte) (cm.Message, error) {
	f.t.Helper()
	response, err := f.call(method, body)
	if err != nil {
		return cm.Message{}, err
	}

	return cm.Message{Body: response, Header: cm.Header{HasEResult: true, EResult: cm.EResultOK}}, nil
}

func marshalAuth(t *testing.T, message proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func float32Pointer(value float32) *float32 {
	return &value
}
