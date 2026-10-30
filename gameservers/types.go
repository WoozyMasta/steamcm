// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import (
	"net/netip"
	"strings"
)

// Endpoint is a server address returned by a CM discovery method.
type Endpoint struct {
	Addr           netip.AddrPort `json:"addr"                       yaml:"addr"`
	AuthPlayers    uint32         `json:"auth_players,omitempty"     yaml:"auth_players,omitempty"`
	HasAuthPlayers bool           `json:"has_auth_players,omitempty" yaml:"has_auth_players,omitempty"`
}

// ListQuery contains the parameters for a CM GetServerList request.
type ListQuery struct {
	Filter string `json:"filter,omitempty" yaml:"filter,omitempty"`
	Limit  uint32 `json:"limit,omitempty"  yaml:"limit,omitempty"`
}

// Server is a rich server record returned by the CM GetServerList method.
type Server struct {
	Addr          netip.AddrPort `yaml:"addr"               json:"addr"`
	Name          string         `yaml:"name"               json:"name"`
	GameDir       string         `yaml:"gamedir"            json:"gamedir"`
	Map           string         `yaml:"map"                json:"map"`
	Product       string         `yaml:"product"            json:"product"`
	Version       string         `yaml:"version"            json:"version"`
	OS            string         `yaml:"os"                 json:"os"`
	GameType      string         `yaml:"gametype"           json:"gametype"`
	SteamID       uint64         `yaml:"steamid"            json:"steamid,string"`
	AppID         uint32         `yaml:"appid"              json:"appid"`
	Region        int32          `yaml:"region,omitempty"   json:"region,omitempty"`
	Players       int32          `yaml:"players"            json:"players"`
	MaxPlayers    int32          `yaml:"max_players"        json:"max_players"`
	Bots          int32          `yaml:"bots,omitempty"     json:"bots,omitempty"`
	GamePort      uint16         `yaml:"gameport"           json:"gameport"`
	SpectatorPort uint16         `yaml:"specport,omitempty" json:"specport,omitempty"`
	Dedicated     bool           `yaml:"dedicated"          json:"dedicated"`
	Secure        bool           `yaml:"secure"             json:"secure"`
}

// GameTypes splits the raw Steam game-type field into trimmed tags.
func (s Server) GameTypes() []string {
	if s.GameType == "" {
		return nil
	}

	parts := strings.Split(s.GameType, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, strings.TrimSpace(part))
	}

	return result
}
