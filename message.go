// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"errors"
	"fmt"
	"runtime"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

// CM message identifiers used by the discovery session.
const (
	EMsgMulti                        uint32 = 1
	EMsgClientHeartBeat              uint32 = 703
	EMsgClientLoggedOff              uint32 = 757
	EMsgClientLogOnResponse          uint32 = 751
	EMsgClientLogon                  uint32 = 5514
	EMsgClientGMSServerQuery         uint32 = 6403
	EMsgGMSClientServerQueryResponse uint32 = 6404
	EMsgServiceMethod                uint32 = 146
	EMsgServiceMethodResponse        uint32 = 147
	EMsgServiceMethodCallFromClient  uint32 = 151
)

const (
	anonymousLogonProtocolVersion uint32 = 65580
	anonymousSteamID              uint64 = 117093590311632896
	steamOSWindows10              uint32 = 16
	steamOSMacOSUnknown           uint32 = 0xffffff9a
	steamOSLinuxUnknown           uint32 = 0xffffff35
	steamOSUnknown                uint32 = 0xffffffff
)

// encodeProtoMessage marshals a CM protobuf header and body into a frame.
func encodeProtoMessage(emsg uint32, header *steamproto.CMsgProtoBufHeader, body proto.Message) (Frame, error) {
	if header == nil {
		return Frame{}, errors.New("CM protobuf header is nil")
	}

	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return Frame{}, fmt.Errorf("marshal CM protobuf header: %w", err)
	}

	bodyBytes, err := proto.Marshal(body)
	if err != nil {
		return Frame{}, fmt.Errorf("marshal CM protobuf body: %w", err)
	}

	return Frame{EMsg: emsg, Proto: true, Header: headerBytes, Body: bodyBytes}, nil
}

// decodeProtoMessage unmarshals both protobuf sections of a CM frame.
func decodeProtoMessage(frame Frame, header *steamproto.CMsgProtoBufHeader, body proto.Message) error {
	if !frame.Proto {
		return errors.New("CM message is not protobuf encoded")
	}
	if err := proto.Unmarshal(frame.Header, header); err != nil {
		return fmt.Errorf("unmarshal CM protobuf header: %w", err)
	}
	if err := proto.Unmarshal(frame.Body, body); err != nil {
		return fmt.Errorf("unmarshal CM protobuf body: %w", err)
	}

	return nil
}

// newAnonymousLogon builds the minimal Steam anonymous logon request.
func newAnonymousLogon() *steamproto.CMsgClientLogon {
	return &steamproto.CMsgClientLogon{
		ProtocolVersion:           uint32Value(anonymousLogonProtocolVersion),
		ClientLanguage:            stringValue(""),
		ClientOsType:              uint32Value(clientOSType()),
		ObfuscatedPrivateIp:       &steamproto.CMsgIPAddress{Ip: &steamproto.CMsgIPAddress_V4{V4: 0}},
		ChatMode:                  uint32Value(2),
		AnonUserTargetAccountName: stringValue("anonymous"),
		MachineName:               stringValue(""),
		SupportsRateLimitResponse: boolValue(false),
	}
}

// clientOSType maps the Go runtime platform to Steam's OS code.
func clientOSType() uint32 {
	switch runtime.GOOS {
	case "windows":
		return steamOSWindows10

	case "darwin":
		return steamOSMacOSUnknown

	case "linux":
		return steamOSLinuxUnknown

	default:
		return steamOSUnknown
	}
}

// newHeartbeat builds a heartbeat that asks CM to send its response.
func newHeartbeat() *steamproto.CMsgClientHeartBeat {
	return &steamproto.CMsgClientHeartBeat{SendReply: boolValue(true)}
}

// uint32Value returns a pointer so protobuf can distinguish set from unset.
func uint32Value(value uint32) *uint32 {
	return &value
}

// stringValue returns a pointer so protobuf can distinguish set from unset.
func stringValue(value string) *string {
	return &value
}

// boolValue returns a pointer so protobuf can distinguish set from unset.
func boolValue(value bool) *bool {
	return &value
}

// int32Pointer returns a pointer for optional protobuf scalar fields.
func int32Pointer(value int32) *int32 {
	return &value
}

// uint64Pointer returns a pointer for optional protobuf scalar fields.
func uint64Pointer(value uint64) *uint64 {
	return &value
}
