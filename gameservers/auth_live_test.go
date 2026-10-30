//go:build cm_auth_live

// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	cm "github.com/woozymasta/steamcm"
)

const (
	authLiveTokenEnv = "STEAMCM_AUTH_TOKEN_FILE"
	maxAuthTokenFile = 128 << 10
)

func TestLiveAuthenticatedDayZMasterServerQuery(t *testing.T) {
	tokenPath := os.Getenv(authLiveTokenEnv)
	if tokenPath == "" {
		t.Fatalf("%s must name an owner-provided token file", authLiveTokenEnv)
	}

	token, err := readLiveToken(tokenPath)
	if err != nil {
		t.Fatalf("read authenticated CM test configuration: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	directory := &cm.Directory{}
	endpoints, err := directory.Get(ctx, cm.DirectoryQuery{MaxCount: 5})
	if err != nil {
		t.Fatalf("CM directory lookup failed: %v", err)
	}
	if len(endpoints) == 0 {
		t.Fatal("CM directory returned no endpoints")
	}

	var lastErr error
	attempts := 0
	seen := make(map[string]struct{}, 3)
	for _, endpoint := range endpoints {
		if attempts >= 3 {
			break
		}

		if _, ok := seen[endpoint.Address]; ok {
			continue
		}

		seen[endpoint.Address] = struct{}{}
		attempts++
		client, err := cm.New(cm.WithWebSocketOption(cm.WithIOTimeout(15 * time.Second)))
		if err != nil {
			t.Fatal(err)
		}

		if err := client.Connect(ctx, endpoint); err != nil {
			lastErr = err
			_ = client.Close()
			continue
		}

		session, err := client.LogonToken(ctx, cm.TokenLogonDetails{
			SteamID: token.SteamID, AccountName: token.AccountName, RefreshToken: token.RefreshToken,
		})
		if err != nil {
			lastErr = err
			_ = client.Close()
			continue
		}

		gameServers, err := New(session)
		if err != nil {
			_ = session.Close()
			_ = client.Close()
			t.Fatal(err)
		}

		queryCtx, queryCancel := context.WithTimeout(ctx, 30*time.Second)
		servers, queryErr := gameServers.MasterServerQuery(queryCtx, QueryRequest{
			AppID: 221100, Filter: `\appid\221100`, Limit: 10,
		})

		queryCancel()
		_ = session.Close()
		_ = client.Close()
		if queryErr == nil && len(servers) > 0 {
			for index, server := range servers {
				if !server.Addr.IsValid() || server.Addr.Port() == 0 {
					t.Fatalf("CM returned invalid server endpoint at index %d", index)
				}
			}
			return
		}

		if queryErr == nil {
			lastErr = errors.New("authenticated DayZ query returned no servers")
		} else {
			lastErr = queryErr
		}
	}

	t.Fatalf("authenticated DayZ CM query failed after %d endpoint attempts: %v", attempts, lastErr)
}

type liveToken struct {
	SteamID      uint64 `json:"steam_id"`
	AccountName  string `json:"account_name"`
	RefreshToken string `json:"refresh_token"`
}

func readLiveToken(path string) (liveToken, error) {
	file, err := os.Open(path)
	if err != nil {
		return liveToken{}, errors.New("could not open token file")
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxAuthTokenFile {
		return liveToken{}, errors.New("token file is not a regular file within the size limit")
	}

	var token liveToken
	decoder := json.NewDecoder(io.LimitReader(file, maxAuthTokenFile+1))
	if err := decoder.Decode(&token); err != nil {
		return liveToken{}, errors.New("token file is not valid JSON")
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return liveToken{}, errors.New("token file contains trailing data")
	}

	if token.SteamID == 0 || token.AccountName == "" || token.RefreshToken == "" ||
		len(token.RefreshToken) > 64<<10 {
		return liveToken{}, errors.New("token file is missing required account fields")
	}

	return token, nil
}
