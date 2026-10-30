// SPDX-License-Identifier: MIT
// Copyright (c) 2026 WoozyMasta
// Source: github.com/woozymasta/steamcm

// Package gameservers implements Steam CM gameserver discovery methods.
package gameservers

import (
	"errors"

	steamcm "github.com/woozymasta/steamcm"
)

// Client calls gameserver methods on an authenticated CM session.
type Client struct {
	// session supplies the authenticated Steam CM request transport.
	session *steamcm.Session
}

// New creates a gameserver client for an authenticated CM session.
func New(session *steamcm.Session) (*Client, error) {
	if session == nil {
		return nil, errors.New("CM gameserver session is nil")
	}

	return &Client{session: session}, nil
}
