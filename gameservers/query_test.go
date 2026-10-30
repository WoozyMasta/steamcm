// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"net/netip"
	"testing"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func TestNewQueryRequest(t *testing.T) {
	request, err := newQueryRequest(QueryRequest{
		AppID:         730,
		GeoLocationIP: netip.MustParseAddr("192.0.2.10"),
		Region:        5,
		Filter:        `\\appid\\730`,
		Limit:         25,
	})
	if err != nil {
		t.Fatalf("newQueryRequest() error = %v", err)
	}
	if request.GetAppId() != 730 || request.GetGeoLocationIp() != 0xc000020a ||
		request.GetRegionCode() != 5 || request.GetFilterText() != `\\appid\\730` ||
		request.GetMaxServers() != 25 {
		t.Fatalf("newQueryRequest() = %v", request)
	}
}

func TestNewQueryRequestRejectsIPv6GeoLocation(t *testing.T) {
	_, err := newQueryRequest(QueryRequest{
		GeoLocationIP: netip.MustParseAddr("2001:db8::10"),
	})
	if err == nil {
		t.Fatal("newQueryRequest() error = nil")
	}
}

func TestQueryEndpoints(t *testing.T) {
	ipv6 := netip.MustParseAddr("2001:db8::20").As16()
	servers := []*steamproto.CMsgGMSClientServerQueryResponse_Server{
		{
			ServerIp: &steamproto.CMsgIPAddress{
				Ip: &steamproto.CMsgIPAddress_V4{V4: 0xc000020a},
			},
			QueryPort:   uint32Value(27015),
			AuthPlayers: uint32Value(3),
		},
		{
			ServerIp: &steamproto.CMsgIPAddress{
				Ip: &steamproto.CMsgIPAddress_V6{V6: ipv6[:]},
			},
			QueryPort: uint32Value(27016),
		},
	}

	endpoints, err := queryEndpoints(servers)
	if err != nil {
		t.Fatalf("queryEndpoints() error = %v", err)
	}
	if got, want := endpoints[0].Addr, netip.MustParseAddrPort("192.0.2.10:27015"); got != want {
		t.Fatalf("IPv4 address = %v, want %v", got, want)
	}
	if !endpoints[0].HasAuthPlayers || endpoints[0].AuthPlayers != 3 {
		t.Fatalf("auth players = %#v", endpoints[0])
	}
	if got, want := endpoints[1].Addr, netip.MustParseAddrPort("[2001:db8::20]:27016"); got != want {
		t.Fatalf("IPv6 address = %v, want %v", got, want)
	}
}

func TestQueryEndpointsRejectsMissingAddress(t *testing.T) {
	_, err := queryEndpoints([]*steamproto.CMsgGMSClientServerQueryResponse_Server{{
		QueryPort: uint32Value(27015),
	}})
	if err == nil {
		t.Fatal("queryEndpoints() error = nil")
	}
}

func TestQueryRecordsResolveStringTableAndDefaultFields(t *testing.T) {
	mapName := "default-map"
	versionIndex := uint32(2)
	gameType := "default-type"
	mapIndex := uint32(0)
	typeIndex := uint32(1)
	gameDirIndex := uint32(3)
	nameIndex := uint32(4)
	description := "DayZ server"
	spectatorName := "Spectator"

	response, err := queryRecordsFromResponse(&steamproto.CMsgGMSClientServerQueryResponse{
		Servers: []*steamproto.CMsgGMSClientServerQueryResponse_Server{{
			ServerIp: &steamproto.CMsgIPAddress{
				Ip: &steamproto.CMsgIPAddress_V4{V4: 0xc000020a},
			},
			QueryPort:          uint32Value(27015),
			MapStrindex:        &mapIndex,
			GametypeStrindex:   &typeIndex,
			GamedirStrindex:    &gameDirIndex,
			NameStrindex:       &nameIndex,
			GameDescriptionStr: &description,
			SpectatorNameStr:   &spectatorName,
			Players:            uint32Value(4),
			GamePort:           uint32Value(2302),
			MaxPlayers:         uint32Value(60),
		}},
		DefaultServerData: &steamproto.CMsgGMSClientServerQueryResponse_Server{
			MapStr:          &mapName,
			VersionStrindex: &versionIndex,
			GametypeStr:     &gameType,
		},
		ServerStrings: []string{"map-from-table", "type-from-table", "1.2.3", "dayz", "server-from-table"},
	}, true)
	if err != nil {
		t.Fatalf("queryRecordsFromResponse() error = %v", err)
	}
	if len(response) != 1 {
		t.Fatalf("records count = %d, want 1", len(response))
	}

	record := response[0]
	if record.mapName != "map-from-table" || record.version != "1.2.3" || record.gameType != "type-from-table" {
		t.Fatalf("resolved metadata = %#v", record)
	}
	if !record.mapKnown || !record.versionKnown || !record.gameTypeKnown {
		t.Fatalf("metadata known flags = %#v", record)
	}
	if record.details.GameDir != "dayz" || record.details.Name != "server-from-table" ||
		record.details.GameDescription != description || record.details.SpectatorName != spectatorName ||
		record.details.Players != 4 || record.details.GamePort != 2302 || record.details.MaxPlayers != 60 {
		t.Fatalf("decoded CM details = %#v", record.details)
	}
}
