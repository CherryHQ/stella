package pluginapi

import "encoding/json"

const ProtocolVersion = "anna-plugin/v1"

type PluginKind string

const (
	KindTool    PluginKind = "tool"
	KindChannel PluginKind = "channel"
)

type Capability string

const (
	CapabilityToolCall         Capability = "tool.call"
	CapabilityChannelStart     Capability = "channel.start"
	CapabilityChannelStop      Capability = "channel.stop"
	CapabilityChannelNotify    Capability = "channel.notify"
	CapabilityHealthCheck      Capability = "health.check"
	CapabilityGracefulShutdown Capability = "shutdown.graceful"
)

type Manifest struct {
	Name            string         `json:"name"`
	Version         string         `json:"version"`
	Kind            PluginKind     `json:"kind"`
	ProtocolVersion string         `json:"protocol_version"`
	Entrypoint      string         `json:"entrypoint"`
	Args            []string       `json:"args,omitempty"`
	Description     string         `json:"description,omitempty"`
	Capabilities    []Capability   `json:"capabilities,omitempty"`
	ConfigSchema    map[string]any `json:"config_schema,omitempty"`
	Permissions     map[string]any `json:"permissions,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

type MessageType string

const (
	MessageTypeRequest  MessageType = "request"
	MessageTypeResponse MessageType = "response"
	MessageTypeEvent    MessageType = "event"
)

type Envelope struct {
	ID     string          `json:"id,omitempty"`
	Type   MessageType     `json:"type"`
	Method string          `json:"method,omitempty"`
	Event  string          `json:"event,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type HandshakeRequest struct {
	ProtocolVersion string `json:"protocol_version"`
}

type HandshakeResponse struct {
	ProtocolVersion string       `json:"protocol_version"`
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	Kind            PluginKind   `json:"kind"`
	Capabilities    []Capability `json:"capabilities,omitempty"`
}

type HealthResponse struct {
	OK bool `json:"ok"`
}
