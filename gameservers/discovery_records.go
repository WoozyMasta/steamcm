// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

// discoveryServerAccumulator deduplicates records while keeping latest metadata.
type discoveryServerAccumulator struct {
	// indices maps each query address to its stable position in servers.
	indices map[string]int
	// servers retains one latest record for each non-empty query address.
	servers []DiscoveryServer
}

// merge appends new addresses and refreshes metadata for addresses seen earlier.
func (accumulator *discoveryServerAccumulator) merge(records []queryRecord) {
	if len(records) == 0 {
		return
	}
	if accumulator.indices == nil {
		accumulator.indices = make(map[string]int, len(records))
	}

	for _, record := range records {
		if record.details == nil || record.details.Addr == "" {
			continue
		}
		if index, exists := accumulator.indices[record.details.Addr]; exists {
			accumulator.servers[index] = *record.details
			continue
		}

		accumulator.indices[record.details.Addr] = len(accumulator.servers)
		accumulator.servers = append(accumulator.servers, *record.details)
	}
}
