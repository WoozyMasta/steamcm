// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"google.golang.org/protobuf/proto"

	steamcm "github.com/woozymasta/steamcm"
	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

// QueryRequest contains the parameters accepted by the CM gameserver query methods.
type QueryRequest struct {
	GeoLocationIP netip.Addr `json:"geo_location_ip,omitzero" yaml:"geo_location_ip,omitempty"`
	Filter        string     `json:"filter,omitempty"         yaml:"filter,omitempty"`
	AppID         uint32     `json:"appid,omitempty"          yaml:"appid,omitempty"`
	Region        uint32     `json:"region,omitempty"         yaml:"region,omitempty"`
	Limit         uint32     `json:"limit,omitempty"          yaml:"limit,omitempty"`
}

// queryRecord keeps the endpoint and planner metadata from one CM response row.
type queryRecord struct {
	details       *DiscoveryServer
	mapName       string
	version       string
	gameType      string
	endpoint      Endpoint
	mapKnown      bool
	versionKnown  bool
	gameTypeKnown bool
}

// Query requests server endpoints through ClientGMSServerQuery.
func (c *Client) Query(ctx context.Context, query QueryRequest) ([]Endpoint, error) {
	response, err := c.queryResponse(ctx, query)
	if err != nil {
		return nil, err
	}

	return queryEndpoints(response.GetServers())
}

// queryRecords retains only metadata needed to build and verify discovery splits.
func (c *Client) queryRecords(
	ctx context.Context,
	query QueryRequest,
	includeDetails bool,
) ([]queryRecord, error) {
	response, err := c.queryResponse(ctx, query)
	if err != nil {
		return nil, err
	}

	return queryRecordsFromResponse(response, includeDetails)
}

// queryResponse executes and decodes one CM gameserver query response.
func (c *Client) queryResponse(
	ctx context.Context,
	query QueryRequest,
) (*steamproto.CMsgGMSClientServerQueryResponse, error) {
	if c == nil || c.session == nil {
		return nil, errors.New("CM gameserver client is nil")
	}

	request, err := newQueryRequest(query)
	if err != nil {
		return nil, err
	}

	body, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal CM gameserver query: %w", err)
	}

	message, err := c.session.Call(ctx, steamcm.JobRequest{
		EMsg:         steamcm.EMsgClientGMSServerQuery,
		ExpectedEMsg: steamcm.EMsgGMSClientServerQueryResponse,
		Body:         body,
	})
	if err != nil {
		return nil, fmt.Errorf("execute CM gameserver query: %w", err)
	}

	response := &steamproto.CMsgGMSClientServerQueryResponse{}
	if err := proto.Unmarshal(message.Body, response); err != nil {
		return nil, fmt.Errorf("decode CM gameserver query response: %w", err)
	}
	if response.GetError() != "" {
		return nil, &ResponseError{Message: response.GetError()}
	}

	return response, nil
}

// newQueryRequest maps public query options to the CM protobuf request.
func newQueryRequest(query QueryRequest) (*steamproto.CMsgClientGMSServerQuery, error) {
	request := &steamproto.CMsgClientGMSServerQuery{}
	if query.AppID != 0 {
		request.AppId = uint32Value(query.AppID)
	}
	if query.Region != 0 {
		request.RegionCode = uint32Value(query.Region)
	}
	if query.Filter != "" {
		request.FilterText = stringValue(query.Filter)
	}
	if query.Limit != 0 {
		request.MaxServers = uint32Value(query.Limit)
	}

	if query.GeoLocationIP.IsValid() {
		// The CM protobuf field encodes IPv4 as a network-order uint32 only.
		if !query.GeoLocationIP.Is4() {
			return nil, errors.New("CM gameserver geo location IP must be IPv4")
		}

		address := query.GeoLocationIP.As4()
		request.GeoLocationIp = uint32Value(binary.BigEndian.Uint32(address[:]))
	}

	return request, nil
}

