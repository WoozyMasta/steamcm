// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryPartitionsCappedPagesAndBuildsReusablePlan(t *testing.T) {
	servers := make([]queryRecord, 0, 16)
	for mapIndex, mapName := range []string{"map-a", "map-b", "map-c", "map-d"} {
		for serverIndex := range 4 {
			gameType := "tag-a"
			if serverIndex%2 != 0 {
				gameType = "tag-b"
			}
			address := netip.AddrPortFrom(
				netip.AddrFrom4([4]byte{192, 0, 2, byte(mapIndex*4 + serverIndex + 1)}),
				uint16(27015+mapIndex),
			)
			servers = append(servers, queryRecord{
				endpoint: Endpoint{Addr: address}, mapName: mapName, version: "1.0",
				gameType: gameType, mapKnown: true, versionKnown: true, gameTypeKnown: true,
			})
		}
	}

	queryFn := func(_ context.Context, query QueryRequest) ([]queryRecord, error) {
		conditions, excluded, err := parseTestDiscoveryFilter(query.Filter)
		if err != nil {
			return nil, err
		}

		matched := make([]queryRecord, 0, query.Limit)
		for _, server := range servers {
			if !matchesTestDiscoveryFilter(server, conditions, excluded) {
				continue
			}
			matched = append(matched, server)
		}
		if len(matched) > int(query.Limit) {
			matched = matched[:query.Limit]
		}

		return matched, nil
	}

	var events []DiscoveryEvent
	root := QueryRequest{AppID: 221100, Filter: `\appid\221100`}
	run := discoveryRun{
		ctx: context.Background(), root: root, limit: 8, maxCalls: 24, maxDepth: 16,
		indices: make(map[netip.AddrPort]int), queryFn: queryFn,
		onEvent: func(event DiscoveryEvent) error {
			events = append(events, event)
			return nil
		},
	}

	root.Limit = run.limit
	page, err := run.request(root)
	if err != nil {
		t.Fatal(err)
	}

	walk := run.walk(root, discoveryPath{}, page, 0)
	if walk.err != nil {
		t.Fatalf("walk() error = %v", walk.err)
	}
	if walk.unresolved != 0 || !walk.drained {
		t.Fatalf("walk status = unresolved %d, drained %t", walk.unresolved, walk.drained)
	}
	if got, want := len(run.items), len(servers); got != want {
		t.Fatalf("unique endpoints = %d, want %d", got, want)
	}
	if len(walk.queries) < 2 || len(walk.queries) > maxDiscoveryPlanQueries {
		t.Fatalf("terminal query count = %d", len(walk.queries))
	}
	if len(events) == 0 || events[0].Type != "request" ||
		events[0].ReturnedRows == nil || *events[0].ReturnedRows != len(page) {
		t.Fatalf("initial diagnostic event = %#v", events[:min(1, len(events))])
	}

	var splitAttempt, splitResult, leaf bool
	for _, event := range events {
		switch event.Type {
		case "split_attempt":
			splitAttempt = true
		case "split_membership":
			splitResult = true
		case "leaf":
			leaf = true
		}
	}
	if !splitAttempt || !splitResult || !leaf {
		t.Fatalf("diagnostics omitted split or leaf events: %#v", events)
	}

	plan := DiscoveryPlan{Root: root, Queries: walk.queries, Limit: run.limit}
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan.Validate() error = %v", err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan) error = %v", err)
	}
	var decoded DiscoveryPlan
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(plan) error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded plan validation error = %v", err)
	}
}

