package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/email"
)

// maxEmailRequestBytes caps the JSON body of email write endpoints. Attachments
// are not accepted over the API, so a few MB is ample for headers and text body
// while bounding memory use from an authenticated client.
const maxEmailRequestBytes = 5 << 20

// loadEmailAccount loads the EMAIL_CONFIG vault entry for the authenticated
// user, parses it, and resolves the requested account name. On failure it
// writes an error response and returns a zero value + false.
func (s *Server) loadEmailAccount(w http.ResponseWriter, r *http.Request, accountParam *string) (email.EmailAccount, bool) {
	if s.vaultSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return email.EmailAccount{}, false
	}

	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return email.EmailAccount{}, false
	}

	value, err := s.vaultSvc.Get(r.Context(), info.UserID, "EMAIL_CONFIG")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "no email accounts configured")
			return email.EmailAccount{}, false
		}
		s.log.Error("load email config from vault", "user_id", info.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return email.EmailAccount{}, false
	}

	cfg := &email.Config{Accounts: make(map[string]email.EmailAccount)}
	if value != "" && value != "{}" {
		if err := json.Unmarshal([]byte(value), cfg); err != nil {
			writeError(w, http.StatusInternalServerError, "malformed EMAIL_CONFIG in vault")
			return email.EmailAccount{}, false
		}
	}
	if cfg.Accounts == nil {
		cfg.Accounts = make(map[string]email.EmailAccount)
	}
	if len(cfg.Accounts) == 0 {
		writeError(w, http.StatusBadRequest, "no email accounts configured")
		return email.EmailAccount{}, false
	}

	var name string
	if accountParam != nil {
		name = *accountParam
	}
	acct, err := cfg.Resolve(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("resolve email account: %v", err))
		return email.EmailAccount{}, false
	}
	if err := email.ValidateAccountEgress(acct); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return email.EmailAccount{}, false
	}
	return acct, true
}

// ListEmailFolders handles GET /api/email/folders.
func (s *Server) ListEmailFolders(w http.ResponseWriter, r *http.Request, params apiserver.ListEmailFoldersParams) {
	acct, ok := s.loadEmailAccount(w, r, params.Account)
	if !ok {
		return
	}

	folders, err := email.Folders(acct)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	writeData(w, http.StatusOK, apitypes.EmailFolderList{Folders: folders})
}

// ListEmailMessages handles GET /api/email/messages.
func (s *Server) ListEmailMessages(w http.ResponseWriter, r *http.Request, params apiserver.ListEmailMessagesParams) {
	acct, ok := s.loadEmailAccount(w, r, params.Account)
	if !ok {
		return
	}

	limit := 20
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 500 {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
		return
	}

	opts := email.ListOptions{Limit: limit}
	if params.Folder != nil {
		opts.Folder = *params.Folder
	}
	if params.Unread != nil {
		opts.Unread = *params.Unread
	}
	if params.From != nil {
		opts.From = *params.From
	}
	if params.Subject != nil {
		opts.Subject = *params.Subject
	}
	if params.Since != nil {
		t := params.Since.Time
		opts.Since = &t
	}
	if params.Before != nil {
		t := params.Before.Time
		opts.Before = &t
	}

	msgs, err := email.List(acct, opts)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	envelopes := make([]apitypes.EmailEnvelope, 0, len(msgs))
	for _, m := range msgs {
		envelopes = append(envelopes, emailEnvelopeToAPI(m))
	}
	writeData(w, http.StatusOK, apitypes.EmailEnvelopeList{Messages: envelopes})
}

// GetEmailMessage handles GET /api/email/messages/{uid}.
func (s *Server) GetEmailMessage(w http.ResponseWriter, r *http.Request, uid int, params apiserver.GetEmailMessageParams) {
	acct, ok := s.loadEmailAccount(w, r, params.Account)
	if !ok {
		return
	}

	imapUID, ok := parseIMAPUID(w, uid)
	if !ok {
		return
	}

	folder := "INBOX"
	if params.Folder != nil {
		folder = *params.Folder
	}

	msg, err := email.Read(acct, folder, imapUID)
	if err != nil {
		if errors.Is(err, email.ErrMessageNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
		} else {
			s.writeInternalError(w, err)
		}
		return
	}

	writeData(w, http.StatusOK, emailMessageToAPI(msg))
}