// queryEndpoints validates CM rows and projects them to endpoint contracts.
func queryEndpoints(servers []*steamproto.CMsgGMSClientServerQueryResponse_Server) ([]Endpoint, error) {
	endpoints := make([]Endpoint, 0, len(servers))
	for index, server := range servers {
		if server == nil {
			return nil, fmt.Errorf("decode CM gameserver %d: server is nil", index)
		}

		address, err := queryAddress(server)
		if err != nil {
			return nil, fmt.Errorf("decode CM gameserver %d: %w", index, err)
		}

		port := server.GetQueryPort()
		if port == 0 || port > 1<<16-1 {
			return nil, fmt.Errorf("decode CM gameserver %d: invalid query port %d", index, port)
		}

		endpoints = append(endpoints, Endpoint{
			Addr:           netip.AddrPortFrom(address, uint16(port)),
			AuthPlayers:    server.GetAuthPlayers(),
			HasAuthPlayers: server.AuthPlayers != nil,
		})
	}

	return endpoints, nil
}

// queryRecordsFromResponse resolves planner metadata and optional full diagnostics.
func queryRecordsFromResponse(
	response *steamproto.CMsgGMSClientServerQueryResponse,
	includeDetails bool,
) ([]queryRecord, error) {
	servers := response.GetServers()
	stringsTable := response.GetServerStrings()
	defaults := response.GetDefaultServerData()
	records := make([]queryRecord, 0, len(servers))
	for index, server := range servers {
		if server == nil {
			return nil, fmt.Errorf("decode CM gameserver %d: server is nil", index)
		}

		address, err := queryAddress(server)
		if err != nil {
			return nil, fmt.Errorf("decode CM gameserver %d: %w", index, err)
		}

		port := server.GetQueryPort()
		if port == 0 || port > 1<<16-1 {
			return nil, fmt.Errorf("decode CM gameserver %d: invalid query port %d", index, port)
		}

		// CM may store repeated strings in a shared table and provide defaults once.
		var defaultMap, defaultVersion, defaultGameType *string
		var defaultMapIndex, defaultVersionIndex, defaultGameTypeIndex *uint32
		if defaults != nil {
			defaultMap, defaultMapIndex = defaults.MapStr, defaults.MapStrindex
			defaultVersion, defaultVersionIndex = defaults.VersionStr, defaults.VersionStrindex
			defaultGameType, defaultGameTypeIndex = defaults.GametypeStr, defaults.GametypeStrindex
		}

		mapName, mapKnown := queryServerString(
			server.MapStr,
			server.MapStrindex,
			defaultMap,
			defaultMapIndex, stringsTable,
		)
		version, versionKnown := queryServerString(
			server.VersionStr,
			server.VersionStrindex,
			defaultVersion,
			defaultVersionIndex, stringsTable,
		)
		gameType, gameTypeKnown := queryServerString(
			server.GametypeStr,
			server.GametypeStrindex,
			defaultGameType,
			defaultGameTypeIndex, stringsTable,
		)

		endpoint := netip.AddrPortFrom(address, uint16(port))
		var details *DiscoveryServer
		if includeDetails {
			details = queryServerDetails(
				server, defaults, stringsTable, endpoint, mapName,
				version, gameType, mapKnown, versionKnown, gameTypeKnown,
			)
		}

		records = append(records, queryRecord{
			endpoint: Endpoint{
				Addr:           endpoint,
				AuthPlayers:    server.GetAuthPlayers(),
				HasAuthPlayers: server.AuthPlayers != nil,
			},
			mapName:       mapName,
			version:       version,
			gameType:      gameType,
			mapKnown:      mapKnown,
			versionKnown:  versionKnown,
			gameTypeKnown: gameTypeKnown,
			details:       details,
		})
	}

	return records, nil
}