func TestDiscoverySplitSkipsKeysAlreadyUsedInPath(t *testing.T) {
	path := discoveryPath{excluded: []discoveryPredicate{{key: "gametype", value: "tag-a"}}}
	if !pathHasKey(path, "gametype") {
		t.Fatal("pathHasKey() did not include excluded predicates")
	}
	records := []queryRecord{
		{
			mapName:       "map-a",
			version:       "1",
			gameType:      "tag-a,tag-b",
			mapKnown:      true,
			versionKnown:  true,
			gameTypeKnown: true,
		},
		{
			mapName:       "map-a",
			version:       "1",
			gameType:      "tag-b",
			mapKnown:      true,
			versionKnown:  true,
			gameTypeKnown: true,
		},
		{
			mapName:       "map-b",
			version:       "2",
			gameType:      "tag-a",
			mapKnown:      true,
			versionKnown:  true,
			gameTypeKnown: true,
		},
		{
			mapName:       "map-b",
			version:       "2",
			gameType:      "tag-b",
			mapKnown:      true,
			versionKnown:  true,
			gameTypeKnown: true,
		},
	}
	predicate, ok := chooseCMDiscoverySplit(records, path, `\appid\221100`)
	if !ok || predicate.key == "gametype" {
		t.Fatalf("chooseCMDiscoverySplit() = %#v, %t; must avoid used gametype", predicate, ok)
	}
}

func TestDiscoveryUsesAdditionalGameTypeTagsOnlyAfterMapFilter(t *testing.T) {
	records := []queryRecord{
		{
			mapName:       "map-a",
			gameType:      "shard,tag-a",
			mapKnown:      true,
			gameTypeKnown: true,
		},
		{
			mapName:       "map-a",
			gameType:      "shard,tag-a",
			mapKnown:      true,
			gameTypeKnown: true,
		},
		{
			mapName:       "map-a",
			gameType:      "shard,tag-b",
			mapKnown:      true,
			gameTypeKnown: true,
		},
		{
			mapName:       "map-a",
			gameType:      "shard,tag-b",
			mapKnown:      true,
			gameTypeKnown: true,
		},
	}
	rootPath := discoveryPath{}
	if predicate, ok := chooseCMDiscoverySplit(records, rootPath, `\appid\221100`); ok {
		t.Fatalf("root split = %#v; gametype must not be used without a map predicate", predicate)
	}

	mapPath := discoveryPath{
		positive: []discoveryPredicate{{key: "map", value: "map-a"}},
	}
	if predicate, ok := chooseCMDiscoverySplit(records, mapPath, `\appid\221100`); !ok || predicate.key != "gametype" {
		t.Fatalf("map-scoped split = %#v, %t; want gametype", predicate, ok)
	}

	shardPath := discoveryPath{
		positive: []discoveryPredicate{
			{key: "map", value: "map-a"},
			{key: "gametype", value: "shard"},
		},
	}
	predicate, ok := chooseCMDiscoverySplit(records, shardPath, `\appid\221100`)
	if !ok || predicate.key != "gametype" || predicate.value == "shard" {
		t.Fatalf("repeated game-type split = %#v, %t; want a different tag", predicate, ok)
	}
}

func TestValidCMSplitRejectsEmptyBranches(t *testing.T) {
	if valid, reason := validCMSplit(
		nil,
		[]queryRecord{{}},
		discoveryPredicate{},
	); valid || reason != "positive_result_empty" {
		t.Fatalf("empty positive split = %t, %q", valid, reason)
	}
	if valid, reason := validCMSplit(
		[]queryRecord{{}},
		nil,
		discoveryPredicate{},
	); valid || reason != "negative_result_empty" {
		t.Fatalf("empty negative split = %t, %q", valid, reason)
	}
}

