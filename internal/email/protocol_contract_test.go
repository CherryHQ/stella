package email

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	fakeEmailUsername = "stella@example.com"
	fakeEmailPassword = "contract-secret"
)

// TestFakeIMAPSMTPLifecycle exercises Stella's actual wire clients against
// deterministic protocol servers. Service/vault scope and idempotency remain
// covered by service_test.go; this contract owns folder/list/read/flag/send.
func TestFakeIMAPSMTPLifecycle(t *testing.T) {
	imapAddr, uid := startFakeIMAP(t)
	smtp := &fakeSMTP{}

	previousDial := dialEmailTCP
	dialEmailTCP = func(ctx context.Context, kind, _ string, _ int) (net.Conn, error) {
		switch kind {
		case "imap":
			var dialer net.Dialer
			return dialer.DialContext(ctx, "tcp", imapAddr)
		case "smtp":
			return smtp.dial(), nil
		default:
			return nil, fmt.Errorf("unexpected email protocol %q", kind)
		}
	}
	t.Cleanup(func() { dialEmailTCP = previousDial })

	acct := EmailAccount{
		IMAPHost: "imap.contract.invalid",
		IMAPPort: 143,
		IMAPTLS:  "none",
		// localhost lets net/smtp exercise AUTH without TLS; the dial seam still
		// routes the connection into the in-memory protocol server.
		SMTPHost: "localhost",
		SMTPPort: 25,
		SMTPTLS:  "none",
		Username: fakeEmailUsername,
		Password: fakeEmailPassword,
		From:     fakeEmailUsername,
	}

	folders, err := Folders(acct)
	if err != nil {
		t.Fatalf("Folders: %v", err)
	}
	if len(folders) != 1 || folders[0] != "INBOX" {
		t.Fatalf("folders = %v, want [INBOX]", folders)
	}

	envelopes, err := List(acct, ListOptions{Folder: "INBOX", Unread: true, Subject: "Release contract"})
	if err != nil {
		t.Fatalf("List unread: %v", err)
	}
	if len(envelopes) != 1 || envelopes[0].UID != uid || envelopes[0].Subject != "Release contract" {
		t.Fatalf("envelopes = %+v, want seeded unread message UID %d", envelopes, uid)
	}

	message, err := Read(acct, "INBOX", uid)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if message.Subject != "Release contract" || !strings.Contains(message.TextBody, "deterministic IMAP body") {
		t.Fatalf("message = %+v, want seeded subject/body", message)
	}

	if err := MarkSeen(acct, "INBOX", uid, true); err != nil {
		t.Fatalf("MarkSeen(true): %v", err)
	}
	unread, err := List(acct, ListOptions{Folder: "INBOX", Unread: true})
	if err != nil {
		t.Fatalf("List after MarkSeen(true): %v", err)
	}
	if len(unread) != 0 {
		t.Fatalf("unread after MarkSeen(true) = %+v, want none", unread)
	}
	if err := MarkSeen(acct, "INBOX", uid, false); err != nil {
		t.Fatalf("MarkSeen(false): %v", err)
	}

	err = Send(acct, SendOptions{
		To:      []string{"receiver@example.com"},
		Subject: "SMTP release contract",
		Body:    "deterministic SMTP body",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	raw, err := smtp.message()
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"From: <" + fakeEmailUsername + ">",
		"To: <receiver@example.com>",
		"Subject: SMTP release contract",
		"deterministic SMTP body",
	} {
		if !strings.Contains(raw, marker) {
			t.Errorf("SMTP message missing %q:\n%s", marker, raw)
		}
	}
}

// startFakeIMAP starts go-imap's in-memory server and seeds one message through
// the same IMAP APPEND protocol a real mailbox uses.
func startFakeIMAP(t *testing.T) (string, uint32) {
	t.Helper()

	memoryServer := imapmemserver.New()
	user := imapmemserver.NewUser(fakeEmailUsername, fakeEmailPassword)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	memoryServer.AddUser(user)

	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memoryServer.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIMAP4rev2: {},
		},
		Logger: log.New(io.Discard, "", 0),
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen IMAP: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serveErr; err != nil {
			t.Errorf("IMAP server: %v", err)
		}
	})

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial seed IMAP: %v", err)
	}
	client := imapclient.New(conn, nil)
	if err := client.Login(fakeEmailUsername, fakeEmailPassword).Wait(); err != nil {
		t.Fatalf("seed IMAP login: %v", err)
	}
	raw := strings.Join([]string{
		"From: Sender <sender@example.com>",
		"To: Stella <stella@example.com>",
		"Subject: Release contract",
		"Date: Tue, 28 Jul 2026 12:00:00 +0000",
		"Message-ID: <release-contract@example.com>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"deterministic IMAP body",
		"",
	}, "\r\n")
	appendCommand := client.Append("INBOX", int64(len(raw)), &imap.AppendOptions{Time: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)})
	if _, err := appendCommand.Write([]byte(raw)); err != nil {
		t.Fatalf("seed IMAP write: %v", err)
	}
	if err := appendCommand.Close(); err != nil {
		t.Fatalf("seed IMAP close: %v", err)
	}
	appended, err := appendCommand.Wait()
	if err != nil {
		t.Fatalf("seed IMAP append: %v", err)
	}
	if err := client.Logout().Wait(); err != nil {
		t.Fatalf("seed IMAP logout: %v", err)
	}
	_ = client.Close()
	return listener.Addr().String(), uint32(appended.UID)
}

