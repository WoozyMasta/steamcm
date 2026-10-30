// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
)

const (
	// defaultDirectoryURL is Steam's public endpoint directory API.
	defaultDirectoryURL = "https://api.steampowered.com/ISteamDirectory/GetCMListForConnect/v1/"
	// maxDirectoryResponse caps memory used for the directory JSON body.
	maxDirectoryResponse = 1 << 20
)

// Endpoint is a CM endpoint returned by the public directory.
type Endpoint struct {
	Address      string  `json:"endpoint"                  yaml:"endpoint"`
	Legacy       string  `json:"legacy_endpoint,omitempty" yaml:"legacy_endpoint,omitempty"`
	Type         string  `json:"type,omitempty"            yaml:"type,omitempty"`
	DataCenter   string  `json:"dc,omitempty"              yaml:"dc,omitempty"`
	Realm        string  `json:"realm,omitempty"           yaml:"realm,omitempty"`
	Load         int32   `json:"load,omitempty"            yaml:"load,omitempty"`
	WeightedLoad float64 `json:"wtd_load,omitempty"        yaml:"wtd_load,omitempty"`
}

// DirectoryQuery contains filters accepted by GetCMListForConnect.
type DirectoryQuery struct {
	CMType   string
	Realm    string
	CellID   uint32
	MaxCount uint32
	QoSLevel uint32
}

// Directory queries the public Steam CM directory.
type Directory struct {
	HTTPClient *http.Client
	BaseURL    string
}

// directoryResponse matches the success envelope returned by the CM directory.
type directoryResponse struct {
	Response struct {
		// ServerList contains the selected CM endpoints.
		ServerList []Endpoint `json:"serverlist"`
		// Success indicates whether the directory request succeeded.
		Success bool `json:"success"`
		// Message explains a failed directory request.
		Message string `json:"message"`
	} `json:"response"`
}

// Get returns WebSocket-capable CM endpoints matching query.
func (d *Directory) Get(ctx context.Context, query DirectoryQuery) ([]Endpoint, error) {
	if ctx == nil {
		return nil, errors.New("CM directory context is nil")
	}

	client := d.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	baseURL := d.BaseURL
	if baseURL == "" {
		baseURL = defaultDirectoryURL
	}

	requestURL, err := url.Parse(baseURL)
	if err != nil || requestURL.Scheme == "" || requestURL.Host == "" {
		return nil, fmt.Errorf("invalid CM directory URL %q", baseURL)
	}

	params := requestURL.Query()
	params.Set("format", "json")
	params.Set("cellid", strconv.FormatUint(uint64(query.CellID), 10))

	cmType := query.CMType
	if cmType == "" {
		cmType = "websockets"
	}

	params.Set("cmtype", cmType)
	if query.Realm != "" {
		params.Set("realm", query.Realm)
	}
	if query.MaxCount != 0 {
		params.Set("maxcount", strconv.FormatUint(uint64(query.MaxCount), 10))
	}
	if query.QoSLevel != 0 {
		params.Set("qoslevel", strconv.FormatUint(uint64(query.QoSLevel), 10))
	}
	requestURL.RawQuery = params.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create CM directory request: %w", err)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute CM directory request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &HTTPError{StatusCode: response.StatusCode, Status: response.Status}
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxDirectoryResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read CM directory response: %w", err)
	}

	if len(body) > maxDirectoryResponse {
		return nil, fmt.Errorf("CM directory response exceeds %d bytes", maxDirectoryResponse)
	}

	return decodeDirectoryResponse(body)
}

// decodeDirectoryResponse validates the directory envelope and every endpoint.
func decodeDirectoryResponse(body []byte) ([]Endpoint, error) {
	var envelope directoryResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode CM directory response: %w", err)
	}
	if !envelope.Response.Success {
		return nil, &DirectoryError{Message: envelope.Response.Message}
	}

	for i := range envelope.Response.ServerList {
		if err := validateEndpoint(envelope.Response.ServerList[i].Address); err != nil {
			return nil, fmt.Errorf("invalid CM endpoint %d: %w", i, err)
		}
	}

	return envelope.Response.ServerList, nil
}

// validateEndpoint requires a host and a non-zero port.
func validateEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("invalid endpoint %q", endpoint)
	}

	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return fmt.Errorf("invalid endpoint port in %q", endpoint)
	}

	return nil
}