// SendEmail handles POST /api/email/send.
func (s *Server) SendEmail(w http.ResponseWriter, r *http.Request, params apiserver.SendEmailParams) {
	acct, ok := s.loadEmailAccount(w, r, params.Account)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxEmailRequestBytes)
	var body apitypes.EmailSendRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if len(body.To) == 0 {
		writeError(w, http.StatusBadRequest, "to must contain at least one recipient")
		return
	}
	if strings.TrimSpace(body.Subject) == "" {
		writeError(w, http.StatusBadRequest, "subject is required")
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		writeError(w, http.StatusBadRequest, "body is required")
		return
	}

	opts := email.SendOptions{
		To:      body.To,
		Subject: body.Subject,
		Body:    body.Body,
	}
	if body.Cc != nil {
		opts.Cc = *body.Cc
	}
	if body.Bcc != nil {
		opts.Bcc = *body.Bcc
	}
	if body.Html != nil {
		opts.HTML = *body.Html
	}
	if body.From != nil {
		opts.From = *body.From
	}
	if body.ReplyTo != nil {
		opts.ReplyTo = *body.ReplyTo
	}
	if body.InReplyTo != nil {
		opts.InReplyTo = *body.InReplyTo
	}

	if err := email.Send(acct, opts); err != nil {
		s.writeInternalError(w, err)
		return
	}

	writeNoContent(w)
}

// MarkEmailMessage handles POST /api/email/messages/{uid}/mark.
func (s *Server) MarkEmailMessage(w http.ResponseWriter, r *http.Request, uid int, params apiserver.MarkEmailMessageParams) {
	acct, ok := s.loadEmailAccount(w, r, params.Account)
	if !ok {
		return
	}

	imapUID, ok := parseIMAPUID(w, uid)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxEmailRequestBytes)
	var body struct {
		Seen *bool `json:"seen"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Seen == nil {
		writeError(w, http.StatusBadRequest, "seen is required")
		return
	}

	folder := "INBOX"
	if params.Folder != nil {
		folder = *params.Folder
	}

	if err := email.MarkSeen(acct, folder, imapUID, *body.Seen); err != nil {
		if errors.Is(err, email.ErrMessageNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
		} else {
			s.writeInternalError(w, err)
		}
		return
	}

	writeNoContent(w)
}

func parseIMAPUID(w http.ResponseWriter, uid int) (uint32, bool) {
	const maxIMAPUID = int64(^uint32(0))
	if uid < 1 || int64(uid) > maxIMAPUID {
		writeError(w, http.StatusBadRequest, "uid must be between 1 and 4294967295")
		return 0, false
	}
	return uint32(uid), true
}

// --------------- converters ---------------

func emailEnvelopeToAPI(e email.Envelope) apitypes.EmailEnvelope {
	env := apitypes.EmailEnvelope{
		Uid:     int(e.UID),
		From:    e.From,
		Subject: e.Subject,
		Date:    e.Date.UTC(),
	}
	if e.MessageID != "" {
		env.MessageId = &e.MessageID
	}
	if e.FromName != "" {
		env.FromName = &e.FromName
	}
	if e.FromAddr != "" {
		env.FromAddr = &e.FromAddr
	}
	if len(e.To) > 0 {
		env.To = &e.To
	}
	if len(e.Flags) > 0 {
		env.Flags = &e.Flags
	}
	if e.HasAttachments {
		env.HasAttachments = &e.HasAttachments
	}
	if e.Size > 0 {
		size := int(e.Size)
		env.Size = &size
	}
	return env
}

func emailMessageToAPI(m *email.Message) apitypes.EmailMessage {
	msg := apitypes.EmailMessage{
		Uid:     int(m.UID),
		From:    m.From,
		Subject: m.Subject,
		Date:    m.Date.UTC(),
	}
	if m.MessageID != "" {
		msg.MessageId = &m.MessageID
	}
	if m.FromName != "" {
		msg.FromName = &m.FromName
	}
	if m.FromAddr != "" {
		msg.FromAddr = &m.FromAddr
	}
	if len(m.To) > 0 {
		msg.To = &m.To
	}
	if len(m.Flags) > 0 {
		msg.Flags = &m.Flags
	}
	if m.HasAttachments {
		msg.HasAttachments = &m.HasAttachments
	}
	if m.Size > 0 {
		size := int(m.Size)
		msg.Size = &size
	}
	if m.TextBody != "" {
		msg.TextBody = &m.TextBody
	}
	if m.HTMLBody != "" {
		msg.HtmlBody = &m.HTMLBody
	}
	if len(m.Attachments) > 0 {
		atts := make([]apitypes.EmailAttachmentInfo, 0, len(m.Attachments))
		for _, a := range m.Attachments {
			att := apitypes.EmailAttachmentInfo{}
			if a.Filename != "" {
				att.Filename = &a.Filename
			}
			if a.Size > 0 {
				size := int(a.Size)
				att.Size = &size
			}
			if a.MIMEType != "" {
				att.MimeType = &a.MIMEType
			}
			atts = append(atts, att)
		}
		msg.Attachments = &atts
	}
	return msg
}
