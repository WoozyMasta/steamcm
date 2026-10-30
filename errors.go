// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package cm

import "fmt"

// Common Steam EResult values returned by CM operations.
const (
	EResultOK           int32 = 1
	EResultFail         int32 = 2
	EResultAccessDenied int32 = 15
)

// HTTPError reports a non-success response from the CM directory endpoint.
type HTTPError struct {
	Status     string
	StatusCode int
}

func (e *HTTPError) Error() string {
	return "CM directory returned HTTP status " + e.Status
}

// DirectoryError reports an error returned by the directory API.
type DirectoryError struct {
	Message string
}

func (e *DirectoryError) Error() string {
	if e.Message == "" {
		return "CM directory returned an unsuccessful response"
	}

	return "CM directory error: " + e.Message
}

// LogonError reports a non-success EResult from ClientLogonResponse.
type LogonError struct {
	Message string
	EResult int32
}

func (e *LogonError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("CM logon failed with EResult %d", e.EResult)
	}

	return fmt.Sprintf("CM logon failed with EResult %d: %s", e.EResult, e.Message)
}

// LogoffError reports a server-initiated CM session termination.
type LogoffError struct {
	EResult    int32
	HasEResult bool
}

func (e *LogoffError) Error() string {
	if !e.HasEResult {
		return "CM session was logged off"
	}

	return fmt.Sprintf("CM session was logged off with EResult %d", e.EResult)
}

// JobError reports a non-success EResult from a correlated CM job.
type JobError struct {
	Message string
	EResult int32
}

func (e *JobError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("CM job failed with EResult %d", e.EResult)
	}

	return fmt.Sprintf("CM job failed with EResult %d: %s", e.EResult, e.Message)
}

// MissingEResultError reports a CM job response
// without an EResult field when the request requires one.
type MissingEResultError struct{}

func (MissingEResultError) Error() string {
	return "CM job response is missing EResult"
}
