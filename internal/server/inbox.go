package server

import (
	"errors"
	"net/http"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/inbox"
)

func (s *Server) ListInbox(w http.ResponseWriter, r *http.Request, params apiserver.ListInboxParams) {
	info := requireAuth(w, r)
	if info == nil {
		return
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	pageSize := 20
	if params.PageSize != nil {
		if *params.PageSize < 0 {
			writeError(w, http.StatusBadRequest, "page_size must not be negative")
			return
		}
		if *params.PageSize > 0 {
			pageSize = *params.PageSize
		}
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset, err := decodeOffsetToken(derefStr(params.PageToken))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page token")
		return
	}

	items, hasMore, err := s.inboxSvc.List(r.Context(), authority, derefStr(params.AgentId), offset, pageSize)
	if err != nil {
		switch {
		case errors.Is(err, inbox.ErrForbidden):
			writeError(w, http.StatusForbidden, "forbidden")
		case errors.Is(err, inbox.ErrInvalidPage):
			writeError(w, http.StatusBadRequest, "invalid page token")
		default:
			s.writeInternalError(w, err)
		}
		return
	}

	out := make([]apitypes.InboxItem, 0, len(items))
	for _, item := range items {
		out = append(out, inboxItemToAPI(item))
	}
	var nextPageToken *string
	if hasMore {
		tok := encodeOffsetToken(offset + pageSize)
		nextPageToken = &tok
	}
	writeData(w, http.StatusOK, apitypes.InboxList{
		Items:         out,
		NextPageToken: nextPageToken,
	})
}

func inboxItemToAPI(item inbox.Item) apitypes.InboxItem {
	return apitypes.InboxItem{
		Id:         item.ID,
		Kind:       apitypes.InboxItemKind(item.Kind),
		Title:      item.Title,
		Detail:     optionalString(item.Detail),
		AgentId:    optionalString(item.AgentID),
		ProjectId:  optionalString(item.ProjectID),
		SourceType: apitypes.InboxItemSourceType(item.Source),
		SourceId:   item.SourceID,
		TargetPath: item.TargetPath,
		CreatedAt:  item.CreatedAt,
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
