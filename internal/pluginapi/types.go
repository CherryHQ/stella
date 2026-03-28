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
	CapabilityChannelInbound   Capability = "channel.inbound"
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
	Tool            *ToolSpec      `json:"tool,omitempty"`
	Capabilities    []Capability   `json:"capabilities,omitempty"`
	ConfigSchema    map[string]any `json:"config_schema,omitempty"`
	Permissions     map[string]any `json:"permissions,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
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
	Tool            *ToolSpec    `json:"tool,omitempty"`
}

type HealthResponse struct {
	OK bool `json:"ok"`
}

type ToolCallRequest struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ToolCallResponse struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type ChannelNotification struct {
	Channel string `json:"channel,omitempty"`
	ChatID  string `json:"chat_id,omitempty"`
	Text    string `json:"text,omitempty"`
	Silent  bool   `json:"silent,omitempty"`
}

type ChannelStartRequest struct {
	Config json.RawMessage `json:"config,omitempty"`
}

type ChannelStartResponse struct {
	Started bool `json:"started"`
}

type ChannelStopRequest struct{}

type ChannelStopResponse struct {
	Stopped bool `json:"stopped"`
}

type ChannelNotifyRequest struct {
	Notification ChannelNotification `json:"notification"`
}

type ChannelNotifyResponse struct {
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

type ChannelInboundMessage struct {
	Platform   string         `json:"platform"`
	ChatID     string         `json:"chat_id,omitempty"`
	SenderID   string         `json:"sender_id,omitempty"`
	SenderName string         `json:"sender_name,omitempty"`
	MessageID  string         `json:"message_id,omitempty"`
	IsGroup    bool           `json:"is_group,omitempty"`
	Text       string         `json:"text,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
}

type ChannelEvent struct {
	Kind    string                 `json:"kind"`
	Message *ChannelInboundMessage `json:"message,omitempty"`
	Error   *RPCError              `json:"error,omitempty"`
}
