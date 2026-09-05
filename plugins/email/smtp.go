package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	mail "github.com/wneessen/go-mail"
)

// Send composes and sends an email via SMTP using the provided account configuration.
func Send(acct EmailAccount, opts SendOptions) error {
	if len(opts.To) == 0 && len(opts.Cc) == 0 && len(opts.Bcc) == 0 {
		return fmt.Errorf("at least one recipient is required (--to, --cc, or --bcc)")
	}

	msg := mail.NewMsg()

	from := opts.From
	if from == "" {
		from = acct.From
	}
	if err := msg.From(from); err != nil {
		return fmt.Errorf("set From header: %w", err)
	}

	if len(opts.To) > 0 {
		if err := msg.To(opts.To...); err != nil {
			return fmt.Errorf("set To header: %w", err)
		}
	}
	if len(opts.Cc) > 0 {
		if err := msg.Cc(opts.Cc...); err != nil {
			return fmt.Errorf("set Cc header: %w", err)
		}
	}
	if len(opts.Bcc) > 0 {
		if err := msg.Bcc(opts.Bcc...); err != nil {
			return fmt.Errorf("set Bcc header: %w", err)
		}
	}

	msg.Subject(opts.Subject)

	if opts.HTML {
		msg.SetBodyString(mail.TypeTextHTML, opts.Body)
	} else {
		msg.SetBodyString(mail.TypeTextPlain, opts.Body)
	}

	if opts.ReplyTo != "" {
		if err := msg.ReplyTo(opts.ReplyTo); err != nil {
			return fmt.Errorf("set Reply-To header: %w", err)
		}
	}

	if opts.InReplyTo != "" {
		msg.SetGenHeader("In-Reply-To", opts.InReplyTo)
	}

	for _, path := range opts.Attachments {
		msg.AttachFile(path)
	}

	client, err := newSMTPClient(acct)
	if err != nil {
		return fmt.Errorf("create SMTP client: %w", err)
	}

	if err := client.DialAndSend(msg); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	return nil
}

// newSMTPClient builds a go-mail Client from the account's SMTP configuration.
func newSMTPClient(acct EmailAccount) (*mail.Client, error) {
	baseOpts := []mail.Option{
		mail.WithPort(acct.SMTPPort),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(acct.Username),
		mail.WithPassword(acct.Password),
		mail.WithDialContextFunc(safeSMTPDialer(acct)),
	}

	var tlsOpts []mail.Option
	switch acct.SMTPTLS {
	case "ssl":
		tlsOpts = []mail.Option{mail.WithSSL()}
	case "none":
		tlsOpts = []mail.Option{mail.WithTLSPortPolicy(mail.NoTLS)}
	default: // "starttls" or ""
		tlsOpts = []mail.Option{mail.WithTLSPortPolicy(mail.TLSMandatory)}
	}

	return mail.NewClient(acct.SMTPHost, append(baseOpts, tlsOpts...)...)
}

func safeSMTPDialer(acct EmailAccount) mail.DialContextFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		if network != "tcp" {
			return nil, fmt.Errorf("unsupported SMTP network %q", network)
		}
		conn, err := DialPublicTCP(ctx, "smtp", acct.SMTPHost, acct.SMTPPort)
		if err != nil {
			return nil, err
		}
		if acct.SMTPTLS != "ssl" {
			return conn, nil
		}

		tlsConn := tls.Client(conn, &tls.Config{ServerName: acct.SMTPHost, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
}

// FormatDryRun returns a human-readable representation of the email that would
// be sent, for use with --dry-run.
func FormatDryRun(acct EmailAccount, opts SendOptions) string {
	var sb strings.Builder

	from := opts.From
	if from == "" {
		from = acct.From
	}

	contentType := "text/plain"
	if opts.HTML {
		contentType = "text/html"
	}

	fmt.Fprintf(&sb, "From: %s\n", from)
	fmt.Fprintf(&sb, "To: %s\n", strings.Join(opts.To, ", "))
	if len(opts.Cc) > 0 {
		fmt.Fprintf(&sb, "Cc: %s\n", strings.Join(opts.Cc, ", "))
	}
	if len(opts.Bcc) > 0 {
		fmt.Fprintf(&sb, "Bcc: %s\n", strings.Join(opts.Bcc, ", "))
	}
	if opts.ReplyTo != "" {
		fmt.Fprintf(&sb, "Reply-To: %s\n", opts.ReplyTo)
	}
	if opts.InReplyTo != "" {
		fmt.Fprintf(&sb, "In-Reply-To: %s\n", opts.InReplyTo)
	}
	fmt.Fprintf(&sb, "Subject: %s\n", opts.Subject)
	fmt.Fprintf(&sb, "Content-Type: %s\n", contentType)
	if len(opts.Attachments) > 0 {
		fmt.Fprintf(&sb, "Attachments: %s\n", strings.Join(opts.Attachments, ", "))
	}
	sb.WriteString("\n---\n")
	sb.WriteString(opts.Body)

	return sb.String()
}
