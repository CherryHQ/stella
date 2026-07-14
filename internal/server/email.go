package server

import (
	"errors"
	"net/http"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/email"
)

// maxEmailRequestBytes caps the JSON body of email write endpoints. Attachments
// are not accepted over the API, so a few MB is ample for headers and text body
// while bounding memory use from an authenticated client.
const maxEmailRequestBytes = 5 << 20

// emailAccess derives the trusted Authority for the authenticated caller and
// binds one email use case to it. Email is a user-owned capability enforced by
// the captured user's vault namespace; the handler never inspects identity
// beyond deriving the Authority from verified session claims.
func (s *Server) emailAccess(w http.ResponseWriter, r *http.Request) (*email.Access, bool) {
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	authority, err := info.authority()
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return nil, false
	}
	acc, err := s.emailSvc.Access(authority)
	if err != nil {
		s.writeEmailError(w, err)
		return nil, false
	}
	return acc, true
}

func (s *Server) writeEmailError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authz.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, authz.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// ListEmailAccounts handles GET /api/email/accounts.
func (s *Server) ListEmailAccounts(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.emailAccess(w, r)
	if !ok {
		return
	}
	accounts, err := acc.Accounts(r.Context())
	if err != nil {
		s.writeEmailError(w, err)
		return
	}
	writeData(w, http.StatusOK, apitypes.EmailAccountList{Accounts: accounts.Accounts, Default: &accounts.Default})
}

// ListEmailFolders handles GET /api/email/folders.
func (s *Server) ListEmailFolders(w http.ResponseWriter, r *http.Request, params apiserver.ListEmailFoldersParams) {
	acc, ok := s.emailAccess(w, r)
	if !ok {
		return
	}
	account := ""
	if params.Account != nil {
		account = *params.Account
	}
	folders, err := acc.Folders(r.Context(), account)
	if err != nil {
		s.writeEmailError(w, err)
		return
	}
	writeData(w, http.StatusOK, apitypes.EmailFolderList{Folders: folders})
}

// ListEmailMessages handles GET /api/email/messages.
func (s *Server) ListEmailMessages(w http.ResponseWriter, r *http.Request, params apiserver.ListEmailMessagesParams) {
	acc, ok := s.emailAccess(w, r)
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
	account := ""
	if params.Account != nil {
		account = *params.Account
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
	msgs, err := acc.List(r.Context(), account, opts)
	if err != nil {
		s.writeEmailError(w, err)
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
	acc, ok := s.emailAccess(w, r)
	if !ok {
		return
	}
	imapUID, ok := parseIMAPUID(w, uid)
	if !ok {
		return
	}
	account := ""
	if params.Account != nil {
		account = *params.Account
	}
	folder := "INBOX"
	if params.Folder != nil {
		folder = *params.Folder
	}
	msg, err := acc.Read(r.Context(), account, folder, imapUID)
	if err != nil {
		if errors.Is(err, email.ErrMessageNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
		} else {
			s.writeEmailError(w, err)
		}
		return
	}
	writeData(w, http.StatusOK, emailMessageToAPI(msg))
}

// SendEmail handles POST /api/email/send.
func (s *Server) SendEmail(w http.ResponseWriter, r *http.Request, params apiserver.SendEmailParams) {
	acc, ok := s.emailAccess(w, r)
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
	account := ""
	if params.Account != nil {
		account = *params.Account
	}
	opts := email.SendOptions{To: body.To, Subject: body.Subject, Body: body.Body}
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
	idem := ""
	if body.IdempotencyKey != nil {
		idem = *body.IdempotencyKey
	}
	if _, err := acc.Send(r.Context(), account, opts, idem); err != nil {
		s.writeEmailError(w, err)
		return
	}
	writeNoContent(w)
}

// MarkEmailMessage handles POST /api/email/messages/{uid}/mark.
func (s *Server) MarkEmailMessage(w http.ResponseWriter, r *http.Request, uid int, params apiserver.MarkEmailMessageParams) {
	acc, ok := s.emailAccess(w, r)
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
	account := ""
	if params.Account != nil {
		account = *params.Account
	}
	folder := "INBOX"
	if params.Folder != nil {
		folder = *params.Folder
	}
	if err := acc.MarkSeen(r.Context(), account, folder, imapUID, *body.Seen); err != nil {
		if errors.Is(err, email.ErrMessageNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
		} else {
			s.writeEmailError(w, err)
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

func emailEnvelopeToAPI(e email.Envelope) apitypes.EmailEnvelope {
	env := apitypes.EmailEnvelope{Uid: int(e.UID), From: e.From, Subject: e.Subject, Date: e.Date.UTC()}
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
	msg := apitypes.EmailMessage{Uid: int(m.UID), From: m.From, Subject: m.Subject, Date: m.Date.UTC()}
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
