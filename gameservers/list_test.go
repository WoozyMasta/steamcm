// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"net/netip"
	"testing"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func TestListServers(t *testing.T) {
	servers, err := listServers([]*steamproto.CGameServers_GetServerList_Response_Server{{
		Addr:       stringValue("192.0.2.10:27015"),
		Gameport:   uint32Value(27015),
		Specport:   uint32Value(27020),
		Steamid:    uint64Pointer(76561198000000001),
		Appid:      uint32Value(730),
		Name:       stringValue("Test server"),
		Gamedir:    stringValue("csgo"),
		Version:    stringValue("1.0"),
		Product:    stringValue("csgo"),
		Region:     int32Pointer(5),
		Players:    int32Pointer(2),
		MaxPlayers: int32Pointer(16),
		Bots:       int32Pointer(1),
		Map:        stringValue("de_dust2"),
		Secure:     boolPointer(true),
		Dedicated:  boolPointer(true),
		Os:         stringValue("l"),
		Gametype:   stringValue("valve_ds,secure"),
	}})
	if err != nil {
		t.Fatalf("listServers() error = %v", err)
	}

	want := Server{
		Addr:          netip.MustParseAddrPort("192.0.2.10:27015"),
		GamePort:      27015,
		SpectatorPort: 27020,
		SteamID:       76561198000000001,
		AppID:         730,
		Name:          "Test server",
		GameDir:       "csgo",
		Version:       "1.0",
		Product:       "csgo",
		Region:        5,
		Players:       2,
		MaxPlayers:    16,
		Bots:          1,
		Map:           "de_dust2",
		Secure:        true,
		Dedicated:     true,
		OS:            "l",
		GameType:      "valve_ds,secure",
	}
	if len(servers) != 1 || servers[0] != want {
		t.Fatalf("listServers() = %#v, want %#v", servers, []Server{want})
	}
}

func TestListServersRejectsInvalidPort(t *testing.T) {
	_, err := listServers([]*steamproto.CGameServers_GetServerList_Response_Server{{
		Addr:     stringValue("192.0.2.10:27015"),
		Gameport: uint32Value(1 << 16),
	}})
	if err == nil {
		t.Fatal("listServers() error = nil")
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func int32Pointer(value int32) *int32 {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}
