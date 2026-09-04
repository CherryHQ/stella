package server

import (
	"errors"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

// ListFeishuChannelChats returns the group chats visible to this specific
// running bot. The directory is intentionally live: persisted group policy is
// not a substitute for whether a bot is still a member of a chat.
func (s *Server) ListFeishuChannelChats(w http.ResponseWriter, r *http.Request, id string, params apiserver.ListFeishuChannelChatsParams) {
	access, ok := s.beginChannelAccess(w, r)
	if !ok {
		return
	}
	ch, err := access.GetChannel(r.Context(), id)
	if err != nil {
		s.writeControlPlaneError(w, err)
		return
	}
	if effectiveChannelType(ch) != pkgchannel.PlatformFeishu {
		writeError(w, http.StatusNotFound, "Feishu channel not found")
		return
	}

	pageSize := 100
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	if pageSize < 1 || pageSize > 100 {
		writeError(w, http.StatusBadRequest, "invalid pagination parameters")
		return
	}
	pageToken := ""
	if params.PageToken != nil {
		pageToken = *params.PageToken
	}
	if s.pluginHost == nil {
		writeError(w, http.StatusConflict, "Feishu channel is not running; save and enable it before listing groups")
		return
	}
	page, err := s.pluginHost.ListChannelChats(r.Context(), ch.ID, pageSize, pageToken)
	if err != nil {
		if errors.Is(err, pkgchannel.ErrJoinedChatListingUnavailable) {
			writeError(w, http.StatusConflict, "Feishu channel is not running; save and enable it before listing groups")
			return
		}
		s.log.Error("list Feishu channel chats", "channel_id", ch.ID, "error", err)
		s.writeBadGatewayError(w, err)
		return
	}

	chats := make([]apitypes.FeishuChat, len(page.Chats))
	for i, chat := range page.Chats {
		chats[i] = apitypes.FeishuChat{Id: chat.ID, Name: chat.Name}
	}
	var nextPageToken *string
	if page.NextPageToken != "" {
		nextPageToken = &page.NextPageToken
	}
	writeData(w, http.StatusOK, apitypes.FeishuChatList{Chats: chats, NextPageToken: nextPageToken})
}
