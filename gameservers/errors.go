// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package gameservers

import "errors"

var (
	// ErrNilDiscoveryContext reports a nil context supplied to discovery.
	ErrNilDiscoveryContext = errors.New("nil CM discovery context")
	// ErrInvalidDiscoveryOptions reports invalid discovery resource limits.
	ErrInvalidDiscoveryOptions = errors.New("invalid CM discovery options")
	// ErrInvalidDiscoveryPlan reports a malformed or unsupported saved plan.
	ErrInvalidDiscoveryPlan = errors.New("invalid CM discovery plan")
	// ErrUnsupportedDiscoveryFilter reports a root filter the planner cannot safely extend.
	ErrUnsupportedDiscoveryFilter = errors.New("unsupported CM discovery root filter")
	// ErrDiscoveryResponseOverLimit reports a CM response larger than the requested cap.
	ErrDiscoveryResponseOverLimit = errors.New("CM discovery response exceeded its limit")
)

// ResponseError reports an error string returned by the CM gameserver method.
type ResponseError struct {
	Message string
}

func (e *ResponseError) Error() string {
	if e.Message == "" {
		return "CM gameserver response contains an error"
	}

	return "CM gameserver response error: " + e.Message
}
