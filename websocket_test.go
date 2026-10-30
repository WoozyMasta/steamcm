// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebSocketConnRoundTrip(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cmsocket/" {
			http.NotFound(w, r)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		messageType, payload, err := conn.ReadMessage()
		if err != nil || messageType != websocket.BinaryMessage {
			return
		}

		frame, err := DecodeFrame(payload, FrameLimits{})
		if err != nil {
			return
		}

		frame.Body = append(frame.Body, 0xff)
		encoded, err := EncodeFrame(frame, FrameLimits{})
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, encoded)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, err := Dial(context.Background(), wsURL, WithIOTimeout(time.Second))
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	want := Frame{EMsg: 6403, Proto: true, Header: []byte{1, 2}, Body: []byte{3}}
	if err := client.WriteFrame(context.Background(), want, FrameLimits{}); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}

	got, err := client.ReadFrame(context.Background(), FrameLimits{})
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if got.EMsg != want.EMsg || !got.Proto || string(got.Body) != string([]byte{3, 0xff}) {
		t.Fatalf("ReadFrame() = %#v, want response body", got)
	}
}

func TestWebSocketURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "cmp-fra.steamserver.net:27019", want: "wss://cmp-fra.steamserver.net:27019/cmsocket/"},
		{input: "wss://example.test:443", want: "wss://example.test:443/cmsocket/"},
		{input: "ws://example.test/cmsocket/", want: "ws://example.test/cmsocket/"},
	}
	for _, tt := range tests {
		got, err := websocketURL(tt.input)
		if err != nil {
			t.Fatalf("websocketURL(%q) error = %v", tt.input, err)
		}
		if got != tt.want {
			t.Fatalf("websocketURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
		if _, err := url.Parse(got); err != nil {
			t.Fatalf("websocketURL(%q) produced invalid URL: %v", tt.input, err)
		}
	}
}
