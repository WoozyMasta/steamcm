// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

package auth

import (
	"errors"
	"fmt"

	cm "github.com/woozymasta/steamcm"
)

// UnsupportedConfirmationError reports a Steam Guard option
// that this client cannot complete.
type UnsupportedConfirmationError struct {
	Type ConfirmationType
}

func (e *UnsupportedConfirmationError) Error() string {
	return fmt.Sprintf("Steam Guard confirmation type %d is unsupported", e.Type)
}

// FailureCategory identifies a recognized Steam authentication failure.
type FailureCategory string

const (
	// FailureUnclassified is a remote EResult without a known auth category.
	FailureUnclassified FailureCategory = "unclassified"
	// FailureInvalidCredentials means Steam rejected the account credentials.
	// #nosec G101 -- This is an error category, not a credential value.
	FailureInvalidCredentials FailureCategory = "invalid_credentials"
	// FailureGuardCodeRejected means Steam rejected the submitted Guard code.
	FailureGuardCodeRejected FailureCategory = "guard_code_rejected"
	// FailureAttemptExpired means the remote authentication attempt expired.
	FailureAttemptExpired FailureCategory = "attempt_expired"
	// FailureRateLimited means Steam is temporarily rejecting authentication.
	FailureRateLimited FailureCategory = "rate_limited"
	// FailureDuplicateRequest means Steam considers the request a duplicate.
	FailureDuplicateRequest FailureCategory = "duplicate_request"
)

// RemoteError preserves a recognized numeric Steam EResult
// without exposing response payloads or request secrets.
type RemoteError struct {
	cause    error
	Category FailureCategory
	EResult  int32
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("Steam authentication failed with EResult %d (%s)", e.EResult, e.Category)
}

func (e *RemoteError) Unwrap() error {
	return e.cause
}

// classifyRemoteError maps known CM EResult values to auth failure categories.
func classifyRemoteError(err error) error {
	var jobErr *cm.JobError
	if !errors.As(err, &jobErr) {
		return err
	}

	category := FailureUnclassified
	switch jobErr.EResult {
	case 5, 18:
		category = FailureInvalidCredentials

	case 65, 71, 88, 89:
		category = FailureGuardCodeRejected

	case 27:
		category = FailureAttemptExpired

	case 25, 84, 87:
		category = FailureRateLimited

	case 29:
		category = FailureDuplicateRequest
	}

	return &RemoteError{
		EResult:  jobErr.EResult,
		Category: category,
		cause:    err,
	}
}
