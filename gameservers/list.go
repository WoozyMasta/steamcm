// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"google.golang.org/protobuf/proto"

	steamcm "github.com/woozymasta/steamcm"
	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

// listJobName is the Unified Messaging method used by Client.List.
const listJobName = "GameServers.GetServerList#1"

// List requests rich server records through Unified Messaging.
func (c *Client) List(ctx context.Context, query ListQuery) ([]Server, error) {
	if c == nil || c.session == nil {
		return nil, errors.New("CM gameserver client is nil")
	}

	request := &steamproto.CGameServers_GetServerList_Request{}
	if query.Filter != "" {
		request.Filter = stringValue(query.Filter)
	}
	if query.Limit != 0 {
		request.Limit = uint32Value(query.Limit)
	}

	body, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal CM gameserver list request: %w", err)
	}

	message, callErr := c.session.Call(ctx, steamcm.JobRequest{
		EMsg:           steamcm.EMsgServiceMethodCallFromClient,
		ExpectedEMsg:   steamcm.EMsgServiceMethodResponse,
		TargetJobName:  listJobName,
		RequireEResult: true,
		Body:           body,
	})
	if callErr != nil {
		var jobErr *steamcm.JobError
		// Steam can attach useful rows to EResult_FAIL; decode before deciding whether to return it.
		if !errors.As(callErr, &jobErr) || jobErr.EResult != steamcm.EResultFail {
			return nil, fmt.Errorf("execute CM gameserver list: %w", callErr)
		}
	}

	response := &steamproto.CGameServers_GetServerList_Response{}
	if err := proto.Unmarshal(message.Body, response); err != nil {
		if callErr != nil {
			return nil, fmt.Errorf("execute CM gameserver list: %w", callErr)
		}
		return nil, fmt.Errorf("decode CM gameserver list response: %w", err)
	}

	servers, err := listServers(response.GetServers())
	if err != nil {
		return nil, err
	}
	if callErr != nil && len(servers) == 0 {
		return nil, fmt.Errorf("execute CM gameserver list: %w", callErr)
	}

	return servers, nil
}

// listServers validates rich list rows and converts them to public records.
func listServers(servers []*steamproto.CGameServers_GetServerList_Response_Server) ([]Server, error) {
	result := make([]Server, 0, len(servers))
	for index, server := range servers {
		if server == nil {
			return nil, fmt.Errorf("decode CM gameserver list item %d: server is nil", index)
		}

		address, err := netip.ParseAddrPort(server.GetAddr())
		if err != nil {
			return nil, fmt.Errorf("decode CM gameserver list item %d address %q: %w", index, server.GetAddr(), err)
		}

		gamePort, err := uint16Port(server.GetGameport(), "game")
		if err != nil {
			return nil, fmt.Errorf("decode CM gameserver list item %d: %w", index, err)
		}

		spectatorPort, err := uint16Port(server.GetSpecport(), "spectator")
		if err != nil {
			return nil, fmt.Errorf("decode CM gameserver list item %d: %w", index, err)
		}

		result = append(result, Server{
			Addr:          address,
			GamePort:      gamePort,
			SpectatorPort: spectatorPort,
			SteamID:       server.GetSteamid(),
			AppID:         server.GetAppid(),
			Name:          server.GetName(),
			GameDir:       server.GetGamedir(),
			Map:           server.GetMap(),
			Product:       server.GetProduct(),
			Version:       server.GetVersion(),
			OS:            server.GetOs(),
			GameType:      server.GetGametype(),
			Region:        server.GetRegion(),
			Players:       server.GetPlayers(),
			MaxPlayers:    server.GetMaxPlayers(),
			Bots:          server.GetBots(),
			Dedicated:     server.GetDedicated(),
			Secure:        server.GetSecure(),
		})
	}

	return result, nil
}

// uint16Port validates a protobuf port before narrowing it to uint16.
func uint16Port(value uint32, name string) (uint16, error) {
	if value > 1<<16-1 {
		return 0, fmt.Errorf("invalid %s port %d", name, value)
	}

	return uint16(value), nil
}
