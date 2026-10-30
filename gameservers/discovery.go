// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultDiscoveryRequests is the implicit per-run CM request budget.
	defaultDiscoveryRequests = 24
	// defaultDiscoveryDepth bounds implicit recursive partition depth.
	defaultDiscoveryDepth = 16
	// maxDiscoveryRequests caps caller-selected request budgets.
	maxDiscoveryRequests = 256
	// maxDiscoveryDepth caps caller-selected recursive depth.
	maxDiscoveryDepth = 32
	// maxDiscoveryPlanQueries bounds persisted plan size.
	maxDiscoveryPlanQueries = 256
)

// discoveryPredicate is one exact key/value condition in a filter path.
type discoveryPredicate struct {
	// key is the CM filter field name.
	key string
	// value is the exact value matched or excluded for key.
	value string
}

// discoveryPath tracks positive and excluded conditions from the root query.
type discoveryPath struct {
	// positive contains predicates every record in this branch must match.
	positive []discoveryPredicate
	// excluded contains predicates every record in this branch must not match.
	excluded []discoveryPredicate
}

// discoveryWalkResult summarizes one recursive branch and its terminal queries.
type discoveryWalkResult struct {
	// err is the first request or diagnostic callback failure in this branch.
	err error
	// seen is the endpoint set returned by this branch's observed queries.
	seen map[netip.AddrPort]struct{}
	// queries are the terminal filters reusable by Collect.
	queries []QueryRequest
	// unresolved counts capped or otherwise unverified leaves.
	unresolved int
	// drained reports whether every leaf was observed below the near-cap margin.
	drained bool
}

// discoverySplitBranch contains one side of a candidate filter split.
type discoverySplitBranch struct {
	// path is the accumulated predicate set represented by query.
	path discoveryPath
	// records is the sample fetched before descending into this branch.
	records []queryRecord
	// query is the exact CM request for this side.
	query QueryRequest
}

// discoverySplit holds both queries and samples needed to validate a partition.
type discoverySplit struct {
	// predicate is the key/value condition that separates the branches.
	predicate discoveryPredicate
	// positive contains records matching predicate.
	positive discoverySplitBranch
	// negative contains records excluding predicate.
	negative discoverySplitBranch
}

// discoveryRun holds mutable state for one Discover or Collect operation.
type discoveryRun struct {
	// lastRequestStart anchors the next pacing delay.
	lastRequestStart time.Time
	// ctx bounds requests and pacing waits for the whole operation.
	ctx context.Context
	// client sends live queries when no injected query function is configured.
	client *Client
	// queryFn substitutes a deterministic source for local discovery tests.
	queryFn func(context.Context, QueryRequest) ([]queryRecord, error)
	// onEvent receives optional synchronous diagnostics.
	onEvent func(DiscoveryEvent) error
	// onServers retains decoded records when the caller requests them.
	onServers func([]queryRecord)
	// indices maps each endpoint to its position in items for in-place refresh.
	indices map[netip.AddrPort]int
	// items stores the deduplicated endpoints observed during the run.
	items []Endpoint
	// root is the normalized query shared by every generated branch.
	root QueryRequest
	// requestInterval is the minimum time between paced request starts.
	requestInterval time.Duration
	// initialBurst is the one-time unpaced request allowance for this run.
	initialBurst int
	// successfulResponses distinguishes a valid empty root result from a silent
	// empty response after this run has already observed servers.
	successfulResponses int
	// maxCalls bounds attempted requests; calls is advanced for successes and failures.
	maxCalls int
	// maxDepth limits recursive filter partitioning.
	maxDepth int
	// calls counts attempted CM requests.
	calls int
	// limit is the response cap used for every request.
	limit uint32
	// emptyAfterData stops further calls after an ambiguous empty response.
	emptyAfterData bool
}

// Discover samples a query and builds a bounded, reusable plan of CM filters.
// It partitions capped responses using map, version, and game-type metadata.
//
// Request pacing is caller-controlled:
// zero [DiscoveryOptions.MinRequestInterval] and [DiscoveryOptions.InitialBurst] add no pacing.
// For repeated polling, consider [RecommendedDiscoveryBurst]
// and 35–60 seconds between later request starts.
// These are starting points, not guaranteed service limits.
//
// CM may return changing, overlapping subsets
// for identical filters and exposes no continuation cursor,
// so Complete describes observed filter coverage, not a consistent global snapshot.
// An empty response after server data stops the run
// as ambiguous rather than treating it as an exhausted partition.
func (c *Client) Discover(
	ctx context.Context,
	query QueryRequest,
	options DiscoveryOptions,
) (DiscoveryResult, error) {
	return c.discover(ctx, query, options, nil)
}

