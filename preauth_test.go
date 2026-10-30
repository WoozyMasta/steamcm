// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func TestCallUnauthenticatedSendsHelloAndCorrelatesMultiResponse(t *testing.T) {
	const method = "Authentication.GetPasswordRSAPublicKey#1"
	requestBody := []byte{0x0a, 0x03, 'u', 's', 'r'}
	serverErr := make(chan error, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.Close() }()

		hello, err := readWebSocketFrame(conn)
		if err != nil || hello.EMsg != EMsgClientHello {
			serverErr <- errors.New("first frame was not ClientHello")
			return
		}

		var helloMessage steamproto.CMsgClientHello
		if err := proto.Unmarshal(hello.Body, &helloMessage); err != nil ||
			helloMessage.GetProtocolVersion() != anonymousLogonProtocolVersion {
			serverErr <- errors.New("ClientHello protocol version is invalid")
			return
		}

		request, err := readWebSocketFrame(conn)
		if err != nil || request.EMsg != EMsgServiceMethodCallFromClientNonAuthed {
			serverErr <- errors.New("second frame was not an unauthenticated UM call")
			return
		}

		requestHeader := &steamHeader{}
		if err := proto.Unmarshal(request.Header, requestHeader); err != nil ||
			requestHeader.GetTargetJobName() != method ||
			requestHeader.GetSteamid() != 0 ||
			requestHeader.GetJobidTarget() != NoJobID ||
			requestHeader.GetJobidSource() == NoJobID ||
			string(request.Body) != string(requestBody) {
			serverErr <- errors.New("unauthenticated UM request header or body is invalid")
			return
		}

		unrelated := mustEncodedFrame(t, Frame{EMsg: EMsgClientHeartBeat, Proto: true})
		responseHeader, err := proto.Marshal(&steamHeader{
			JobidTarget: uint64Pointer(requestHeader.GetJobidSource()),
			Eresult:     int32Pointer(EResultOK),
		})
		if err != nil {
			serverErr <- err
			return
		}

		response := mustEncodedFrame(t, Frame{
			EMsg:   EMsgServiceMethodResponse,
			Proto:  true,
			Header: responseHeader,
			Body:   []byte{9, 8, 7},
		})

		legacy := make([]byte, 20)
		binary.LittleEndian.PutUint32(legacy[0:4], EMsgClientHeartBeat)
		binary.LittleEndian.PutUint64(legacy[4:12], NoJobID)
		binary.LittleEndian.PutUint64(legacy[12:20], NoJobID)
		multi := mustMultiFrame(t, appendLengthPrefixed(unrelated, legacy, response), false)

		encoded, err := EncodeFrame(multi, FrameLimits{})
		if err != nil {
			serverErr <- err
			return
		}

		serverErr <- conn.WriteMessage(websocket.BinaryMessage, encoded)
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

	got, err := client.CallUnauthenticated(context.Background(), method, requestBody)
	if err != nil {
		t.Fatalf("CallUnauthenticated() error = %v", err)
	}
	if got.EMsg != EMsgServiceMethodResponse || string(got.Body) != string([]byte{9, 8, 7}) {
		t.Fatalf("CallUnauthenticated() = %#v, want correlated response", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("scripted peer: %v", err)
	}
}

func TestCallUnauthenticatedReadCancellationClosesConnection(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if _, err := readWebSocketFrame(conn); err != nil {
			return
		}
		if _, err := readWebSocketFrame(conn); err != nil {
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	client, err := New(WithWebSocketOption(WithIOTimeout(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	if err := client.Connect(context.Background(), Endpoint{Address: wsURL}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err = client.CallUnauthenticated(ctx, "Authentication.Test#1", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CallUnauthenticated() error = %v, want deadline exceeded", err)
	}
	if _, err := client.CallUnauthenticated(context.Background(), "Authentication.Test#1", nil); err == nil {
		t.Fatal("call after canceled read succeeded on an unusable connection")
	}
}

func readWebSocketFrame(conn *websocket.Conn) (Frame, error) {
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return Frame{}, err
	}

	if messageType != websocket.BinaryMessage {
		return Frame{}, errors.New("message is not binary")
	}

	return DecodeFrame(payload, FrameLimits{})
}
