// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import "time"

const (
	// DefaultDiscoveryLimit is the observed per-query CM result ceiling.
	DefaultDiscoveryLimit uint32 = 10000
	// RecommendedDiscoveryBurst is a suggested initial request burst size.
	RecommendedDiscoveryBurst = 3
	// RecommendedDiscoveryRequestInterval is a suggested spacing between requests.
	RecommendedDiscoveryRequestInterval = time.Minute
)

// DiscoveryOptions bounds request count and recursive filter partitioning.
type DiscoveryOptions struct {
	// OnEvent receives synchronous request, split, and stop diagnostics.
	// Returning an error stops discovery.
	// Setting this callback also includes the decoded CM server metadata in each request event.
	OnEvent func(DiscoveryEvent) error `json:"-" yaml:"-"`

	// MinRequestInterval is the minimum spacing between request starts after InitialBurst.
	// Zero disables pacing. For polling, consider 35–60 seconds.
	MinRequestInterval time.Duration `json:"min_request_interval,omitempty" yaml:"min_request_interval,omitempty"`

	// InitialBurst is the number of initial requests exempt from MinRequestInterval.
	// It is not replenished during the run. Zero sends no initial burst.
	InitialBurst int `json:"initial_burst,omitempty" yaml:"initial_burst,omitempty"`

	// MaxRequests bounds all CM calls in one discovery run; zero uses 24.
	MaxRequests int `json:"max_requests,omitempty" yaml:"max_requests,omitempty"`

	// MaxDepth bounds nested binary partitions; zero uses 16.
	MaxDepth int `json:"max_depth,omitempty" yaml:"max_depth,omitempty"`
}

// DiscoveryEvent reports one step of a discovery run for diagnostics.
type DiscoveryEvent struct {
	// StartedAt is the UTC start time of a CM request.
	StartedAt string `json:"started_at,omitempty"`
	// Accepted reports whether a candidate split passed validation.
	Accepted *bool `json:"accepted,omitempty"`
	// ReturnedRows is the number of rows in a CM response.
	ReturnedRows *int `json:"returned_rows,omitempty"`
	// PositiveRows is the response size for PositiveFilter.
	PositiveRows *int `json:"positive_rows,omitempty"`
	// NegativeRows is the response size for NegativeFilter.
	NegativeRows *int `json:"negative_rows,omitempty"`
	// Type identifies the diagnostic event kind.
	Type string `json:"type"`
	// Filter is the parent or request filter associated with the event.
	Filter string `json:"filter,omitempty"`
	// Reason explains a leaf stop or split validation result.
	Reason string `json:"reason,omitempty"`
	// SplitKey is the CM filter key selected for partitioning.
	SplitKey string `json:"split_key,omitempty"`
	// SplitValue is the observed value selected for partitioning.
	SplitValue string `json:"split_value,omitempty"`
	// PositiveFilter matches SplitKey and SplitValue.
	PositiveFilter string `json:"positive_filter,omitempty"`
	// NegativeFilter excludes SplitKey and SplitValue.
	NegativeFilter string `json:"negative_filter,omitempty"`
	// Error contains the request error, if one occurred.
	Error string `json:"error,omitempty"`
	// Servers contains decoded rows when diagnostics are enabled.
	Servers []DiscoveryServer `json:"servers,omitempty"`
	// Request is the one-based CM request number.
	Request int `json:"request,omitempty"`
	// Depth is the partition depth associated with a split or leaf.
	Depth int `json:"depth,omitempty"`
	// DurationMS is the request duration in milliseconds.
	DurationMS int64 `json:"duration_ms,omitempty"`
}

// DiscoveryServer contains the server fields exposed by the CM master query.
// The CM response does not provide every field available from Web API.
type DiscoveryServer struct {
	// Addr is the server query endpoint.
	Addr string `json:"addr"`
	// GameDir is the game's directory identifier.
	GameDir string `json:"gamedir,omitempty"`
	// Map is the current map name.
	Map string `json:"map,omitempty"`
	// Name is the advertised server name.
	Name string `json:"name,omitempty"`
	// GameDescription is the advertised game description.
	GameDescription string `json:"game_description,omitempty"`
	// Version is the advertised game version.
	Version string `json:"version,omitempty"`
	// GameType contains the raw comma-separated game tags.
	GameType string `json:"gametype,omitempty"`
	// SpectatorName is the advertised spectator server name.
	SpectatorName string `json:"spectator_name,omitempty"`
	// SDRPingLocation is the server's Steam Datagram Relay location.
	SDRPingLocation string `json:"sdr_ping_location,omitempty"`
	// SteamID is the server's Steam identifier.
	SteamID uint64 `json:"steamid,string,omitempty"`
	// Revision is the CM record revision.
	Revision uint32 `json:"revision,omitempty"`
	// Players is the reported current player count.
	Players uint32 `json:"players,omitempty"`
	// GamePort is the advertised game port.
	GamePort uint32 `json:"gameport,omitempty"`
	// QueryPort is the endpoint port used by the master query.
	QueryPort uint32 `json:"queryport,omitempty"`
	// SDRPopID identifies the server's SDR point of presence.
	SDRPopID uint32 `json:"sdr_popid,omitempty"`
	// Flags contains opaque CM server flags.
	Flags uint32 `json:"flags,omitempty"`
	// AppID is the server's application ID when present in the response.
	AppID uint32 `json:"appid,omitempty"`
	// MaxPlayers is the advertised player capacity.
	MaxPlayers uint32 `json:"max_players,omitempty"`
	// Bots is the reported bot count.
	Bots uint32 `json:"bots,omitempty"`
	// SpectatorPort is the advertised spectator port.
	SpectatorPort uint32 `json:"specport,omitempty"`
	// AuthPlayers is the authenticated player count reported by CM.
	AuthPlayers uint32 `json:"auth_players,omitempty"`
	// FriendsCount is the friend count included in this record.
	FriendsCount uint32 `json:"friends_count,omitempty"`
	// FriendsCountConnected is the connected friend count.
	FriendsCountConnected uint32 `json:"friends_count_connected,omitempty"`
	// PingEstimate is the CM ping estimate when present.
	PingEstimate uint32 `json:"ping_estimate,omitempty"`
	// MapKnown reports whether the map string was present or resolvable.
	MapKnown bool `json:"map_known"`
	// VersionKnown reports whether the version string was present or resolvable.
	VersionKnown bool `json:"version_known"`
	// GameTypeKnown reports whether the game-type string was present or resolvable.
	GameTypeKnown bool `json:"gametype_known"`
}