// queryServerDetails resolves the full CM record only for opt-in diagnostics.
func queryServerDetails(
	server *steamproto.CMsgGMSClientServerQueryResponse_Server,
	defaults *steamproto.CMsgGMSClientServerQueryResponse_Server,
	stringsTable []string,
	endpoint netip.AddrPort,
	mapName, version, gameType string,
	mapKnown, versionKnown, gameTypeKnown bool,
) *DiscoveryServer {
	var defaultGameDir, defaultName, defaultDescription, defaultSpectatorName *string
	var defaultGameDirIndex, defaultNameIndex, defaultDescriptionIndex, defaultSpectatorNameIndex *uint32

	if defaults != nil {
		defaultGameDir, defaultGameDirIndex = defaults.GamedirStr, defaults.GamedirStrindex
		defaultName, defaultNameIndex = defaults.NameStr, defaults.NameStrindex
		defaultDescription, defaultDescriptionIndex = defaults.GameDescriptionStr, defaults.GameDescriptionStrindex
		defaultSpectatorName, defaultSpectatorNameIndex = defaults.SpectatorNameStr, defaults.SpectatorNameStrindex
	}

	gameDir, _ := queryServerString(
		server.GamedirStr,
		server.GamedirStrindex,
		defaultGameDir,
		defaultGameDirIndex,
		stringsTable,
	)
	name, _ := queryServerString(
		server.NameStr,
		server.NameStrindex,
		defaultName,
		defaultNameIndex,
		stringsTable,
	)
	description, _ := queryServerString(
		server.GameDescriptionStr,
		server.GameDescriptionStrindex,
		defaultDescription,
		defaultDescriptionIndex,
		stringsTable,
	)
	spectatorName, _ := queryServerString(
		server.SpectatorNameStr,
		server.SpectatorNameStrindex,
		defaultSpectatorName,
		defaultSpectatorNameIndex,
		stringsTable,
	)

	return &DiscoveryServer{
		Addr:                  endpoint.String(),
		GameDir:               gameDir,
		Map:                   mapName,
		Name:                  name,
		GameDescription:       description,
		Version:               version,
		GameType:              gameType,
		SpectatorName:         spectatorName,
		SDRPingLocation:       server.GetSdrPingLocation(),
		SteamID:               server.GetSteamId(),
		Revision:              server.GetRevision(),
		Players:               server.GetPlayers(),
		GamePort:              server.GetGamePort(),
		QueryPort:             uint32(endpoint.Port()),
		SDRPopID:              server.GetSdrPopid(),
		Flags:                 server.GetFlags(),
		AppID:                 server.GetAppId(),
		MaxPlayers:            server.GetMaxPlayers(),
		Bots:                  server.GetBots(),
		SpectatorPort:         server.GetSpectatorPort(),
		AuthPlayers:           server.GetAuthPlayers(),
		FriendsCount:          server.GetFriendsCount(),
		FriendsCountConnected: server.GetFriendsCountConnected(),
		PingEstimate:          server.GetPingEstimate(),
		MapKnown:              mapKnown,
		VersionKnown:          versionKnown,
		GameTypeKnown:         gameTypeKnown,
	}
}

// queryServerString resolves a direct value, table index, or shared default.
func queryServerString(
	value *string,
	index *uint32,
	defaultValue *string,
	defaultIndex *uint32,
	stringsTable []string,
) (string, bool) {
	if value != nil {
		return *value, true
	}

	if index != nil {
		// An explicit but invalid index is unknown; do not silently use a default.
		if uint64(*index) >= uint64(len(stringsTable)) {
			return "", false
		}

		return stringsTable[*index], true
	}

	if defaultValue != nil {
		return *defaultValue, true
	}
	if defaultIndex == nil || uint64(*defaultIndex) >= uint64(len(stringsTable)) {
		return "", false
	}

	return stringsTable[*defaultIndex], true
}

// queryAddress prefers the typed IP field and falls back to the deprecated IPv4 field.
func queryAddress(server *steamproto.CMsgGMSClientServerQueryResponse_Server) (netip.Addr, error) {
	if server.GetServerIp() != nil {
		return ipAddress(server.GetServerIp())
	}

	if server.DeprecatedServerIp != nil {
		var bytes [4]byte
		binary.BigEndian.PutUint32(bytes[:], server.GetDeprecatedServerIp())

		return netip.AddrFrom4(bytes), nil
	}

	return netip.Addr{}, errors.New("server IP is missing")
}

// ipAddress converts either protobuf IP oneof variant to netip.Addr.
func ipAddress(value *steamproto.CMsgIPAddress) (netip.Addr, error) {
	switch address := value.GetIp().(type) {
	case *steamproto.CMsgIPAddress_V4:
		var bytes [4]byte
		binary.BigEndian.PutUint32(bytes[:], address.V4)
		return netip.AddrFrom4(bytes), nil

	case *steamproto.CMsgIPAddress_V6:
		if len(address.V6) != 16 {
			return netip.Addr{}, fmt.Errorf("invalid IPv6 length %d", len(address.V6))
		}

		var bytes [16]byte
		copy(bytes[:], address.V6)
		return netip.AddrFrom16(bytes), nil

	default:
		return netip.Addr{}, errors.New("server IP is empty")
	}
}

// uint32Value sets an optional CM protobuf scalar, including a zero value.
func uint32Value(value uint32) *uint32 {
	return &value
}

// stringValue sets an optional CM protobuf string, including an empty value.
func stringValue(value string) *string {
	return &value
}
