// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

// PortForwardMessageType represents the type of port-forward WebSocket message.
type PortForwardMessageType string

const (
	// PortForwardMessageTypeStart is sent by the client to initiate port-forwarding.
	PortForwardMessageTypeStart PortForwardMessageType = "start"
	// PortForwardMessageTypeConnected is received when the tunnel is established.
	PortForwardMessageTypeConnected PortForwardMessageType = "connected"
	// PortForwardMessageTypeError is received when an error occurs.
	PortForwardMessageTypeError PortForwardMessageType = "error"
)