// DiscoveryPlan stores filters for subsequent polling runs.
// CM does not expose a continuation cursor,
// and repeated filters may return overlapping subsets.
type DiscoveryPlan struct {
	// Queries are the terminal filters that can be re-executed independently.
	Queries []QueryRequest `json:"queries" yaml:"queries"`
	// Root is the original query before planner-generated predicates.
	Root QueryRequest `json:"root" yaml:"root"`
	// Limit is the response cap used for every query in the plan.
	Limit uint32 `json:"limit" yaml:"limit"`
}

// DiscoveryStatus reports whether every planned leaf
// was drained below the near-cap threshold without encountering an ambiguous empty response.
type DiscoveryStatus struct {
	// Requests is the number of CM queries attempted.
	Requests int `json:"requests" yaml:"requests"`
	// Unresolved counts near-cap leaves, unsafe splits, or queries not reached within budget.
	Unresolved int `json:"unresolved" yaml:"unresolved"`
	// Complete means every branch drained below the 0.1% near-cap threshold
	// and sampled split membership checks were consistent.
	Complete bool `json:"complete" yaml:"complete"`
}

// DiscoveryResult contains deduplicated endpoints, a reusable plan, and run status.
type DiscoveryResult struct {
	// Endpoints contains unique query addresses observed during this run.
	Endpoints []Endpoint `json:"endpoints" yaml:"endpoints"`
	// Plan stores the terminal filters for later polling.
	Plan DiscoveryPlan `json:"plan" yaml:"plan"`
	// Status describes request coverage for this run.
	Status DiscoveryStatus `json:"status" yaml:"status"`
}

// ServerDiscoveryResult contains the metadata records retained during discovery.
type ServerDiscoveryResult struct {
	// Servers contains one latest record per observed query address.
	Servers []DiscoveryServer `json:"servers" yaml:"servers"`
	// Plan stores the terminal filters for later collection.
	Plan DiscoveryPlan `json:"plan" yaml:"plan"`
	// Status describes request coverage for this run.
	Status DiscoveryStatus `json:"status" yaml:"status"`
}

// CollectionOptions bounds execution of a saved plan; zero allows every leaf.
type CollectionOptions struct {
	// OnEvent receives synchronous request events and decoded server metadata.
	// Returning an error stops collection.
	OnEvent func(DiscoveryEvent) error `json:"-" yaml:"-"`
	// MaxRequests limits queries executed from the saved plan.
	MaxRequests int `json:"max_requests,omitempty" yaml:"max_requests,omitempty"`
	// MinRequestInterval spaces request start times after InitialBurst. Zero adds no delay.
	// Use [RecommendedDiscoveryRequestInterval] for repeated CM polling.
	MinRequestInterval time.Duration `json:"min_request_interval,omitempty" yaml:"min_request_interval,omitempty"`
	// InitialBurst is the one-time number of unpaced requests.
	// Zero means no burst; the allowance does not replenish during the run.
	InitialBurst int `json:"initial_burst,omitempty" yaml:"initial_burst,omitempty"`
}

// CollectionResult contains endpoints observed while executing a saved plan.
type CollectionResult struct {
	// Endpoints contains unique query addresses observed during collection.
	Endpoints []Endpoint `json:"endpoints" yaml:"endpoints"`
	// Status describes whether all plan leaves were drained below the near-cap threshold.
	Status DiscoveryStatus `json:"status" yaml:"status"`
}

// ServerCollectionResult contains metadata records retained while executing a plan.
type ServerCollectionResult struct {
	// Servers contains one latest record per observed query address.
	Servers []DiscoveryServer `json:"servers" yaml:"servers"`
	// Status describes whether all plan leaves were drained below the near-cap threshold.
	Status DiscoveryStatus `json:"status" yaml:"status"`
}
