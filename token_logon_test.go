// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func TestLogonTokenPreservesPostLogonMultiMessages(t *testing.T) {
	const steamID = uint64(76561198000000001)
	const token = "synthetic.header.signature"
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		request, err := readWebSocketFrame(conn)
		if err != nil || request.EMsg != EMsgClientLogon {
			t.Errorf("read individual logon: frame=%#v err=%v", request, err)
			return
		}

		var header steamHeader
		var logon steamLogon
		if err := proto.Unmarshal(request.Header, &header); err != nil {
			t.Error(err)
			return
		}
		if err := proto.Unmarshal(request.Body, &logon); err != nil {
			t.Error(err)
			return
		}

		if header.GetSteamid() != steamID ||
			header.GetClientSessionid() != 0 ||
			logon.GetAccountName() != "test-account" ||
			logon.GetAccessToken() != token ||
			!logon.GetShouldRememberPassword() ||
			logon.GetAnonUserTargetAccountName() != "" ||
			logon.DeprecatedObfuscatedPrivateIp == nil ||
			logon.GetObfuscatedPrivateIp().GetV4() != logon.GetDeprecatedObfuscatedPrivateIp() ||
			logon.GetDeprecatedObfuscatedPrivateIp() == 0 {
			t.Error("individual logon request fields do not match the requested account")
			return
		}

		response, err := encodeProtoMessage(EMsgClientLogOnResponse, &steamHeader{
			Steamid:         uint64Pointer(steamID),
			ClientSessionid: int32Pointer(77),
		}, &steamLogonResponse{Eresult: int32Pointer(EResultOK)})
		if err != nil {
			t.Error(err)
			return
		}

		queued, err := encodeProtoMessage(EMsgClientHeartBeat, &steamHeader{}, &steamproto.CMsgClientHeartBeat{})
		if err != nil {
			t.Error(err)
			return
		}

		multi := mustMultiFrame(t, appendLengthPrefixed(mustEncodedFrame(t, response), mustEncodedFrame(t, queued)), false)
		encoded, err := EncodeFrame(multi, FrameLimits{})
		if err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
			return
		}

		loggedOff, err := encodeProtoMessage(
			EMsgClientLoggedOff,
			&steamHeader{},
			&steamproto.CMsgClientLoggedOff{Eresult: int32Pointer(EResultAccessDenied)},
		)
		if err != nil {
			return
		}

		encoded, err = EncodeFrame(loggedOff, FrameLimits{})
		if err == nil {
			_ = conn.WriteMessage(websocket.BinaryMessage, encoded)
		}
	}))
	defer server.Close()

	client, err := New(WithWebSocketOption(WithIOTimeout(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	if err := client.Connect(context.Background(), Endpoint{Address: wsURL}); err != nil {
		t.Fatal(err)
	}

	session, err := client.LogonToken(context.Background(), TokenLogonDetails{
		SteamID:      steamID,
		AccountName:  "test-account",
		RefreshToken: token,
		LoginID:      12345,
	})
	if err != nil {
		t.Fatalf("LogonToken() error = %v", err)
	}
	if session.SteamID != steamID || session.SessionID != 77 {
		t.Fatalf("session = %#v", session.SessionState)
	}
	if _, err := client.CallUnauthenticated(context.Background(), "Authentication.Test#1", nil); err == nil {
		t.Fatal("CallUnauthenticated() succeeded after user logon")
	}

	message, err := session.ReadMessage(context.Background())
	if err != nil || message.EMsg != EMsgClientHeartBeat {
		t.Fatalf("ReadMessage() = %#v, %v; want queued post-logon message", message, err)
	}

	message, err = session.ReadMessage(context.Background())
	var logoff *LogoffError
	if !errors.As(err, &logoff) || logoff.EResult != EResultAccessDenied {
		t.Fatalf("ReadMessage() = %#v, %v; want server logoff result", message, err)
	}

	formatted := fmt.Sprintf("%#v %v", TokenLogonDetails{RefreshToken: token}, session)
	if strings.Contains(formatted, token) {
		t.Fatalf("diagnostics expose refresh token: %s", formatted)
	}
}

func TestLogonTokenRejectsIdentityMismatch(t *testing.T) {
	const requestedSteamID = uint64(76561198000000001)
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if _, err := readWebSocketFrame(conn); err != nil {
			return
		}

		response, err := encodeProtoMessage(EMsgClientLogOnResponse, &steamHeader{
			Steamid: uint64Pointer(requestedSteamID + 1),
		}, &steamLogonResponse{Eresult: int32Pointer(EResultOK)})
		if err != nil {
			return
		}

		encoded, err := EncodeFrame(response, FrameLimits{})
		if err == nil {
			_ = conn.WriteMessage(websocket.BinaryMessage, encoded)
		}
	}))
	defer server.Close()

	client, err := New(WithWebSocketOption(WithIOTimeout(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	if err := client.Connect(context.Background(), Endpoint{Address: wsURL}); err != nil {
		t.Fatal(err)
	}

	_, err = client.LogonToken(context.Background(), TokenLogonDetails{
		SteamID:      requestedSteamID,
		AccountName:  "test-account",
		RefreshToken: "synthetic.token.signature",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("LogonToken() error = %v, want account identity mismatch", err)
	}
}
