// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"net/netip"
	"testing"

	"google.golang.org/protobuf/proto"

	steamproto "github.com/woozymasta/steamcm/internal/steamproto"
)

func BenchmarkDecodeQueryRecords10K(b *testing.B) {
	const serverCount = 10_000

	mapIndex, versionIndex, gameTypeIndex := uint32(0), uint32(1), uint32(2)
	gameDir, serverName, description := "dayz", "Test server", "DayZ server"
	response := &steamproto.CMsgGMSClientServerQueryResponse{
		Servers: make([]*steamproto.CMsgGMSClientServerQueryResponse_Server, serverCount),
		ServerStrings: []string{
			"chernarusplus", "1.29.163709", "shardABC123",
			"dayz", "Test server", "DayZ server",
		},
		DefaultServerData: &steamproto.CMsgGMSClientServerQueryResponse_Server{
			MapStrindex:        &mapIndex,
			VersionStrindex:    &versionIndex,
			GametypeStrindex:   &gameTypeIndex,
			GamedirStr:         &gameDir,
			NameStr:            &serverName,
			GameDescriptionStr: &description,
		},
	}
	for index := range response.Servers {
		response.Servers[index] = &steamproto.CMsgGMSClientServerQueryResponse_Server{
			ServerIp: &steamproto.CMsgIPAddress{
				Ip: &steamproto.CMsgIPAddress_V4{V4: uint32(0x0a000000 + index)},
			},
			QueryPort:  uint32Value(27016),
			Players:    uint32Value(uint32(index % 64)),
			MaxPlayers: uint32Value(60),
		}
	}
	wire, err := proto.Marshal(response)
	if err != nil {
		b.Fatal(err)
	}

	for _, includeDetails := range []bool{false, true} {
		name := "metadata-only"
		if includeDetails {
			name = "rich-records"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(wire)))
			b.ResetTimer()
			for range b.N {
				decoded := &steamproto.CMsgGMSClientServerQueryResponse{}
				if err := proto.Unmarshal(wire, decoded); err != nil {
					b.Fatal(err)
				}
				records, err := queryRecordsFromResponse(decoded, includeDetails)
				if err != nil || len(records) != serverCount {
					b.Fatalf("queryRecordsFromResponse() records=%d, error=%v", len(records), err)
				}
			}
		})
	}
}

func BenchmarkDiscoveryWalk10K(b *testing.B) {
	const (
		serverCount = 10_000
		queryLimit  = 1_000
	)
	mapNames := [...]string{"chernarusplus", "sakhal", "livonia", "banov"}
	versions := [...]string{"1.29", "1.28"}
	gameTypes := [...]string{"shardABC123", "etm10.000000"}
	records := make([]queryRecord, serverCount)
	for index := range records {
		address := netip.AddrFrom4([4]byte{10, 0, byte(index >> 8), byte(index)})
		records[index] = queryRecord{
			endpoint:      Endpoint{Addr: netip.AddrPortFrom(address, 27016)},
			mapName:       mapNames[index%len(mapNames)],
			version:       versions[(index/len(mapNames))%len(versions)],
			gameType:      gameTypes[(index/len(mapNames)/len(versions))%len(gameTypes)],
			mapKnown:      true,
			versionKnown:  true,
			gameTypeKnown: true,
		}
	}
	// This offline fixture includes predicate parsing and a linear data scan.
	queryFn := func(_ context.Context, query QueryRequest) ([]queryRecord, error) {
		conditions, excluded, err := parseTestDiscoveryFilter(query.Filter)
		if err != nil {
			return nil, err
		}

		matched := make([]queryRecord, 0, min(len(records), int(query.Limit)))
		for _, record := range records {
			if !matchesTestDiscoveryFilter(record, conditions, excluded) {
				continue
			}
			matched = append(matched, record)
			if len(matched) == int(query.Limit) {
				break
			}
		}

		return matched, nil
	}
	root := QueryRequest{AppID: 221100, Filter: `\appid\221100`, Limit: queryLimit}
	var requests, endpoints int

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		run := discoveryRun{
			ctx:      context.Background(),
			root:     root,
			limit:    queryLimit,
			maxCalls: 24,
			maxDepth: 16,
			indices:  make(map[netip.AddrPort]int),
			queryFn:  queryFn,
		}
		page, err := run.request(root)
		if err != nil {
			b.Fatal(err)
		}
		walk := run.walk(root, discoveryPath{}, page, 0)
		if walk.err != nil {
			b.Fatal(walk.err)
		}
		requests, endpoints = run.calls, len(run.items)
	}
	b.ReportMetric(float64(requests), "requests/op")
	b.ReportMetric(float64(endpoints), "endpoints/op")
}
