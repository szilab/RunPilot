// Package pluginapi defines RunPilot's small, versioned local plugin control
// protocol. It deliberately contains data only: process-local callbacks and
// network connections never cross the process boundary.
package pluginapi

import "github.com/szilab/RunPilot/internal/model"

const Version = 1

type Info struct {
	ID              string   `json:"id"`
	Version         string   `json:"version"`
	ProtocolVersion int      `json:"protocolVersion"`
	Capabilities    []string `json:"capabilities"`
}
type Health struct {
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}
type RemoteStatusRequest struct {
	Guacd model.GuacdConfig `json:"guacd,omitempty"`
}
type RemoteStartRequest struct {
	ID      string             `json:"id"`
	Target  model.RemoteTarget `json:"target"`
	DataDir string             `json:"dataDir"`
	Guacd   model.GuacdConfig  `json:"guacd,omitempty"`
}
type RemoteStartResponse struct {
	ClientKind        string            `json:"clientKind"`
	ClientParams      map[string]string `json:"clientParams,omitempty"`
	Endpoint          string            `json:"endpoint,omitempty"`
	TransportEndpoint string            `json:"transportEndpoint,omitempty"`
	Message           string            `json:"message,omitempty"`
}
type SessionState struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	Failure string `json:"failure,omitempty"`
}
type SessionProbe struct {
	WindowCount int    `json:"windowCount"`
	Message     string `json:"message,omitempty"`
}