func TestDiscoveryStopsAfterEmptyResponseFollowingData(t *testing.T) {
	servers := make([]queryRecord, 0, 10)
	for index := range 10 {
		mapName := "map-a"
		if index >= 5 {
			mapName = "map-b"
		}
		servers = append(servers, queryRecord{
			endpoint: Endpoint{Addr: netip.AddrPortFrom(
				netip.AddrFrom4([4]byte{192, 0, 2, byte(index + 1)}), 27015,
			)},
			mapName: mapName, mapKnown: true,
		})
	}

	requests := 0
	queryFn := func(_ context.Context, query QueryRequest) ([]queryRecord, error) {
		requests++
		if query.Filter == `\appid\221100\map\map-a` {
			return nil, nil
		}

		conditions, excluded, err := parseTestDiscoveryFilter(query.Filter)
		if err != nil {
			return nil, err
		}

		var matched []queryRecord
		for _, server := range servers {
			if matchesTestDiscoveryFilter(server, conditions, excluded) {
				matched = append(matched, server)
			}
		}

		return matched, nil
	}

	var events []DiscoveryEvent
	run := discoveryRun{
		ctx: context.Background(), root: QueryRequest{AppID: 221100, Filter: `\appid\221100`},
		limit: 10, maxCalls: 8, maxDepth: 4,
		indices: make(map[netip.AddrPort]int), queryFn: queryFn, successfulResponses: 1,
		onEvent: func(event DiscoveryEvent) error {
			events = append(events, event)
			return nil
		},
	}

	walk := run.walk(run.root, discoveryPath{}, servers, 0)
	if walk.err != nil {
		t.Fatalf("walk() error = %v", walk.err)
	}
	if walk.unresolved != 1 || walk.drained {
		t.Fatalf("walk status = unresolved %d, drained %t", walk.unresolved, walk.drained)
	}
	if requests != 1 || run.calls != 1 {
		t.Fatalf("requests = %d, run calls = %d; want one empty branch request", requests, run.calls)
	}
	var retries, ambiguousEmpty int
	for _, event := range events {
		if event.Type == "split_retry" {
			retries++
		}
		if event.Type == "request" && event.Reason == "empty_response_after_nonempty_response" {
			ambiguousEmpty++
		}
	}
	if retries != 0 || ambiguousEmpty != 1 {
		t.Fatalf("retries/ambiguous-empty events = %d/%d, want 0/1", retries, ambiguousEmpty)
	}
}

func TestDiscoveryEventRetainsZeroRowCounts(t *testing.T) {
	event := DiscoveryEvent{Type: "request", ReturnedRows: intPointer(0)}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal(event) error = %v", err)
	}
	if !strings.Contains(string(encoded), `"returned_rows":0`) {
		t.Fatalf("zero row count was omitted: %s", encoded)
	}
}

func TestDiscoveryRequestIntervalSpacesCalls(t *testing.T) {
	var started []time.Time
	run := discoveryRun{
		ctx: context.Background(), limit: 1, requestInterval: 20 * time.Millisecond,
		queryFn: func(context.Context, QueryRequest) ([]queryRecord, error) {
			started = append(started, time.Now())
			return nil, nil
		},
	}

	for range 2 {
		if _, err := run.request(QueryRequest{}); err != nil {
			t.Fatalf("request() error = %v", err)
		}
	}
	if len(started) != 2 {
		t.Fatalf("request starts = %d, want 2", len(started))
	}
	if elapsed := started[1].Sub(started[0]); elapsed < run.requestInterval {
		t.Fatalf("request interval = %s, want at least %s", elapsed, run.requestInterval)
	}
}

func TestDiscoveryZeroPacingDoesNotWait(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	run := discoveryRun{
		ctx: canceled, lastRequestStart: time.Now(), calls: 1,
	}
	if err := run.waitForRequestSlot(); err != nil {
		t.Fatalf("waitForRequestSlot() with zero-value pacing = %v, want nil", err)
	}
}

func TestDiscoveryInitialBurstSkipsPacingThenSpacesRequests(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	run := discoveryRun{
		ctx: canceled, initialBurst: 3, requestInterval: time.Hour,
		lastRequestStart: time.Now(),
	}
	run.calls = 2
	if err := run.waitForRequestSlot(); err != nil {
		t.Fatalf("waitForRequestSlot() delayed request within initial burst: %v", err)
	}

	run.calls = 3
	if err := run.waitForRequestSlot(); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForRequestSlot() after initial burst error = %v, want context.Canceled", err)
	}
}

