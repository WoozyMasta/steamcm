// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	cm "github.com/woozymasta/steamcm"
	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func TestClientQueryAndList(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if !serveGameserverLogon(t, conn) {
			return
		}

		for range 3 {
			frame, header, err := readGameserverRequest(conn)
			if err != nil {
				t.Errorf("read gameserver request: %v", err)
				return
			}

			var responseBody proto.Message
			var responseEMsg uint32
			responseEResult := int32(1)

			switch frame.EMsg {
			case cm.EMsgClientGMSServerQuery:
				request := &steamproto.CMsgClientGMSServerQuery{}
				if err := proto.Unmarshal(frame.Body, request); err != nil {
					t.Errorf("decode GMS query: %v", err)
					return
				}

				if request.GetAppId() != 730 ||
					request.GetMaxServers() != 1 ||
					request.GetFilterText() != `\appid\730` {
					t.Errorf("GMS query = %v", request)
					return
				}

				responseBody = &steamproto.CMsgGMSClientServerQueryResponse{
					Servers: []*steamproto.CMsgGMSClientServerQueryResponse_Server{{
						ServerIp:  &steamproto.CMsgIPAddress{Ip: &steamproto.CMsgIPAddress_V4{V4: 0xc000020a}},
						QueryPort: uint32Pointer(27015),
					}},
				}
				responseEMsg = cm.EMsgGMSClientServerQueryResponse

			case cm.EMsgServiceMethodCallFromClient:
				switch header.GetTargetJobName() {
				case listJobName:
					request := &steamproto.CGameServers_GetServerList_Request{}
					if err := proto.Unmarshal(frame.Body, request); err != nil {
						t.Errorf("decode list request: %v", err)
						return
					}

					if request.GetFilter() != `\appid\730` || request.GetLimit() != 1 {
						t.Errorf("list request = %v", request)
						return
					}

					responseBody = &steamproto.CGameServers_GetServerList_Response{
						Servers: []*steamproto.CGameServers_GetServerList_Response_Server{{
							Addr:     stringPointer("192.0.2.10:27015"),
							Gameport: uint32Pointer(27015),
							Appid:    uint32Pointer(730),
							Gametype: stringPointer("secure"),
						}},
					}
					responseEResult = cm.EResultFail

				case masterServerQueryJobName:
					request := &steamproto.CGameServers_MasterServerQuery_Request{}
					if err := proto.Unmarshal(frame.Body, request); err != nil {
						t.Errorf("decode master query request: %v", err)
						return
					}

					queryData := request.GetQueryData()
					if request.GetAppid() != 730 || queryData.GetAppId() != 730 ||
						queryData.GetFilterText() != `\appid\730` || queryData.GetMaxServers() != 1 {
						t.Errorf("master query request = %v", request)
						return
					}

					responseBody = &steamproto.CGameServers_MasterServerQuery_Response{
						QueryResponse: &steamproto.CMsgGMSClientServerQueryResponse{
							Servers: []*steamproto.CMsgGMSClientServerQueryResponse_Server{{
								ServerIp: &steamproto.CMsgIPAddress{
									Ip: &steamproto.CMsgIPAddress_V4{V4: 0xc000020a},
								},
								QueryPort: uint32Pointer(27015),
							}},
						},
					}

				default:
					t.Errorf("unexpected UM target %q", header.GetTargetJobName())
					return
				}
				responseEMsg = cm.EMsgServiceMethodResponse

			default:
				t.Errorf("unexpected request EMsg %d", frame.EMsg)
				return
			}

			if err := writeGameserverResponse(conn, responseEMsg, header.GetJobidSource(), responseEResult, responseBody); err != nil {
				t.Errorf("write gameserver response: %v", err)
				return
			}
		}
	}))
	defer server.Close()

	client, err := cm.New(cm.WithWebSocketOption(cm.WithIOTimeout(time.Second)))
	if err != nil {
		t.Fatalf("cm.New() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	if err := client.Connect(context.Background(), cm.Endpoint{Address: wsURL}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	session, err := client.LogonAnonymous(context.Background())
	if err != nil {
		t.Fatalf("LogonAnonymous() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	gameservers, err := New(session)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	endpoints, err := gameservers.Query(context.Background(), QueryRequest{AppID: 730, Filter: `\appid\730`, Limit: 1})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(endpoints) != 1 || endpoints[0].Addr.String() != "192.0.2.10:27015" {
		t.Fatalf("Query() = %#v", endpoints)
	}

	servers, err := gameservers.List(context.Background(), ListQuery{Filter: `\appid\730`, Limit: 1})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(servers) != 1 || servers[0].Addr.String() != "192.0.2.10:27015" || servers[0].GameType != "secure" {
		t.Fatalf("List() = %#v", servers)
	}

	endpoints, err = gameservers.MasterServerQuery(context.Background(), QueryRequest{
		AppID:  730,
		Filter: `\appid\730`,
		Limit:  1,
	})
	if err != nil {
		t.Fatalf("MasterServerQuery() error = %v", err)
	}
	if len(endpoints) != 1 || endpoints[0].Addr.String() != "192.0.2.10:27015" {
		t.Fatalf("MasterServerQuery() = %#v", endpoints)
	}
}

func serveGameserverLogon(t *testing.T, conn *websocket.Conn) bool {
	t.Helper()
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Errorf("read logon: %v", err)
		return false
	}

	frame, err := cm.DecodeFrame(payload, cm.FrameLimits{})
	if err != nil || frame.EMsg != cm.EMsgClientLogon {
		t.Errorf("logon frame = %#v, error = %v", frame, err)
		return false
	}

	header, err := proto.Marshal(&steamproto.CMsgProtoBufHeader{
		Steamid:         uint64Pointer(76561198000000001),
		ClientSessionid: int32Pointer(42),
	})
	if err != nil {
		t.Errorf("marshal logon header: %v", err)
		return false
	}

	body, err := proto.Marshal(&steamproto.CMsgClientLogonResponse{
		Eresult:          int32Pointer(1),
		HeartbeatSeconds: int32Pointer(60),
	})
	if err != nil {
		t.Errorf("marshal logon body: %v", err)
		return false
	}

	encoded, err := cm.EncodeFrame(
		cm.Frame{
			EMsg:   cm.EMsgClientLogOnResponse,
			Proto:  true,
			Header: header,
			Body:   body,
		},
		cm.FrameLimits{},
	)
	if err != nil {
		t.Errorf("encode logon response: %v", err)
		return false
	}

	return conn.WriteMessage(websocket.BinaryMessage, encoded) == nil
}

func readGameserverRequest(conn *websocket.Conn) (cm.Frame, *steamproto.CMsgProtoBufHeader, error) {
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return cm.Frame{}, nil, err
	}
	if messageType != websocket.BinaryMessage {
		return cm.Frame{}, nil, fmt.Errorf("message type %d is not binary", messageType)
	}

	frame, err := cm.DecodeFrame(payload, cm.FrameLimits{})
	if err != nil {
		return cm.Frame{}, nil, err
	}

	header := &steamproto.CMsgProtoBufHeader{}
	if err := proto.Unmarshal(frame.Header, header); err != nil {
		return cm.Frame{}, nil, err
	}

	return frame, header, nil
}

func writeGameserverResponse(conn *websocket.Conn, emsg uint32, jobID uint64, eResult int32, body proto.Message) error {
	header, err := proto.Marshal(&steamproto.CMsgProtoBufHeader{
		JobidTarget: uint64Pointer(jobID),
		Eresult:     int32Pointer(eResult),
	})
	if err != nil {
		return err
	}

	payload, err := proto.Marshal(body)
	if err != nil {
		return err
	}

	encoded, err := cm.EncodeFrame(
		cm.Frame{
			EMsg:   emsg,
			Proto:  true,
			Header: header,
			Body:   payload,
		},
		cm.FrameLimits{},
	)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, encoded)
}

func uint32Pointer(value uint32) *uint32 {
	return &value
}

func stringPointer(value string) *string {
	return &value
}
