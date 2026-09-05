package email

import pkgemail "github.com/CherryHQ/stella/pkg/email"

var ErrMessageNotFound = pkgemail.ErrMessageNotFound

type (
	Envelope       = pkgemail.Envelope
	AttachmentInfo = pkgemail.AttachmentInfo
	Message        = pkgemail.Message
	ListOptions    = pkgemail.ListOptions
	SendOptions    = pkgemail.SendOptions
	SendResult     = pkgemail.SendResult
	AccountList    = pkgemail.AccountList
	Access         = pkgemail.Access
)