func TestNormalizeDiscoveryBurst(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   int
		budget  int
		want    int
		wantErr bool
	}{
		{name: "zero disables burst", budget: 10, want: 0},
		{name: "configured", value: 1, budget: 10, want: 1},
		{name: "clamped to budget", value: 3, budget: 2, want: 2},
		{name: "negative", value: -1, budget: 10, wantErr: true},
		{name: "exceeds maximum", value: maxDiscoveryRequests + 1, budget: 10, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeDiscoveryBurst(test.value, test.budget)
			if (err != nil) != test.wantErr {
				t.Fatalf("normalizeDiscoveryBurst() error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("normalizeDiscoveryBurst() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestDiscoveryNearLimitMargin(t *testing.T) {
	for _, test := range []struct {
		count int
		limit int
		want  bool
	}{
		{count: 10000, limit: 10000, want: true},
		{count: 9990, limit: 10000, want: true},
		{count: 9989, limit: 10000},
		{count: 99, limit: 100, want: true},
		{count: 98, limit: 100},
	} {
		if got := discoveryNearLimit(test.count, test.limit); got != test.want {
			t.Errorf("discoveryNearLimit(%d, %d) = %t, want %t", test.count, test.limit, got, test.want)
		}
	}
}

func TestDiscoveryAcceptsPositiveRootFilter(t *testing.T) {
	query, err := normalizeDiscoveryRoot(QueryRequest{
		AppID: 221100, Filter: `appid\221100\map\sakhal`,
	})
	if err != nil {
		t.Fatalf("normalizeDiscoveryRoot() error = %v", err)
	}
	if query.Filter != `\appid\221100\map\sakhal` {
		t.Fatalf("normalized filter = %q", query.Filter)
	}
}

func TestDiscoveryRejectsInvalidRootFilters(t *testing.T) {
	for _, filter := range []string{
		`\appid\10\map\sakhal`,
		`\appid\221100\appid\221100`,
		`\appid\221100\nor\1\map\sakhal`,
		`\appid\221100\map`,
	} {
		_, err := normalizeDiscoveryRoot(QueryRequest{AppID: 221100, Filter: filter})
		if !errors.Is(err, ErrUnsupportedDiscoveryFilter) {
			t.Errorf("normalizeDiscoveryRoot(%q) error = %v", filter, err)
		}
	}
}

func TestDiscoveryRequestRetainsServerRecordsWithoutDiagnostics(t *testing.T) {
	address := netip.MustParseAddrPort("192.0.2.10:27015")
	want := DiscoveryServer{Addr: address.String(), Name: "sample", Map: "map-a"}
	var retained []DiscoveryServer
	run := discoveryRun{
		ctx:      context.Background(),
		limit:    10,
		maxCalls: 1,
		indices:  make(map[netip.AddrPort]int),
		onServers: func(records []queryRecord) {
			for _, record := range records {
				if record.details != nil {
					retained = append(retained, *record.details)
				}
			}
		},
		queryFn: func(context.Context, QueryRequest) ([]queryRecord, error) {
			return []queryRecord{{
				endpoint: Endpoint{Addr: address}, details: &want,
			}}, nil
		},
	}
	if _, err := run.request(QueryRequest{}); err != nil {
		t.Fatalf("request() error = %v", err)
	}
	if len(retained) != 1 || retained[0] != want {
		t.Fatalf("retained records = %#v, want %#v", retained, []DiscoveryServer{want})
	}
}

func TestDiscoveryRequestRetainsRecordsAndEmitsDiagnostics(t *testing.T) {
	address := netip.MustParseAddrPort("192.0.2.12:27015")
	want := DiscoveryServer{Addr: address.String(), Name: "sample"}
	var retained []DiscoveryServer
	var event DiscoveryEvent
	run := discoveryRun{
		ctx:      context.Background(),
		limit:    10,
		maxCalls: 1,
		indices:  make(map[netip.AddrPort]int),
		onServers: func(records []queryRecord) {
			retained = append(retained, *records[0].details)
		},
		onEvent: func(value DiscoveryEvent) error {
			event = value
			return nil
		},
		queryFn: func(context.Context, QueryRequest) ([]queryRecord, error) {
			return []queryRecord{{
				endpoint: Endpoint{Addr: address}, details: &want,
			}}, nil
		},
	}
	if _, err := run.request(QueryRequest{}); err != nil {
		t.Fatalf("request() error = %v", err)
	}
	if len(retained) != 1 || retained[0] != want {
		t.Fatalf("retained records = %#v, want %#v", retained, []DiscoveryServer{want})
	}
	if len(event.Servers) != 1 || event.Servers[0] != want {
		t.Fatalf("diagnostic servers = %#v, want %#v", event.Servers, []DiscoveryServer{want})
	}
}

func TestDiscoveryServerAccumulatorKeepsLatestRecordPerAddress(t *testing.T) {
	first := DiscoveryServer{Addr: "192.0.2.10:27015", Name: "old"}
	updated := DiscoveryServer{Addr: first.Addr, Name: "new"}
	second := DiscoveryServer{Addr: "192.0.2.11:27015", Name: "other"}
	accumulator := discoveryServerAccumulator{}
	accumulator.merge([]queryRecord{{details: &first}, {details: &second}})
	accumulator.merge([]queryRecord{{details: &updated}})

	if len(accumulator.servers) != 2 {
		t.Fatalf("retained records = %d, want 2", len(accumulator.servers))
	}
	if accumulator.servers[0] != updated || accumulator.servers[1] != second {
		t.Fatalf("retained records = %#v", accumulator.servers)
	}
}

func TestDiscoveryOptionsRejectExcessiveLimits(t *testing.T) {
	if _, err := normalizeDiscoveryRoot(QueryRequest{}); !errors.Is(err, ErrInvalidDiscoveryOptions) {
		t.Fatalf("normalizeDiscoveryRoot() error = %v, want ErrInvalidDiscoveryOptions", err)
	}
	if _, _, err := normalizeDiscoveryOptions(DiscoveryOptions{
		MaxRequests: maxDiscoveryRequests + 1,
	}); !errors.Is(err, ErrInvalidDiscoveryOptions) {
		t.Fatalf("normalizeDiscoveryOptions() error = %v, want ErrInvalidDiscoveryOptions", err)
	}
	if _, _, err := normalizeDiscoveryOptions(DiscoveryOptions{
		MinRequestInterval: -time.Second,
	}); !errors.Is(err, ErrInvalidDiscoveryOptions) {
		t.Fatalf("negative request interval error = %v, want ErrInvalidDiscoveryOptions", err)
	}
}

func parseTestDiscoveryFilter(filter string) ([]discoveryPredicate, []discoveryPredicate, error) {
	parts := strings.Split(strings.Trim(filter, `\`), `\`)
	var conditions, excluded []discoveryPredicate
	for index := 0; index < len(parts); {
		if parts[index] == "nor" {
			if index+1 >= len(parts) {
				return nil, nil, errors.New("missing NOR count")
			}

			count, err := strconv.Atoi(parts[index+1])
			if err != nil {
				return nil, nil, err
			}

			index += 2
			for range count {
				if index+1 >= len(parts) {
					return nil, nil, errors.New("truncated NOR group")
				}

				excluded = append(excluded, discoveryPredicate{key: parts[index], value: parts[index+1]})
				index += 2
			}
			continue
		}

		if index+1 >= len(parts) {
			return nil, nil, errors.New("truncated filter pair")
		}
		conditions = append(conditions, discoveryPredicate{key: parts[index], value: parts[index+1]})
		index += 2
	}

	return conditions, excluded, nil
}

func matchesTestDiscoveryFilter(
	server queryRecord,
	conditions, excluded []discoveryPredicate,
) bool {
	for _, predicate := range conditions {
		matched, known := recordMatches(server, predicate)
		if predicate.key == "appid" {
			continue
		}

		if !known || !matched {
			return false
		}
	}

	for _, predicate := range excluded {
		matched, known := recordMatches(server, predicate)
		if !known || matched {
			return false
		}
	}

	return true
}
