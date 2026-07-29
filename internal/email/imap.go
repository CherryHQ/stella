package email

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
)

const maxAttachmentSize = 50 * 1024 * 1024 // 50 MB

// dialIMAP connects to the IMAP server described by acct and logs in.
func dialIMAP(acct EmailAccount) (*imapclient.Client, error) {
	conn, err := dialEmailTCP(context.Background(), "imap", acct.IMAPHost, acct.IMAPPort)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: acct.IMAPHost, NextProtos: []string{"imap"}}
	opts := &imapclient.Options{
		TLSConfig:   tlsConfig,
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	}

	var c *imapclient.Client
	switch acct.IMAPTLS {
	case "starttls":
		c, err = imapclient.NewStartTLS(conn, opts)
	case "none":
		c = imapclient.New(conn, opts)
	default: // "ssl" or ""
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("dial imap %s:%d: %w", acct.IMAPHost, acct.IMAPPort, err)
		}
		c = imapclient.New(tlsConn, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("dial imap %s:%d: %w", acct.IMAPHost, acct.IMAPPort, err)
	}

	if err := c.Login(acct.Username, acct.Password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("imap login: %w", err)
	}
	return c, nil
}

// Folders returns a sorted list of mailbox names available on the server.
func Folders(acct EmailAccount) ([]string, error) {
	c, err := dialIMAP(acct)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	listData, err := c.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("list mailboxes: %w", err)
	}

	names := make([]string, 0, len(listData))
	for _, d := range listData {
		names = append(names, d.Mailbox)
	}
	slices.Sort(names)
	return names, nil
}

// List returns envelope metadata for messages in the given folder, filtered by opts.
func List(acct EmailAccount, opts ListOptions) ([]Envelope, error) {
	c, err := dialIMAP(acct)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	folder := cmp.Or(opts.Folder, "INBOX")
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %q: %w", folder, err)
	}

	// Build search criteria.
	criteria := &imap.SearchCriteria{}
	if opts.Unread {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	}
	if opts.Since != nil {
		criteria.Since = *opts.Since
	}
	if opts.Before != nil {
		criteria.Before = *opts.Before
	}
	if opts.From != "" {
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{
			Key:   "From",
			Value: opts.From,
		})
	}
	if opts.Subject != "" {
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{
			Key:   "Subject",
			Value: opts.Subject,
		})
	}

	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return []Envelope{}, nil
	}

	// Apply limit: take the last N (most recent) UIDs.
	limit := opts.Limit
	if limit <= 0 || limit > len(uids) {
		limit = len(uids)
	}
	uids = uids[len(uids)-limit:]

	fetchOpts := &imap.FetchOptions{
		Envelope:      true,
		Flags:         true,
		RFC822Size:    true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch envelopes: %w", err)
	}

	envelopes := make([]Envelope, 0, len(msgs))
	for _, msg := range msgs {
		env := buildEnvelope(msg)
		envelopes = append(envelopes, env)
	}

	// Sort by date descending (newest first).
	sort.Slice(envelopes, func(i, j int) bool {
		return envelopes[i].Date.After(envelopes[j].Date)
	})
	return envelopes, nil
}

// Read fetches and parses a single message by UID.
func Read(acct EmailAccount, folder string, uid uint32) (*Message, error) {
	c, err := dialIMAP(acct)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	folder = cmp.Or(folder, "INBOX")
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %q: %w", folder, err)
	}

	fetchOpts := &imap.FetchOptions{
		Envelope:      true,
		Flags:         true,
		RFC822Size:    true,
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true}, // full body, peek to avoid marking as \Seen
		},
	}
	msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message uid=%d: %w", uid, err)
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("%w: uid=%d", ErrMessageNotFound, uid)
	}

	buf := msgs[0]
	env := buildEnvelope(buf)

	rawSection := &imap.FetchItemBodySection{Peek: true}
	rawBytes := buf.FindBodySection(rawSection)

	msg := &Message{Envelope: env}
	if len(rawBytes) > 0 {
		mr, err := gomail.CreateReader(bytes.NewReader(rawBytes))
		if err != nil {
			return nil, fmt.Errorf("parse message: %w", err)
		}
		defer func() { _ = mr.Close() }()

		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				// Some parts may be unreadable; skip them.
				slog.Warn("imap: skipping unreadable part", "uid", uid, "err", err)
				continue
			}

			switch h := part.Header.(type) {
			case *gomail.InlineHeader:
				ct, _, _ := h.ContentType()
				body, readErr := io.ReadAll(part.Body)
				if readErr != nil {
					slog.Warn("imap: failed to read inline body", "uid", uid, "content_type", ct, "err", readErr)
					continue
				}
				switch strings.ToLower(ct) {
				case "text/plain":
					if msg.TextBody == "" {
						msg.TextBody = string(body)
					}
				case "text/html":
					if msg.HTMLBody == "" {
						msg.HTMLBody = string(body)
					}
				}
			case *gomail.AttachmentHeader:
				filename, _ := h.Filename()
				ct, _, _ := h.ContentType()
				// Read body to determine size, then discard.
				body, readErr := io.ReadAll(part.Body)
				if readErr != nil {
					slog.Warn("imap: failed to read attachment", "uid", uid, "filename", filename, "err", readErr)
					continue
				}
				msg.Attachments = append(msg.Attachments, AttachmentInfo{
					Filename: filename,
					Size:     int64(len(body)),
					MIMEType: ct,
				})
			}
		}
	}

	return msg, nil
}

