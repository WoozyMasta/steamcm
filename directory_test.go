// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDirectoryGet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("cellid") != "42" ||
			query.Get("cmtype") != "websockets" ||
			query.Get("realm") != "steamglobal" ||
			query.Get("maxcount") != "3" ||
			query.Get("qoslevel") != "2" ||
			query.Get("format") != "json" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "response": {
    "serverlist": [{
      "endpoint": "cmp-fra.steamserver.net:27019",
      "type": "websockets",
      "dc": "fra1",
      "realm": "steamglobal",
      "load": 53,
      "wtd_load": 125.5
    }],
    "success": true,
    "message": ""
  }
}`))
	}))
	defer server.Close()

	directory := Directory{BaseURL: server.URL}
	got, err := directory.Get(context.Background(), DirectoryQuery{
		CellID:   42,
		CMType:   "websockets",
		Realm:    "steamglobal",
		MaxCount: 3,
		QoSLevel: 2,
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	want := []Endpoint{{
		Address:      "cmp-fra.steamserver.net:27019",
		Type:         "websockets",
		DataCenter:   "fra1",
		Realm:        "steamglobal",
		Load:         53,
		WeightedLoad: 125.5,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get() = %#v, want %#v", got, want)
	}
}

func TestDirectoryGetDefaultsToWebSockets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("cellid"); got != "0" {
			t.Errorf("cellid = %q, want 0", got)
		}
		if got := r.URL.Query().Get("cmtype"); got != "websockets" {
			t.Errorf("cmtype = %q, want websockets", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "response": {
    "serverlist": [{
      "endpoint": "cmp-fra.steamserver.net:27019",
      "type": "websockets"
    }],
    "success": true
  }
}`))
	}))
	defer server.Close()

	directory := Directory{BaseURL: server.URL}
	if _, err := directory.Get(context.Background(), DirectoryQuery{}); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestDirectoryErrors(t *testing.T) {
	tests := []struct {
		name     string
		code     int
		body     string
		wantHTTP bool
	}{
		{
			name:     "status",
			code:     http.StatusBadGateway,
			body:     "upstream",
			wantHTTP: true,
		},
		{
			name:     "malformed",
			code:     http.StatusOK,
			body:     `{`,
			wantHTTP: false,
		},
		{
			name:     "api error",
			code:     http.StatusOK,
			body:     `{"response":{"success":false,"message":"unavailable"}}`,
			wantHTTP: false,
		},
		{
			name:     "invalid endpoint",
			code:     http.StatusOK,
			body:     `{"response":{"success":true,"serverlist":[{"endpoint":"invalid"}]}}`,
			wantHTTP: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			directory := Directory{BaseURL: server.URL}
			_, err := directory.Get(context.Background(), DirectoryQuery{})
			if err == nil {
				t.Fatal("Get() error = nil, want error")
			}

			var httpErr *HTTPError
			if errors.As(err, &httpErr) != tt.wantHTTP {
				t.Fatalf(
					"HTTPError presence = %t, want %t; error = %v",
					errors.As(err, &httpErr), tt.wantHTTP, err)
			}
		})
	}
}
