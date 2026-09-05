package channel

import "context"

// GroupPublisherRegistrar is the optional routing registration surface used
// when a channel adapter becomes ready to publish accepted group replies.
type GroupPublisherRegistrar interface {
	RegisterGroupPublisher(channelID string, publisher GroupPublisher)
}

// GroupPublisherUnregistrar removes a channel adapter from the internal
// publisher routing table during finalization.
type GroupPublisherUnregistrar interface {
	UnregisterGroupPublisher(channelID string)
}

// BotIdentityUnregistrar removes a bot identity during channel finalization.
type BotIdentityUnregistrar interface {
	UnregisterBotIdentity(platform, platformBotID, channelID string)
}

// BotNameUnregistrar removes a display-name routing entry during finalization.
type BotNameUnregistrar interface {
	UnregisterBotName(platform, displayName, channelID string)
}

// GroupMemberProvisioner ensures that a platform group is represented in the
// host before the adapter accepts group traffic.
type GroupMemberProvisioner interface {
	EnsurePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
}

// ThreadGroupMemberProvisioner is the topic/thread variant of group admission.
type ThreadGroupMemberProvisioner interface {
	EnsurePlatformThreadGroupMember(ctx context.Context, platform, platformGroupID, platformThreadID, legacyPlatformGroupID, channelID string) error
}

// GroupHistoryImporter imports platform history into the canonical group log.
type GroupHistoryImporter interface {
	ImportGroupHistory(ctx context.Context, messages []IncomingMessage) error
}

// GroupMemberRemover removes a platform group member during lifecycle cleanup.
type GroupMemberRemover interface {
	RemovePlatformGroupMember(ctx context.Context, platform, platformGroupID, channelID string) error
}
