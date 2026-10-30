// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"runtime"
	"testing"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func TestProtoMessageRoundTrip(t *testing.T) {
	header := &steamproto.CMsgProtoBufHeader{
		ClientSessionid: int32Value(17),
		JobidSource:     uint64Value(42),
		TargetJobName:   stringValue("GameServers.GetServerList#1"),
	}

	body := newHeartbeat()
	frame, err := encodeProtoMessage(EMsgClientHeartBeat, header, body)
	if err != nil {
		t.Fatalf("encodeProtoMessage() error = %v", err)
	}

	encoded, err := EncodeFrame(frame, FrameLimits{})
	if err != nil {
		t.Fatalf("EncodeFrame() error = %v", err)
	}

	decodedFrame, err := DecodeFrame(encoded, FrameLimits{})
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}

	gotHeader := &steamproto.CMsgProtoBufHeader{}
	gotBody := &steamproto.CMsgClientHeartBeat{}
	if err := decodeProtoMessage(decodedFrame, gotHeader, gotBody); err != nil {
		t.Fatalf("decodeProtoMessage() error = %v", err)
	}
	if gotHeader.GetClientSessionid() != 17 || gotHeader.GetJobidSource() != 42 ||
		gotHeader.GetTargetJobName() != "GameServers.GetServerList#1" || !gotBody.GetSendReply() {
		t.Fatalf("decoded message = header %#v body %#v", gotHeader, gotBody)
	}
}

func TestAnonymousLogonMessage(t *testing.T) {
	logon := newAnonymousLogon()
	encoded, err := proto.Marshal(logon)
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}

	decoded := &steamproto.CMsgClientLogon{}
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("proto.Unmarshal() error = %v", err)
	}

	if decoded.GetProtocolVersion() != anonymousLogonProtocolVersion ||
		decoded.GetClientLanguage() != "" ||
		decoded.GetClientOsType() != clientOSType() ||
		decoded.GetAnonUserTargetAccountName() != "anonymous" ||
		decoded.GetObfuscatedPrivateIp() == nil ||
		decoded.GetObfuscatedPrivateIp().GetV4() != 0 ||
		decoded.ChatMode == nil || decoded.GetChatMode() != 2 ||
		decoded.MachineName == nil || decoded.GetMachineName() != "" ||
		decoded.SupportsRateLimitResponse == nil ||
		decoded.GetSupportsRateLimitResponse() {
		t.Fatalf("anonymous logon = %v", decoded)
	}
}

func TestClientOSType(t *testing.T) {
	want := steamOSUnknown
	switch runtime.GOOS {
	case "windows":
		want = steamOSWindows10
	case "darwin":
		want = steamOSMacOSUnknown
	case "linux":
		want = steamOSLinuxUnknown
	}

	if got := clientOSType(); got != want {
		t.Fatalf("clientOSType() = %d, want %d", got, want)
	}
}

func int32Value(value int32) *int32 {
	return &value
}

func uint64Value(value uint64) *uint64 {
	return &value
}