// SaveAttachments downloads and saves all attachments of a message to dir.
// It returns the list of file paths written.
func SaveAttachments(acct EmailAccount, folder string, uid uint32, dir string) ([]string, error) {
	c, err := dialIMAP(acct)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	folder = cmp.Or(folder, "INBOX")
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %q: %w", folder, err)
	}

	fetchOpts := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{
			{Peek: true},
		},
	}
	msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch message uid=%d: %w", uid, err)
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("%w: uid=%d", ErrMessageNotFound, uid)
	}

	rawSection := &imap.FetchItemBodySection{Peek: true}
	rawBytes := msgs[0].FindBodySection(rawSection)
	if len(rawBytes) == 0 {
		return nil, nil
	}

	mr, err := gomail.CreateReader(bytes.NewReader(rawBytes))
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	defer func() { _ = mr.Close() }()

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve dir: %w", err)
	}

	var saved []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Warn("imap: skipping unreadable part", "uid", uid, "err", err)
			continue
		}

		ah, ok := part.Header.(*gomail.AttachmentHeader)
		if !ok {
			// Not an attachment; drain and skip.
			_, _ = io.Copy(io.Discard, part.Body)
			continue
		}

		filename, _ := ah.Filename()
		if filename == "" {
			filename = "attachment"
		}
		filename = filepath.Base(filename)

		data, readErr := io.ReadAll(part.Body)
		if readErr != nil {
			slog.Warn("imap: failed to read attachment", "uid", uid, "filename", filename, "err", readErr)
			continue
		}

		if int64(len(data)) > maxAttachmentSize {
			slog.Warn("imap: skipping attachment exceeding 50MB", "uid", uid, "filename", filename, "size", len(data))
			continue
		}

		dest, err := safeDestPath(absDir, filename)
		if err != nil {
			return saved, fmt.Errorf("resolve attachment path %q: %w", filename, err)
		}

		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return saved, fmt.Errorf("write attachment %q: %w", dest, err)
		}
		saved = append(saved, dest)
	}

	return saved, nil
}

// safeDestPath returns a path inside dir for filename, with collision handling
// and path-traversal prevention.
func safeDestPath(absDir, filename string) (string, error) {
	candidate := filepath.Join(absDir, filename)
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absCandidate, absDir+string(filepath.Separator)) &&
		absCandidate != absDir {
		return "", fmt.Errorf("filename %q escapes target directory", filename)
	}

	// Collision avoidance.
	if _, err := os.Stat(absCandidate); os.IsNotExist(err) {
		return absCandidate, nil
	}
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for i := 1; i <= 9999; i++ {
		name := fmt.Sprintf("%s(%d)%s", base, i, ext)
		p := filepath.Join(absDir, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p, nil
		}
	}
	return "", fmt.Errorf("cannot find a free filename for %q", filename)
}

// MarkSeen adds or removes the \Seen flag on a message identified by UID.
// seen=true marks the message as read; seen=false marks it as unread.
func MarkSeen(acct EmailAccount, folder string, uid uint32, seen bool) error {
	c, err := dialIMAP(acct)
	if err != nil {
		return err
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()

	folder = cmp.Or(folder, "INBOX")
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("select %q: %w", folder, err)
	}
	if err := ensureMessageExists(c, uid); err != nil {
		return err
	}

	op := imap.StoreFlagsDel
	if seen {
		op = imap.StoreFlagsAdd
	}
	storeFlags := &imap.StoreFlags{
		Op:     op,
		Silent: true,
		Flags:  []imap.Flag{imap.FlagSeen},
	}
	if _, err := c.Store(imap.UIDSetNum(imap.UID(uid)), storeFlags, nil).Collect(); err != nil {
		return fmt.Errorf("store flags uid=%d: %w", uid, err)
	}
	return nil
}

func ensureMessageExists(c *imapclient.Client, uid uint32) error {
	fetchOpts := &imap.FetchOptions{UID: true}
	msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), fetchOpts).Collect()
	if err != nil {
		return fmt.Errorf("fetch message uid=%d: %w", uid, err)
	}
	if len(msgs) == 0 {
		return fmt.Errorf("%w: uid=%d", ErrMessageNotFound, uid)
	}
	return nil
}

// buildEnvelope converts a FetchMessageBuffer into our Envelope type.
func buildEnvelope(msg *imapclient.FetchMessageBuffer) Envelope {
	env := Envelope{
		UID:  uint32(msg.UID),
		Size: msg.RFC822Size,
	}

	if msg.Envelope != nil {
		e := msg.Envelope
		env.Subject = e.Subject
		env.Date = e.Date
		env.MessageID = e.MessageID

		if len(e.From) > 0 {
			env.From = formatAddress(e.From[0])
			env.FromName = e.From[0].Name
			env.FromAddr = e.From[0].Mailbox + "@" + e.From[0].Host
		}
		for _, addr := range e.To {
			env.To = append(env.To, formatAddress(addr))
		}
	}

	for _, f := range msg.Flags {
		env.Flags = append(env.Flags, string(f))
	}

	if msg.BodyStructure != nil {
		env.HasAttachments = hasAttachments(msg.BodyStructure)
	}

	return env
}

// formatAddress formats an imap.Address as "Name <mailbox@host>" or "mailbox@host".
func formatAddress(addr imap.Address) string {
	email := addr.Mailbox + "@" + addr.Host
	if addr.Name != "" {
		return addr.Name + " <" + email + ">"
	}
	return email
}

// hasAttachments reports whether a BodyStructure contains any attachment part.
func hasAttachments(bs imap.BodyStructure) bool {
	found := false
	bs.Walk(func(_ []int, part imap.BodyStructure) bool {
		if found {
			return false
		}
		disp := part.Disposition()
		if disp != nil && strings.EqualFold(disp.Value, "attachment") {
			found = true
			return false
		}
		return true
	})
	return found
}
