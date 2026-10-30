// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	steamcm "github.com/woozymasta/steamcm"
	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

// masterServerQueryJobName is the Unified Messaging method used by discovery queries.
const masterServerQueryJobName = "GameServers.MasterServerQuery#1"

// MasterServerQuery requests server endpoints through
// the current Unified Messaging wrapper around ClientGMSServerQuery.
func (c *Client) MasterServerQuery(ctx context.Context, query QueryRequest) ([]Endpoint, error) {
	if c == nil || c.session == nil {
		return nil, errors.New("CM gameserver client is nil")
	}

	queryData, err := newQueryRequest(query)
	if err != nil {
		return nil, err
	}

	request := &steamproto.CGameServers_MasterServerQuery_Request{
		QueryData: queryData,
	}
	if query.AppID != 0 {
		request.Appid = uint32Value(query.AppID)
	}

	body, err := proto.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal CM master server query: %w", err)
	}

	message, err := c.session.Call(ctx, steamcm.JobRequest{
		EMsg:           steamcm.EMsgServiceMethodCallFromClient,
		ExpectedEMsg:   steamcm.EMsgServiceMethodResponse,
		TargetJobName:  masterServerQueryJobName,
		RequireEResult: true,
		Body:           body,
	})
	if err != nil {
		return nil, fmt.Errorf("execute CM master server query: %w", err)
	}

	response := &steamproto.CGameServers_MasterServerQuery_Response{}
	if err := proto.Unmarshal(message.Body, response); err != nil {
		return nil, fmt.Errorf("decode CM master server query response: %w", err)
	}

	queryResponse := response.GetQueryResponse()
	if queryResponse == nil {
		return nil, errors.New("CM master server query response is missing query response")
	}
	if queryResponse.GetError() != "" {
		return nil, &ResponseError{Message: queryResponse.GetError()}
	}

	return queryEndpoints(queryResponse.GetServers())
}
