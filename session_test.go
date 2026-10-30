// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAnonymousSessionAndHeartbeat(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	heartbeatReceived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		messageType, payload, err := conn.ReadMessage()
		if err != nil || messageType != websocket.BinaryMessage {
			return
		}

		logonFrame, err := DecodeFrame(payload, FrameLimits{})
		if err != nil || logonFrame.EMsg != EMsgClientLogon {
			return
		}

		logon := &steamLogon{}
		if err := decodeProtoMessage(logonFrame, &steamHeader{}, logon); err != nil || logon.GetAnonUserTargetAccountName() != "anonymous" {
			return
		}

		responseHeader := &steamHeader{
			Steamid:         uint64Pointer(76561198000000001),
			ClientSessionid: int32Pointer(42),
		}
		response, err := encodeProtoMessage(EMsgClientLogOnResponse, responseHeader, &steamLogonResponse{
			Eresult:          int32Pointer(1),
			HeartbeatSeconds: int32Pointer(1),
			CellId:           uint32Value(7),
		})
		if err != nil {
			return
		}

		encoded, err := EncodeFrame(response, FrameLimits{})
		if err != nil {
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
			return
		}

		for {
			messageType, payload, err = conn.ReadMessage()
			if err != nil || messageType != websocket.BinaryMessage {
				return
			}

			frame, err := DecodeFrame(payload, FrameLimits{})
			if err == nil && frame.EMsg == EMsgClientHeartBeat {
				heartbeatReceived <- struct{}{}
				return
			}
		}
	}))
	defer server.Close()

	client, err := New(WithWebSocketOption(WithIOTimeout(500 * time.Millisecond)))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	if err := client.Connect(context.Background(), Endpoint{Address: wsURL}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	session, err := client.LogonAnonymous(context.Background())
	if err != nil {
		t.Fatalf("LogonAnonymous() error = %v", err)
	}
	if session.SteamID != 76561198000000001 || session.SessionID != 42 || session.CellID != 7 {
		t.Fatalf("session state = %#v, want assigned values", session.SessionState)
	}
	defer func() { _ = session.Close() }()

	select {
	case <-heartbeatReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for heartbeat")
	}
}