// DiscoverServers retains the latest metadata record for each observed address.
// The explicit method opts into the additional memory required for full records.
func (c *Client) DiscoverServers(
	ctx context.Context,
	query QueryRequest,
	options DiscoveryOptions,
) (ServerDiscoveryResult, error) {
	accumulator := discoveryServerAccumulator{}
	result, err := c.discover(ctx, query, options, accumulator.merge)
	return ServerDiscoveryResult{
		Servers: accumulator.servers,
		Plan:    result.Plan,
		Status:  result.Status,
	}, err
}

// discover executes discovery and optionally retains decoded metadata records.
func (c *Client) discover(
	ctx context.Context,
	query QueryRequest,
	options DiscoveryOptions,
	onServers func([]queryRecord),
) (DiscoveryResult, error) {
	if ctx == nil {
		return DiscoveryResult{}, ErrNilDiscoveryContext
	}
	if c == nil || c.session == nil {
		return DiscoveryResult{}, errors.New("CM gameserver client is nil")
	}

	limit := query.Limit
	if limit == 0 {
		limit = DefaultDiscoveryLimit
	}
	if limit > DefaultDiscoveryLimit {
		return DiscoveryResult{}, fmt.Errorf(
			"%w: CM discovery limit must be in 1..%d", ErrInvalidDiscoveryOptions, DefaultDiscoveryLimit,
		)
	}

	maxCalls, maxDepth, err := normalizeDiscoveryOptions(options)
	if err != nil {
		return DiscoveryResult{}, err
	}

	initialBurst, err := normalizeDiscoveryBurst(options.InitialBurst, maxCalls)
	if err != nil {
		return DiscoveryResult{}, err
	}

	query.Limit = limit
	query, err = normalizeDiscoveryRoot(query)
	if err != nil {
		return DiscoveryResult{}, err
	}

	run := discoveryRun{
		ctx:             ctx,
		client:          c,
		root:            query,
		limit:           limit,
		maxCalls:        maxCalls,
		maxDepth:        maxDepth,
		requestInterval: options.MinRequestInterval,
		initialBurst:    initialBurst,
		indices:         make(map[netip.AddrPort]int),
		onEvent:         options.OnEvent,
		onServers:       onServers,
	}

	root := query
	root.Limit = limit
	rootRecords, err := run.request(root)
	if err != nil {
		return run.result(DiscoveryPlan{Root: query, Limit: limit}), err
	}

	walk := run.walk(root, discoveryPath{}, rootRecords, 0)
	plan := DiscoveryPlan{Root: query, Queries: walk.queries, Limit: limit}
	result := run.result(plan)
	result.Status.Unresolved = walk.unresolved
	result.Status.Complete = walk.unresolved == 0

	return result, walk.err
}

// Collect executes the queries in a previously created discovery plan
// and deduplicates their endpoints.
//
// Pacing is caller-controlled through options;
// zero values add no delays or initial burst.
// See [Discover] for empirical CM pacing observations.
// An ambiguous empty response stops the run.
func (c *Client) Collect(
	ctx context.Context,
	plan DiscoveryPlan,
	options CollectionOptions,
) (CollectionResult, error) {
	return c.collect(ctx, plan, options, nil)
}

// CollectServers retains the latest metadata record for each address in plan.
// The explicit method opts into the additional memory required for full records.
func (c *Client) CollectServers(
	ctx context.Context,
	plan DiscoveryPlan,
	options CollectionOptions,
) (ServerCollectionResult, error) {
	accumulator := discoveryServerAccumulator{}
	result, err := c.collect(ctx, plan, options, accumulator.merge)
	return ServerCollectionResult{Servers: accumulator.servers, Status: result.Status}, err
}