// fakeSMTP implements the bounded SMTP subset used by go-mail. It records the
// DATA payload so the contract can assert the serialized message.
type fakeSMTP struct {
	mu       sync.Mutex
	messages []string
	errs     []error
}

func (s *fakeSMTP) dial() net.Conn {
	client, server := net.Pipe()
	go func() {
		if err := s.serve(server); err != nil {
			s.mu.Lock()
			s.errs = append(s.errs, err)
			s.mu.Unlock()
		}
	}()
	return client
}

func (s *fakeSMTP) message() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errs) > 0 {
		return "", s.errs[0]
	}
	if len(s.messages) != 1 {
		return "", fmt.Errorf("SMTP captured %d messages, want 1", len(s.messages))
	}
	return s.messages[0], nil
}

func (s *fakeSMTP) serve(conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "220 contract.invalid ESMTP ready\r\n"); err != nil {
		return err
	}

	scanner := bufio.NewScanner(conn)
	var data strings.Builder
	inData := false
	awaitingAuth := false
	for scanner.Scan() {
		line := scanner.Text()
		if inData {
			if line == "." {
				s.mu.Lock()
				s.messages = append(s.messages, data.String())
				s.mu.Unlock()
				inData = false
				if _, err := io.WriteString(conn, "250 2.0.0 queued\r\n"); err != nil {
					return err
				}
				continue
			}
			data.WriteString(strings.TrimPrefix(line, "."))
			data.WriteString("\r\n")
			continue
		}
		if awaitingAuth {
			awaitingAuth = false
			if _, err := io.WriteString(conn, "235 2.7.0 authenticated\r\n"); err != nil {
				return err
			}
			continue
		}

		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO "):
			if _, err := io.WriteString(conn, "250-contract.invalid\r\n250-AUTH PLAIN\r\n250 8BITMIME\r\n"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "HELO "):
			if _, err := io.WriteString(conn, "250 contract.invalid\r\n"); err != nil {
				return err
			}
		case upper == "AUTH PLAIN":
			awaitingAuth = true
			if _, err := io.WriteString(conn, "334 \r\n"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "AUTH PLAIN "):
			if _, err := io.WriteString(conn, "235 2.7.0 authenticated\r\n"); err != nil {
				return err
			}
		case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"), upper == "RSET", upper == "NOOP":
			if _, err := io.WriteString(conn, "250 2.1.0 ok\r\n"); err != nil {
				return err
			}
		case upper == "DATA":
			inData = true
			data.Reset()
			if _, err := io.WriteString(conn, "354 end with <CRLF>.<CRLF>\r\n"); err != nil {
				return err
			}
		case upper == "QUIT":
			_, err := io.WriteString(conn, "221 2.0.0 bye\r\n")
			return err
		default:
			return fmt.Errorf("unexpected SMTP command %q", line)
		}
	}
	return scanner.Err()
}
