// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func TestValidateJobResult(t *testing.T) {
	tests := []struct {
		name        string
		request     JobRequest
		header      Header
		wantMissing bool
		wantJob     bool
	}{
		{
			name:    "optional result may be absent",
			request: JobRequest{},
			header:  Header{},
		},
		{
			name:        "required result may not be absent",
			request:     JobRequest{RequireEResult: true},
			header:      Header{},
			wantMissing: true,
		},
		{
			name:    "explicit failure",
			request: JobRequest{},
			header:  Header{HasEResult: true, EResult: EResultFail},
			wantJob: true,
		},
		{
			name:    "explicit success",
			request: JobRequest{RequireEResult: true},
			header:  Header{HasEResult: true, EResult: EResultOK},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJobResult(tt.request, tt.header)
			if !tt.wantMissing && !tt.wantJob {
				if err != nil {
					t.Fatalf("validateJobResult() error = %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("validateJobResult() error = nil")
			}
			if tt.wantMissing {
				var target MissingEResultError
				if !errors.As(err, &target) {
					t.Fatalf("validateJobResult() error = %v, want MissingEResultError", err)
				}
			}
			if tt.wantJob {
				var target *JobError
				if !errors.As(err, &target) {
					t.Fatalf("validateJobResult() error = %v, want JobError", err)
				}
			}
		})
	}
}

func TestSessionCallDispatchesByJobID(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if !serveAnonymousLogon(t, conn) {
			return
		}

		messageType, payload, err := conn.ReadMessage()
		if err != nil || messageType != websocket.BinaryMessage {
			return
		}

		requestFrame, err := DecodeFrame(payload, FrameLimits{})
		if err != nil {
			return
		}

		requestHeader := &steamHeader{}
		if err := protoUnmarshal(requestFrame.Header, requestHeader); err != nil {
			return
		}
		if requestHeader.JobidTarget == nil || requestHeader.GetJobidTarget() != NoJobID {
			t.Errorf("request jobid_target = %v, want explicit NoJobID", requestHeader.JobidTarget)
			return
		}

		responseHeader, err := proto.Marshal(&steamHeader{
			JobidTarget:   uint64Pointer(requestHeader.GetJobidSource()),
			TargetJobName: stringValue(requestHeader.GetTargetJobName()),
		})
		if err != nil {
			return
		}

		response := Frame{EMsg: 6404, Proto: true, Header: responseHeader, Body: []byte{9, 8, 7}}
		encoded, err := EncodeFrame(response, FrameLimits{})
		if err != nil {
			return
		}

		_ = conn.WriteMessage(websocket.BinaryMessage, encoded)
	}))
	defer server.Close()

	client, err := New(WithWebSocketOption(WithIOTimeout(time.Second)))
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
	defer func() { _ = session.Close() }()

	message, err := session.Call(context.Background(), JobRequest{
		EMsg:          6403,
		ExpectedEMsg:  6404,
		TargetJobName: "GameServers.GetServerList#1",
		Body:          []byte{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if message.EMsg != 6404 || string(message.Body) != string([]byte{9, 8, 7}) {
		t.Fatalf("Call() = %#v, want response", message)
	}
}

func protoUnmarshal(data []byte, message proto.Message) error {
	return proto.Unmarshal(data, message)
}

func serveAnonymousLogon(t *testing.T, conn *websocket.Conn) bool {
	t.Helper()
	messageType, payload, err := conn.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage {
		return false
	}

	frame, err := DecodeFrame(payload, FrameLimits{})
	if err != nil || frame.EMsg != EMsgClientLogon {
		return false
	}

	header := &steamHeader{}
	if err := proto.Unmarshal(frame.Header, header); err != nil {
		return false
	}

	if header.JobidSource == nil || header.GetJobidSource() != NoJobID ||
		header.JobidTarget == nil || header.GetJobidTarget() != NoJobID {
		t.Errorf("anonymous logon job IDs are not explicitly NoJobID: source=%v target=%v", header.JobidSource, header.JobidTarget)
		return false
	}

	response, err := encodeProtoMessage(EMsgClientLogOnResponse, &steamHeader{
		Steamid:         uint64Pointer(76561198000000001),
		ClientSessionid: int32Pointer(42),
	}, &steamLogonResponse{Eresult: int32Pointer(1), HeartbeatSeconds: int32Pointer(60)})
	if err != nil {
		return false
	}

	encoded, err := EncodeFrame(response, FrameLimits{})
	if err != nil {
		return false
	}

	return conn.WriteMessage(websocket.BinaryMessage, encoded) == nil
}