// collect executes a saved plan and optionally retains decoded metadata records.
func (c *Client) collect(
	ctx context.Context,
	plan DiscoveryPlan,
	options CollectionOptions,
	onServers func([]queryRecord),
) (CollectionResult, error) {
	if ctx == nil {
		return CollectionResult{}, ErrNilDiscoveryContext
	}
	if c == nil || c.session == nil {
		return CollectionResult{}, errors.New("CM gameserver client is nil")
	}
	if err := plan.Validate(); err != nil {
		return CollectionResult{}, err
	}
	if options.MaxRequests < 0 {
		return CollectionResult{}, fmt.Errorf("%w: negative collection request budget", ErrInvalidDiscoveryOptions)
	}
	if options.MinRequestInterval < 0 {
		return CollectionResult{}, fmt.Errorf("%w: negative minimum request interval", ErrInvalidDiscoveryOptions)
	}

	budget := len(plan.Queries)
	if options.MaxRequests > 0 {
		budget = min(budget, options.MaxRequests)
	}

	initialBurst, err := normalizeDiscoveryBurst(options.InitialBurst, budget)
	if err != nil {
		return CollectionResult{}, err
	}

	run := discoveryRun{
		ctx:                 ctx,
		client:              c,
		limit:               plan.Limit,
		maxCalls:            budget,
		indices:             make(map[netip.AddrPort]int),
		successfulResponses: 1,
		requestInterval:     options.MinRequestInterval,
		initialBurst:        initialBurst,
		onEvent:             options.OnEvent,
		onServers:           onServers,
	}

	status := DiscoveryStatus{}
	for index, query := range plan.Queries {
		if index >= budget {
			status.Unresolved += len(plan.Queries) - index
			break
		}

		query.Limit = plan.Limit
		records, err := run.request(query)
		if err != nil {
			status.Requests = run.calls
			status.Unresolved += len(plan.Queries) - index
			return CollectionResult{Endpoints: run.items, Status: status}, err
		}

		if run.emptyAfterData {
			status.Unresolved += len(plan.Queries) - index
			break
		}

		if discoveryNearLimit(len(records), int(plan.Limit)) {
			status.Unresolved++
		}
	}

	status.Requests = run.calls
	status.Complete = status.Unresolved == 0

	return CollectionResult{Endpoints: run.items, Status: status}, nil
}

// Validate checks that a saved plan can be executed without exceeding CM limits.
func (plan DiscoveryPlan) Validate() error {
	if plan.Root.AppID == 0 ||
		plan.Limit == 0 ||
		plan.Limit > DefaultDiscoveryLimit ||
		plan.Root.Limit != plan.Limit ||
		len(plan.Queries) == 0 ||
		len(plan.Queries) > maxDiscoveryPlanQueries {
		return fmt.Errorf("%w: invalid app ID, limit, or query count", ErrInvalidDiscoveryPlan)
	}

	if err := validateDiscoveryRootFilter(
		plan.Root.Filter, strconv.FormatUint(uint64(plan.Root.AppID), 10),
	); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDiscoveryPlan, err)
	}

	for index, query := range plan.Queries {
		if query.AppID != plan.Root.AppID ||
			query.Region != plan.Root.Region ||
			query.GeoLocationIP != plan.Root.GeoLocationIP ||
			query.Limit != plan.Limit ||
			!strings.HasPrefix(query.Filter, plan.Root.Filter) {
			return fmt.Errorf("%w: invalid query at index %d", ErrInvalidDiscoveryPlan, index)
		}
	}

	return nil
}

// normalizeDiscoveryOptions applies defaults and enforces request and depth bounds.
func normalizeDiscoveryOptions(options DiscoveryOptions) (int, int, error) {
	if options.MinRequestInterval < 0 {
		return 0, 0, fmt.Errorf("%w: negative minimum request interval", ErrInvalidDiscoveryOptions)
	}

	maxCalls := options.MaxRequests
	if maxCalls == 0 {
		maxCalls = defaultDiscoveryRequests
	}
	if maxCalls < 1 || maxCalls > maxDiscoveryRequests {
		return 0, 0, fmt.Errorf(
			"%w: request budget must be in 1..%d", ErrInvalidDiscoveryOptions, maxDiscoveryRequests,
		)
	}

	maxDepth := options.MaxDepth
	if maxDepth == 0 {
		maxDepth = defaultDiscoveryDepth
	}
	if maxDepth < 1 || maxDepth > maxDiscoveryDepth {
		return 0, 0, fmt.Errorf(
			"%w: depth must be in 1..%d", ErrInvalidDiscoveryOptions, maxDiscoveryDepth,
		)
	}

	return maxCalls, maxDepth, nil
}

// normalizeDiscoveryBurst validates and caps the selected burst to this run's budget.
func normalizeDiscoveryBurst(configured, budget int) (int, error) {
	if configured < 0 || configured > maxDiscoveryRequests {
		return 0, fmt.Errorf("%w: initial burst must be in 0..%d", ErrInvalidDiscoveryOptions, maxDiscoveryRequests)
	}

	return min(configured, budget), nil
}

// normalizeDiscoveryRoot supplies and validates the only supported root filter.
func normalizeDiscoveryRoot(query QueryRequest) (QueryRequest, error) {
	if query.AppID == 0 {
		return QueryRequest{}, fmt.Errorf("%w: AppID is required", ErrInvalidDiscoveryOptions)
	}

	expectedAppID := strconv.FormatUint(uint64(query.AppID), 10)
	if query.Filter == "" {
		query.Filter = `\appid\` + expectedAppID
	} else if err := validateDiscoveryRootFilter(query.Filter, expectedAppID); err != nil {
		return QueryRequest{}, err
	}

	query.Filter = `\` + strings.Trim(query.Filter, `\`)
	return query, nil
}

// validateDiscoveryRootFilter accepts a positive conjunction rooted at the request AppID.
func validateDiscoveryRootFilter(filter, expectedAppID string) error {
	parts := strings.Split(strings.Trim(filter, `\`), `\`)
	if len(parts) < 2 || len(parts)%2 != 0 || parts[0] != "appid" || parts[1] != expectedAppID {
		return fmt.Errorf("%w: root filter must start with the request AppID", ErrUnsupportedDiscoveryFilter)
	}

	for index := 0; index < len(parts); index += 2 {
		key, value := parts[index], parts[index+1]
		if key == "" || value == "" || strings.ContainsRune(value, 0) ||
			key == "nor" || key == "nand" || key == "or" {
			return fmt.Errorf("%w: discovery root must be a positive key/value conjunction", ErrUnsupportedDiscoveryFilter)
		}
		if index > 0 && key == "appid" {
			return fmt.Errorf("%w: root filter cannot repeat AppID", ErrUnsupportedDiscoveryFilter)
		}
	}

	return nil
}

// walk drains one filter branch or partitions it into validated positive/negative children.
func (run *discoveryRun) walk(query QueryRequest, path discoveryPath, records []queryRecord, depth int) discoveryWalkResult {
	seen := endpointSet(records)
	if run.emptyAfterData {
		// An empty response after observing data is ambiguous, not proof of exhaustion.
		return discoveryWalkResult{queries: []QueryRequest{query}, seen: seen, unresolved: 1}
	}

	if !discoveryNearLimit(len(records), int(run.limit)) {
		return run.leaf(query, seen, len(records), depth, "below_near_limit", true)
	}

	if depth >= run.maxDepth || run.calls+2 > run.maxCalls {
		reason := "request_budget"
		if depth >= run.maxDepth {
			reason = "max_depth"
		}
		// A split needs one request for each side, so reserve both before starting.
		return run.leaf(query, seen, len(records), depth, reason, false)
	}

	candidate, ok := chooseCMDiscoverySplit(records, path, run.root.Filter)
	if !ok {
		return run.leaf(query, seen, len(records), depth, "no_eligible_split", false)
	}

	split, valid, err := run.requestSplit(query, path, candidate, depth)
	if err != nil {
		return unresolvedDiscoveryBranch(query, seen, err)
	}
	if !valid {
		return unresolvedDiscoveryBranch(query, seen, nil)
	}

	return run.walkSplit(query, records, split, depth)
}

// leaf emits a terminal-branch diagnostic and records whether its sample is complete.
func (run *discoveryRun) leaf(
	query QueryRequest,
	seen map[netip.AddrPort]struct{},
	rows, depth int,
	reason string,
	drained bool,
) discoveryWalkResult {
	if err := run.emit(DiscoveryEvent{
		Type:         "leaf",
		Filter:       query.Filter,
		Depth:        depth,
		ReturnedRows: intPointer(rows),
		Reason:       reason,
	}); err != nil {
		return unresolvedDiscoveryBranch(query, seen, err)
	}

	if drained {
		return discoveryWalkResult{queries: []QueryRequest{query}, seen: seen, drained: true}
	}

	return unresolvedDiscoveryBranch(query, seen, nil)
}

// requestSplit queries both sides and accepts only a disjoint, membership-valid partition.
func (run *discoveryRun) requestSplit(
	parent QueryRequest,
	path discoveryPath,
	predicate discoveryPredicate,
	depth int,
) (discoverySplit, bool, error) {
	positivePath := discoveryPath{
		positive: append(append([]discoveryPredicate(nil), path.positive...), predicate),
		excluded: append([]discoveryPredicate(nil), path.excluded...),
	}
	negativePath := discoveryPath{
		positive: append([]discoveryPredicate(nil), path.positive...),
		excluded: append(append([]discoveryPredicate(nil), path.excluded...), predicate),
	}
	split := discoverySplit{
		predicate: predicate,
		positive:  discoverySplitBranch{path: positivePath, query: run.queryForPath(positivePath)},
		negative:  discoverySplitBranch{path: negativePath, query: run.queryForPath(negativePath)},
	}

	if err := run.emit(DiscoveryEvent{
		Type:           "split_attempt",
		Filter:         parent.Filter,
		Depth:          depth,
		SplitKey:       predicate.key,
		SplitValue:     predicate.value,
		PositiveFilter: split.positive.query.Filter,
		NegativeFilter: split.negative.query.Filter,
	}); err != nil {
		return split, false, err
	}

	var err error
	split.positive.records, err = run.request(split.positive.query)
	if err != nil {
		return split, false, err
	}
	if run.emptyAfterData {
		// Do not send the sibling request when the first side returned an ambiguous empty.
		return split, false, nil
	}

	split.negative.records, err = run.request(split.negative.query)
	if err != nil {
		return split, false, err
	}
	if run.emptyAfterData {
		return split, false, nil
	}

	valid, reason := validCMSplit(split.positive.records, split.negative.records, predicate)
	accepted := valid
	positiveRows := len(split.positive.records)
	negativeRows := len(split.negative.records)

	if !valid {
		if err := run.emit(DiscoveryEvent{
			Type:         "split_membership",
			Filter:       parent.Filter,
			Depth:        depth,
			SplitKey:     predicate.key,
			SplitValue:   predicate.value,
			PositiveRows: &positiveRows,
			NegativeRows: &negativeRows,
			Accepted:     &accepted,
			Reason:       reason,
		}); err != nil {
			return split, false, err
		}

		return split, false, nil
	}

	if err := run.emit(DiscoveryEvent{
		Type:         "split_membership",
		Filter:       parent.Filter,
		Depth:        depth,
		SplitKey:     predicate.key,
		SplitValue:   predicate.value,
		PositiveRows: &positiveRows,
		NegativeRows: &negativeRows,
		Accepted:     &accepted,
		Reason:       "membership_and_disjointness_validated",
	}); err != nil {
		return split, false, err
	}

	return split, true, nil
}

// walkSplit descends both accepted branches and verifies the parent sample is covered.
func (run *discoveryRun) walkSplit(
	parent QueryRequest,
	parentRecords []queryRecord,
	split discoverySplit,
	depth int,
) discoveryWalkResult {
	seen := endpointSet(parentRecords)
	positive := run.walk(split.positive.query, split.positive.path, split.positive.records, depth+1)
	if positive.err != nil {
		return unresolvedDiscoveryBranch(parent, seen, positive.err)
	}

	if run.emptyAfterData {
		// Preserve the already-fetched sibling sample; the ambiguous empty stops recursion.
		negative := discoveryWalkResult{
			queries: []QueryRequest{split.negative.query},
			seen:    endpointSet(split.negative.records),
			drained: !discoveryNearLimit(len(split.negative.records), int(run.limit)),
		}
		if !negative.drained {
			negative.unresolved = 1
		}

		return discoveryWalkResult{
			queries:    append(positive.queries, negative.queries...),
			seen:       mergeEndpointSets(positive.seen, negative.seen),
			unresolved: positive.unresolved + negative.unresolved,
		}
	}

	negative := run.walk(split.negative.query, split.negative.path, split.negative.records, depth+1)
	if negative.err != nil {
		return unresolvedDiscoveryBranch(parent, seen, negative.err)
	}

	covered, err := run.validateSplitCoverage(parent, parentRecords, split, positive, negative, depth)
	if err != nil {
		return unresolvedDiscoveryBranch(parent, seen, err)
	}
	if !covered {
		return unresolvedDiscoveryBranch(parent, seen, nil)
	}

	return discoveryWalkResult{
		queries:    append(positive.queries, negative.queries...),
		seen:       mergeEndpointSets(positive.seen, negative.seen),
		unresolved: positive.unresolved + negative.unresolved,
		drained:    positive.drained && negative.drained,
	}
}

// validateSplitCoverage checks that drained children account for every known parent row.
func (run *discoveryRun) validateSplitCoverage(
	parent QueryRequest,
	parentRecords []queryRecord,
	split discoverySplit,
	positive, negative discoveryWalkResult,
	depth int,
) (bool, error) {
	reportRejected := func(reason string) error {
		rejected := false
		return run.emit(DiscoveryEvent{
			Type:       "split_coverage",
			Filter:     parent.Filter,
			Depth:      depth,
			SplitKey:   split.predicate.key,
			SplitValue: split.predicate.value,
			Accepted:   &rejected,
			Reason:     reason,
		})
	}

	for _, record := range parentRecords {
		matched, known := recordMatches(record, split.predicate)
		if !known {
			// Without the predicate value, the parent row cannot be assigned to either child.
			return false, reportRejected("parent_sample_metadata_unknown")
		}

		branch := negative
		if matched {
			branch = positive
		}
		if branch.drained {
			// Only a drained branch promises complete coverage of its assigned parent rows.
			if _, exists := branch.seen[record.endpoint.Addr]; !exists {
				return false, reportRejected("drained_child_missing_parent_sample_endpoint")
			}
		}
	}

	return true, nil
}

// unresolvedDiscoveryBranch preserves the parent query when partitioning cannot finish.
func unresolvedDiscoveryBranch(query QueryRequest, seen map[netip.AddrPort]struct{}, err error) discoveryWalkResult {
	return discoveryWalkResult{queries: []QueryRequest{query}, seen: seen, err: err, unresolved: 1}
}

// request applies pacing, executes one CM query, emits diagnostics, and deduplicates results.
func (run *discoveryRun) request(query QueryRequest) ([]queryRecord, error) {
	if err := run.waitForRequestSlot(); err != nil {
		return nil, err
	}

	query.Limit = run.limit
	started := time.Now()
	run.lastRequestStart = started

	var records []queryRecord
	var err error
	if run.queryFn != nil {
		records, err = run.queryFn(run.ctx, query)
	} else {
		includeDetails := run.onEvent != nil || run.onServers != nil
		records, err = run.client.queryRecords(run.ctx, query, includeDetails)
	}

	run.calls++
	if err != nil {
		if emitErr := run.emit(DiscoveryEvent{
			Type:       "request",
			Request:    run.calls,
			Filter:     query.Filter,
			StartedAt:  started.UTC().Format(time.RFC3339Nano),
			DurationMS: time.Since(started).Milliseconds(),
			Error:      err.Error(),
		}); emitErr != nil {
			return nil, emitErr
		}

		return nil, err
	}

	if len(records) == 0 && run.successfulResponses > 0 {
		// A later empty reply may be throttling, so it cannot prove this branch is exhausted.
		run.emptyAfterData = true
	} else if len(records) > 0 {
		run.successfulResponses++
	}
	if run.onServers != nil && len(records) <= int(run.limit) {
		// Keep successful rows in the partial result if diagnostics stops the run.
		run.onServers(records)
	}

	if run.onEvent != nil {
		returnedRows := len(records)
		servers := make([]DiscoveryServer, len(records))
		for index, record := range records {
			if record.details != nil {
				servers[index] = *record.details
			} else {
				servers[index].Addr = record.endpoint.Addr.String()
			}
		}

		if err := run.emit(DiscoveryEvent{
			Type:         "request",
			Request:      run.calls,
			Filter:       query.Filter,
			StartedAt:    started.UTC().Format(time.RFC3339Nano),
			ReturnedRows: &returnedRows,
			Reason:       emptyResponseReason(run.emptyAfterData),
			DurationMS:   time.Since(started).Milliseconds(), Servers: servers,
		}); err != nil {
			return nil, err
		}
	}

	if len(records) > int(run.limit) {
		return nil, fmt.Errorf(
			"%w: received %d rows for limit %d",
			ErrDiscoveryResponseOverLimit, len(records), run.limit,
		)
	}

	for _, record := range records {
		address := record.endpoint.Addr
		if index, exists := run.indices[address]; exists {
			run.items[index] = record.endpoint
			continue
		}

		run.indices[address] = len(run.items)
		run.items = append(run.items, record.endpoint)
	}

	return records, nil
}

// emptyResponseReason labels empty results that cannot safely imply exhaustion.
func emptyResponseReason(emptyAfterData bool) string {
	if emptyAfterData {
		return "empty_response_after_nonempty_response"
	}

	return ""
}

// mergeEndpointSets returns the union of two observed endpoint sets.
func mergeEndpointSets(left, right map[netip.AddrPort]struct{}) map[netip.AddrPort]struct{} {
	merged := make(map[netip.AddrPort]struct{}, len(left)+len(right))
	for endpoint := range left {
		merged[endpoint] = struct{}{}
	}
	for endpoint := range right {
		merged[endpoint] = struct{}{}
	}

	return merged
}

// waitForRequestSlot enforces the configured minimum start-to-start interval.
func (run *discoveryRun) waitForRequestSlot() error {
	if run.requestInterval <= 0 || run.lastRequestStart.IsZero() || run.calls < run.initialBurst {
		// The burst applies only to the first N calls; it is never replenished.
		return nil
	}

	remaining := run.requestInterval - time.Since(run.lastRequestStart)
	if remaining <= 0 {
		return nil
	}

	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-run.ctx.Done():
		return run.ctx.Err()
	}
}

// emit synchronously forwards an event when diagnostics are enabled.
func (run *discoveryRun) emit(event DiscoveryEvent) error {
	if run.onEvent == nil {
		return nil
	}

	if err := run.onEvent(event); err != nil {
		return fmt.Errorf("write CM discovery diagnostic event: %w", err)
	}

	return nil
}

// intPointer preserves zero-valued optional diagnostic counts in JSON.
func intPointer(value int) *int {
	return &value
}

// queryForPath serializes a branch path into the raw CM filter grammar.
func (run *discoveryRun) queryForPath(path discoveryPath) QueryRequest {
	query := run.root
	filter := strings.TrimRight(query.Filter, `\`)
	appendPair := func(key, value string) {
		filter += `\` + key + `\` + value
	}

	for _, predicate := range path.positive {
		appendPair(predicate.key, predicate.value)
	}
	if len(path.excluded) > 0 {
		// CM's NOR count groups all following key/value pairs into one exclusion set.
		filter += `\nor\` + strconv.Itoa(len(path.excluded))
		for _, predicate := range path.excluded {
			appendPair(predicate.key, predicate.value)
		}
	}

	query.Filter = filter
	query.Limit = run.limit

	return query
}

// result snapshots accumulated endpoints and request counts with a plan.
func (run *discoveryRun) result(plan DiscoveryPlan) DiscoveryResult {
	return DiscoveryResult{
		Endpoints: run.items,
		Plan:      plan,
		Status:    DiscoveryStatus{Requests: run.calls},
	}
}

// chooseCMDiscoverySplit picks the closest-to-balanced eligible observed predicate.
func chooseCMDiscoverySplit(records []queryRecord, path discoveryPath, rootFilter string) (discoveryPredicate, bool) {
	counts := make(map[discoveryPredicate]int)
	// Map and version are scalar fields,
	// so do not add contradictory predicates for a key already present in this path.
	for _, key := range []string{"map", "version_match", "gametype"} {
		if filterHasKey(rootFilter, key) {
			continue
		}
		if key != "gametype" && pathHasKey(path, key) {
			continue
		}
		if key == "gametype" {
			// GameType is a tag set, so another tag can further partition a map cohort.
			// CM returned no rows for AppID+gametype without map;
			// validate each attempted split against both replies before accepting it.
			if !pathHasKey(path, "map") {
				continue
			}
		}

		knownForPage := true
		for _, record := range records {
			if _, known := recordValues(record, key); !known {
				knownForPage = false
				break
			}
		}

		if !knownForPage {
			continue
		}

		for _, record := range records {
			values, _ := recordValues(record, key)
			seen := make(map[string]struct{}, len(values))
			for _, value := range values {
				if validSplitValue(value) {
					if _, duplicate := seen[value]; duplicate {
						continue
					}

					seen[value] = struct{}{}
					counts[discoveryPredicate{key: key, value: value}]++
				}
			}
		}
	}

	candidates := make([]discoveryPredicate, 0, len(counts))
	for predicate := range counts {
		if counts[predicate] >= 2 && counts[predicate] < len(records)-1 &&
			!pathHasPredicate(path, predicate) {
			candidates = append(candidates, predicate)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		leftBalance := absInt(2*counts[left] - len(records))
		rightBalance := absInt(2*counts[right] - len(records))
		if leftBalance != rightBalance {
			return leftBalance < rightBalance
		}

		if left.key != right.key {
			return left.key < right.key
		}

		return left.value < right.value
	})

	if len(candidates) == 0 {
		return discoveryPredicate{}, false
	}

	return candidates[0], true
}

// recordValues returns known field values in the same form used by CM predicates.
func recordValues(record queryRecord, key string) ([]string, bool) {
	switch key {
	case "map":
		if record.mapName == "" {
			return nil, record.mapKnown
		}
		return []string{record.mapName}, record.mapKnown

	case "version_match":
		if record.version == "" || strings.ContainsAny(record.version, "*?[]") {
			return nil, record.versionKnown
		}
		return []string{record.version}, record.versionKnown

	case "gametype":
		if !record.gameTypeKnown {
			return nil, false
		}
		values := strings.Split(record.gameType, ",")
		for index := range values {
			values[index] = strings.TrimSpace(values[index])
		}
		return values, true

	default:
		return nil, false
	}
}

// recordMatches reports whether metadata is known and matches one predicate.
func recordMatches(record queryRecord, predicate discoveryPredicate) (bool, bool) {
	values, known := recordValues(record, predicate.key)
	if !known {
		return false, false
	}

	for _, value := range values {
		if strings.EqualFold(value, predicate.value) {
			return true, true
		}
	}

	return false, true
}

// validCMSplit checks returned membership and address disjointness before recursion.
func validCMSplit(positive, negative []queryRecord, predicate discoveryPredicate) (bool, string) {
	if len(positive) == 0 {
		return false, "positive_result_empty"
	}
	if len(negative) == 0 {
		return false, "negative_result_empty"
	}

	seen := make(map[netip.AddrPort]struct{}, len(positive))
	for _, record := range positive {
		matched, known := recordMatches(record, predicate)
		if !known {
			return false, "positive_result_metadata_unknown"
		}
		if !matched {
			return false, "positive_result_does_not_match_filter_value"
		}

		seen[record.endpoint.Addr] = struct{}{}
	}

	for _, record := range negative {
		matched, known := recordMatches(record, predicate)
		if !known {
			return false, "negative_result_metadata_unknown"
		}
		if matched {
			return false, "negative_result_contains_excluded_value"
		}
		if _, exists := seen[record.endpoint.Addr]; exists {
			return false, "positive_and_negative_results_overlap"
		}
	}

	return true, ""
}

// endpointSet builds a set of the endpoints in one response sample.
func endpointSet(records []queryRecord) map[netip.AddrPort]struct{} {
	seen := make(map[netip.AddrPort]struct{}, len(records))
	for _, record := range records {
		seen[record.endpoint.Addr] = struct{}{}
	}

	return seen
}

// validSplitValue rejects values that would alter the raw filter syntax.
func validSplitValue(value string) bool {
	return value != "" && !strings.ContainsAny(value, `\*?[]`)
}

// filterHasKey reports whether a key already exists in a raw root filter.
func filterHasKey(filter, key string) bool {
	return slices.Contains(strings.Split(strings.Trim(filter, `\`), `\`), key)
}

// pathHasKey checks both positive and excluded path predicates.
func pathHasKey(path discoveryPath, key string) bool {
	positive := slices.ContainsFunc(path.positive, func(predicate discoveryPredicate) bool {
		return predicate.key == key
	})

	if positive {
		return true
	}

	return slices.ContainsFunc(path.excluded, func(predicate discoveryPredicate) bool {
		return predicate.key == key
	})
}

// pathHasPredicate prevents selecting the same key/value twice on one branch.
func pathHasPredicate(path discoveryPath, candidate discoveryPredicate) bool {
	return slices.Contains(path.positive, candidate) || slices.Contains(path.excluded, candidate)
}

// absInt returns the non-negative magnitude used to rank split balance.
func absInt(value int) int {
	if value < 0 {
		return -value
	}

	return value
}

// discoveryNearLimit treats counts within 0.1% of the cap as potentially truncated.
func discoveryNearLimit(count, limit int) bool {
	margin := limit / 1000
	if limit%1000 != 0 {
		margin++
	}

	return count >= max(1, limit-margin)
}
