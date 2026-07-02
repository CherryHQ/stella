package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	ucli "github.com/urfave/cli/v2"
	"golang.org/x/term"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/email"
)

const emailConfigKey = "EMAIL_CONFIG"

func emailCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "email",
		Usage:    "Read, send, and manage email through configured IMAP/SMTP accounts",
		Category: "Feature",
		Description: `Read, send, and manage email through IMAP/SMTP accounts configured in
the vault. Use "email config" to add an account, then browse folders,
read messages, and send mail directly from the terminal.`,
		Subcommands: []*ucli.Command{
			emailConfigCommand(),
			emailFoldersCommand(),
			emailListCommand(),
			emailReadCommand(),
			emailSendCommand(),
			emailMarkCommand(),
		},
	}
}

func loadVaultEmailConfig(ctx context.Context, api *apiclient.Client) (*email.Config, error) {
	scope := apiclient.GetScopedVaultEntryParamsScope("user")
	resp, err := api.GetScopedVaultEntry(ctx, emailConfigKey, &apiclient.GetScopedVaultEntryParams{Scope: &scope})
	if err != nil {
		return nil, apiclient.WrapServerErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode == http.StatusNotFound {
		return &email.Config{Accounts: make(map[string]email.EmailAccount)}, nil
	}

	var envelope struct {
		Data struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"data"`
	}
	if err := apiclient.DecodeJSON(resp, &envelope); err != nil {
		return nil, err
	}

	cfg := &email.Config{Accounts: make(map[string]email.EmailAccount)}
	if envelope.Data.Value == "" || envelope.Data.Value == "{}" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(envelope.Data.Value), cfg); err != nil {
		return nil, fmt.Errorf("parse EMAIL_CONFIG from vault: %w", err)
	}
	if cfg.Accounts == nil {
		cfg.Accounts = make(map[string]email.EmailAccount)
	}
	return cfg, nil
}

func saveVaultEmailConfig(ctx context.Context, api *apiclient.Client, cfg *email.Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal email config: %w", err)
	}
	scope := apitypes.SetVaultEntryRequestScopeUser
	resp, err := api.SetScopedVaultEntry(ctx, emailConfigKey, apiclient.SetScopedVaultEntryJSONRequestBody{
		Value: string(data),
		Scope: &scope,
	})
	if err != nil {
		return apiclient.WrapServerErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return apiclient.DecodeJSON(resp, nil)
}

// loadEmailConfig tries EMAIL_CONFIG env var first (fast, no server needed).
// If the env var is not set but STELLA_TOKEN is available, falls back to the
// vault HTTP API so that freshly-added accounts work in the same session
// without a sandbox restart.
func loadEmailConfig(ctx context.Context) (*email.Config, error) {
	cfg, err := email.LoadFromEnv()
	if err == nil {
		return cfg, nil
	}
	api, apiErr := apiclient.NewAPIClient()
	if apiErr != nil {
		return nil, fmt.Errorf("EMAIL_CONFIG env var not set (vault API fallback also unavailable: %v)", apiErr) //nolint:errorlint
	}
	return loadVaultEmailConfig(ctx, api)
}

func emailConfigCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "config",
		Usage: "Manage email account configuration",
		Subcommands: []*ucli.Command{
			emailConfigAddCommand(),
			emailConfigRemoveCommand(),
			emailConfigListCommand(),
			emailConfigGetCommand(),
			emailConfigDefaultCommand(),
		},
	}
}

func configAccountName(c *ucli.Context) (string, error) {
	name := c.String("name")
	if name == "" {
		name = c.Args().First()
	}
	if name == "" {
		return "", fmt.Errorf("account name is required (positional arg or --name)")
	}
	if err := email.ValidateAccountName(name); err != nil {
		return "", err
	}
	return name, nil
}

var configNameFlag = &ucli.StringFlag{Name: "name", Aliases: []string{"n"}, Usage: "Account name (lowercase, digits, underscores only)"}

func emailConfigAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "add",
		Usage:     "Add or update an email account",
		ArgsUsage: "[name]",
		Flags: []ucli.Flag{
			configNameFlag,
			&ucli.StringFlag{Name: "imap-host", Usage: "IMAP host"},
			&ucli.IntFlag{Name: "imap-port", Usage: "IMAP port", Value: 0},
			&ucli.StringFlag{Name: "imap-tls", Usage: "IMAP TLS mode: ssl, starttls, none"},
			&ucli.StringFlag{Name: "smtp-host", Usage: "SMTP host"},
			&ucli.IntFlag{Name: "smtp-port", Usage: "SMTP port", Value: 0},
			&ucli.StringFlag{Name: "smtp-tls", Usage: "SMTP TLS mode: ssl, starttls, none"},
			&ucli.StringFlag{Name: "username", Usage: "Account username"},
			&ucli.StringFlag{Name: "from", Usage: "From address"},
			&ucli.BoolFlag{Name: "password-stdin", Usage: "Read password from stdin"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := apiclient.NewAPIClient()
			if err != nil {
				return err
			}

			cfg, err := loadVaultEmailConfig(c.Context, api)
			if err != nil {
				return err
			}

			_, exists := cfg.Accounts[name]
			if !exists {
				// New account — require core fields.
				var missing []string
				if c.String("imap-host") == "" {
					missing = append(missing, "--imap-host")
				}
				if c.String("smtp-host") == "" {
					missing = append(missing, "--smtp-host")
				}
				if c.String("username") == "" {
					missing = append(missing, "--username")
				}
				if c.String("from") == "" {
					missing = append(missing, "--from")
				}
				if len(missing) > 0 {
					return fmt.Errorf("required flags for new account: %s", strings.Join(missing, ", "))
				}
			}

			// Resolve password.
			var password string
			if c.Bool("password-stdin") {
				scanner := bufio.NewScanner(os.Stdin)
				if scanner.Scan() {
					password = strings.TrimRight(scanner.Text(), "\r\n")
				}
				if err := scanner.Err(); err != nil {
					return fmt.Errorf("read password from stdin: %w", err)
				}
			} else if term.IsTerminal(int(os.Stdin.Fd())) {
				e := cli.Stderr(c)
				e.Printf("Password (leave blank to keep existing): ")
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				e.Println()
				if err != nil {
					return fmt.Errorf("read password: %w", err)
				}
				password = string(pw)
			}

			partial := email.EmailAccount{
				IMAPHost: c.String("imap-host"),
				IMAPPort: c.Int("imap-port"),
				IMAPTLS:  c.String("imap-tls"),
				SMTPHost: c.String("smtp-host"),
				SMTPPort: c.Int("smtp-port"),
				SMTPTLS:  c.String("smtp-tls"),
				Username: c.String("username"),
				From:     c.String("from"),
				Password: password,
			}
			cfg.Upsert(name, partial)

			// Auto-set default if this is the first account.
			if cfg.Default == "" && len(cfg.Accounts) == 1 {
				_ = cfg.SetDefault(name)
			}

			if err := saveVaultEmailConfig(c.Context, api, cfg); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, map[string]any{"name": name, "saved": true})
			}
			o := cli.Stdout(c)
			o.Printf("Account %q saved.\n", name)
			return o.Err()
		},
	}
}

func emailConfigRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove an email account",
		ArgsUsage: "[name]",
		Flags:     []ucli.Flag{configNameFlag, cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := apiclient.NewAPIClient()
			if err != nil {
				return err
			}

			cfg, err := loadVaultEmailConfig(c.Context, api)
			if err != nil {
				return err
			}

			if err := cfg.Remove(name); err != nil {
				return err
			}
			if err := saveVaultEmailConfig(c.Context, api, cfg); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintDeleted(c, name)
			}
			o := cli.Stdout(c)
			o.Printf("Account %q removed.\n", name)
			return o.Err()
		},
	}
}

func emailConfigListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List configured email accounts",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			api, err := apiclient.NewAPIClient()
			if err != nil {
				return err
			}

			cfg, err := loadVaultEmailConfig(c.Context, api)
			if err != nil {
				return err
			}

			if cli.IsJSON(c) {
				// Mask passwords before printing.
				masked := maskConfigPasswords(cfg)
				return cli.PrintJSON(c, masked)
			}

			w := tabwriter.NewWriter(c.App.Writer, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tIMAP HOST\tSMTP HOST\tUSERNAME\tFROM\tDEFAULT")
			for _, name := range cfg.AccountNames() {
				acct := cfg.Accounts[name]
				isDefault := ""
				if cfg.Default == name {
					isDefault = "yes"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					name, acct.IMAPHost, acct.SMTPHost, acct.Username, acct.From, isDefault)
			}
			return w.Flush()
		},
	}
}

func emailConfigGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show details for an email account",
		ArgsUsage: "[name]",
		Flags: []ucli.Flag{
			configNameFlag,
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := apiclient.NewAPIClient()
			if err != nil {
				return err
			}

			cfg, err := loadVaultEmailConfig(c.Context, api)
			if err != nil {
				return err
			}

			acct, ok := cfg.Accounts[name]
			if !ok {
				return fmt.Errorf("account %q not found", name)
			}
			acct.Password = "****"

			if cli.IsJSON(c) {
				return cli.PrintJSON(c, acct)
			}

			o := cli.Stdout(c)
			o.Printf("Name:      %s\n", name)
			o.Printf("IMAP Host: %s\n", acct.IMAPHost)
			o.Printf("IMAP Port: %d\n", acct.IMAPPort)
			o.Printf("IMAP TLS:  %s\n", acct.IMAPTLS)
			o.Printf("SMTP Host: %s\n", acct.SMTPHost)
			o.Printf("SMTP Port: %d\n", acct.SMTPPort)
			o.Printf("SMTP TLS:  %s\n", acct.SMTPTLS)
			o.Printf("Username:  %s\n", acct.Username)
			o.Printf("From:      %s\n", acct.From)
			o.Printf("Password:  %s\n", acct.Password)
			o.Printf("Default:   %v\n", cfg.Default == name)
			return o.Err()
		},
	}
}

func emailConfigDefaultCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "default",
		Usage:     "Set the default email account",
		ArgsUsage: "[name]",
		Flags:     []ucli.Flag{configNameFlag, cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := apiclient.NewAPIClient()
			if err != nil {
				return err
			}

			cfg, err := loadVaultEmailConfig(c.Context, api)
			if err != nil {
				return err
			}

			if err := cfg.SetDefault(name); err != nil {
				return err
			}
			if err := saveVaultEmailConfig(c.Context, api, cfg); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, map[string]any{"name": name, "default": true})
			}
			o := cli.Stdout(c)
			o.Printf("Default account set to %q.\n", name)
			return o.Err()
		},
	}
}

func emailFoldersCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "folders",
		Usage: "List available mail folders",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name (uses default if not set)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			accountPtr := optStr(c, "account")
			result, err := apiclient.Call[apiclient.EmailFolderList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListEmailFolders(c.Context, &apiclient.ListEmailFoldersParams{Account: accountPtr})
			})
			if err != nil {
				return err
			}

			if c.Bool("json") {
				return cli.PrintJSON(c, result.Folders)
			}
			for _, f := range result.Folders {
				fmt.Println(f)
			}
			return nil
		},
	}
}

func emailListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List emails in a mailbox folder",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name (uses default if not set)"},
			&ucli.StringFlag{Name: "folder", Usage: "Folder name", Value: "INBOX"},
			&ucli.IntFlag{Name: "limit", Aliases: []string{"n"}, Usage: "Maximum number of messages", Value: 20},
			&ucli.BoolFlag{Name: "unread", Aliases: []string{"u"}, Usage: "Show only unread messages"},
			&ucli.StringFlag{Name: "from", Usage: "Filter by sender"},
			&ucli.StringFlag{Name: "subject", Aliases: []string{"s"}, Usage: "Filter by subject"},
			&ucli.StringFlag{Name: "since", Usage: "Show messages since date (YYYY-MM-DD)"},
			&ucli.StringFlag{Name: "before", Usage: "Show messages before date (YYYY-MM-DD)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			params := &apiclient.ListEmailMessagesParams{
				Account: optStr(c, "account"),
				Folder:  optStr(c, "folder"),
				From:    optStr(c, "from"),
				Subject: optStr(c, "subject"),
			}
			if limit := c.Int("limit"); limit != 0 {
				params.Limit = &limit
			}
			if c.Bool("unread") {
				unread := true
				params.Unread = &unread
			}

			if since := c.String("since"); since != "" {
				t, err := time.Parse("2006-01-02", since)
				if err != nil {
					return fmt.Errorf("parse --since: %w", err)
				}
				params.Since = &openapi_types.Date{Time: t}
			}
			if before := c.String("before"); before != "" {
				t, err := time.Parse("2006-01-02", before)
				if err != nil {
					return fmt.Errorf("parse --before: %w", err)
				}
				params.Before = &openapi_types.Date{Time: t}
			}

			result, err := apiclient.Call[apiclient.EmailEnvelopeList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListEmailMessages(c.Context, params)
			})
			if err != nil {
				return err
			}

			if c.Bool("json") {
				return cli.PrintJSON(c, result.Messages)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "UID\tDATE\tFROM\tSUBJECT\tFLAGS")
			for _, m := range result.Messages {
				subject := m.Subject
				if len(subject) > 80 {
					subject = subject[:80]
				}
				var flags string
				if m.Flags != nil {
					flags = strings.Join(*m.Flags, ",")
				}
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
					m.Uid,
					m.Date.Format("2006-01-02 15:04"),
					m.From,
					subject,
					flags,
				)
			}
			return w.Flush()
		},
	}
}

func emailReadCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "read",
		Usage:     "Read an email by UID",
		ArgsUsage: "<uid>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name (uses default if not set)"},
			&ucli.StringFlag{Name: "folder", Usage: "Folder name", Value: "INBOX"},
			&ucli.BoolFlag{Name: "raw", Usage: "Print full raw message"},
			&ucli.StringFlag{Name: "save-attachments", Usage: "Directory to save attachments (connects to IMAP directly from this host, not through the daemon; requires local network access to the mail server)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			uidStr := c.Args().First()
			if uidStr == "" {
				return fmt.Errorf("uid is required")
			}
			uid, imapUID, err := parseEmailUID(uidStr)
			if err != nil {
				return err
			}

			if dir := c.String("save-attachments"); dir != "" {
				// Attachment download is not exposed over the daemon API yet, so
				// this path talks IMAP directly. It only works when the CLI host
				// can reach the mail server; a remote CLI pointed at a remote
				// daemon will fail here. See the flag help for the caveat.
				cfg, err := loadEmailConfig(c.Context)
				if err != nil {
					return err
				}
				acct, err := cfg.Resolve(c.String("account"))
				if err != nil {
					return err
				}
				paths, err := email.SaveAttachments(acct, c.String("folder"), imapUID, dir)
				if err != nil {
					return err
				}
				for _, p := range paths {
					fmt.Println(p)
				}
				return nil
			}

			msg, err := apiclient.Call[apiclient.EmailMessage](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetEmailMessage(c.Context, uid, &apiclient.GetEmailMessageParams{
					Account: optStr(c, "account"),
					Folder:  optStr(c, "folder"),
				})
			})
			if err != nil {
				return err
			}

			if c.Bool("json") {
				return cli.PrintJSON(c, msg)
			}

			to := derefStrSlice(msg.To)
			flags := derefStrSlice(msg.Flags)
			textBody := derefStr(msg.TextBody)
			htmlBody := derefStr(msg.HtmlBody)

			if c.Bool("raw") {
				fmt.Printf("UID:     %d\n", msg.Uid)
				fmt.Printf("From:    %s\n", msg.From)
				fmt.Printf("To:      %s\n", strings.Join(to, ", "))
				fmt.Printf("Date:    %s\n", msg.Date.Format(time.RFC1123Z))
				fmt.Printf("Subject: %s\n", msg.Subject)
				fmt.Printf("Flags:   %s\n", strings.Join(flags, ", "))
				fmt.Println("---")
				fmt.Println(textBody)
				if htmlBody != "" {
					fmt.Println("--- HTML ---")
					fmt.Println(htmlBody)
				}
				return nil
			}

			// Default formatted output.
			fmt.Printf("From:    %s\n", msg.From)
			fmt.Printf("To:      %s\n", strings.Join(to, ", "))
			fmt.Printf("Date:    %s\n", msg.Date.Format(time.RFC1123Z))
			fmt.Printf("Subject: %s\n", msg.Subject)
			fmt.Println()
			body := textBody
			if body == "" {
				body = htmlBody
			}
			fmt.Println(body)
			return nil
		},
	}
}

func emailSendCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "send",
		Usage: "Send an email",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name (uses default if not set)"},
			&ucli.StringSliceFlag{Name: "to", Aliases: []string{"t"}, Usage: "Recipient(s)", Required: true},
			&ucli.StringSliceFlag{Name: "cc", Usage: "CC recipient(s)"},
			&ucli.StringSliceFlag{Name: "bcc", Usage: "BCC recipient(s)"},
			&ucli.StringFlag{Name: "subject", Aliases: []string{"s"}, Usage: "Subject (required)", Required: true},
			&ucli.StringFlag{Name: "body", Aliases: []string{"b"}, Usage: "Message body"},
			&ucli.StringFlag{Name: "body-file", Aliases: []string{"f"}, Usage: "File containing message body"},
			&ucli.BoolFlag{Name: "html", Usage: "Send as HTML"},
			&ucli.StringSliceFlag{Name: "attach", Usage: "Attachment file path(s) (sent via direct SMTP from this host, not through the daemon; requires local network access to the mail server)"},
			&ucli.StringFlag{Name: "from", Usage: "Override From address"},
			&ucli.StringFlag{Name: "reply-to", Usage: "Reply-To address"},
			&ucli.StringFlag{Name: "in-reply-to", Usage: "Message-ID of the message being replied to (sets In-Reply-To header)"},
			&ucli.BoolFlag{Name: "dry-run", Usage: "Print composed message without sending"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			// Resolve body.
			var body string
			switch {
			case c.String("body") != "":
				body = c.String("body")
			case c.String("body-file") != "":
				data, err := os.ReadFile(c.String("body-file"))
				if err != nil {
					return fmt.Errorf("read body file: %w", err)
				}
				body = string(data)
			default:
				if term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("no body provided: use --body, --body-file, or pipe input via stdin")
				}
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				body = string(data)
			}

			opts := email.SendOptions{
				To:          c.StringSlice("to"),
				Cc:          c.StringSlice("cc"),
				Bcc:         c.StringSlice("bcc"),
				Subject:     c.String("subject"),
				Body:        body,
				HTML:        c.Bool("html"),
				Attachments: c.StringSlice("attach"),
				From:        c.String("from"),
				ReplyTo:     c.String("reply-to"),
				InReplyTo:   c.String("in-reply-to"),
			}

			if c.Bool("dry-run") {
				if cli.IsJSON(c) {
					return cli.PrintJSON(c, map[string]any{
						"dry_run": true,
						"to":      opts.To,
						"cc":      opts.Cc,
						"bcc":     opts.Bcc,
						"subject": opts.Subject,
					})
				}
				// Dry-run needs account config for the From address; load from vault.
				cfg, err := loadEmailConfig(c.Context)
				if err != nil {
					return err
				}
				acct, err := cfg.Resolve(c.String("account"))
				if err != nil {
					return err
				}
				o := cli.Stdout(c)
				o.Println(email.FormatDryRun(acct, opts))
				return o.Err()
			}

			reqBody := apiclient.SendEmailJSONRequestBody{
				To:      opts.To,
				Subject: opts.Subject,
				Body:    opts.Body,
			}
			if len(opts.Cc) > 0 {
				reqBody.Cc = &opts.Cc
			}
			if len(opts.Bcc) > 0 {
				reqBody.Bcc = &opts.Bcc
			}
			if opts.HTML {
				reqBody.Html = &opts.HTML
			}
			if opts.From != "" {
				reqBody.From = &opts.From
			}
			if opts.ReplyTo != "" {
				reqBody.ReplyTo = &opts.ReplyTo
			}
			if opts.InReplyTo != "" {
				reqBody.InReplyTo = &opts.InReplyTo
			}

			if len(opts.Attachments) > 0 {
				// Attachments are not supported over the daemon API yet, so this
				// path sends via direct SMTP from the CLI host. It only works
				// when the CLI can reach the mail server; a remote CLI pointed at
				// a remote daemon will fail here. See the flag help for the caveat.
				cfg, err := loadEmailConfig(c.Context)
				if err != nil {
					return err
				}
				acct, err := cfg.Resolve(c.String("account"))
				if err != nil {
					return err
				}
				if err := email.Send(acct, opts); err != nil {
					return err
				}
				if cli.IsJSON(c) {
					return cli.PrintJSON(c, map[string]any{"sent": true, "to": opts.To, "subject": opts.Subject})
				}
				o := cli.Stdout(c)
				o.Println("Email sent successfully.")
				return o.Err()
			}

			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.SendEmail(c.Context, &apiclient.SendEmailParams{Account: optStr(c, "account")}, reqBody)
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, map[string]any{"sent": true, "to": opts.To, "subject": opts.Subject})
			}
			o := cli.Stdout(c)
			o.Println("Email sent successfully.")
			return o.Err()
		},
	}
}

func emailMarkCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "mark",
		Usage:     "Mark a message as read or unread",
		ArgsUsage: "<uid>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name (uses default if not set)"},
			&ucli.StringFlag{Name: "folder", Usage: "Folder name", Value: "INBOX"},
			&ucli.BoolFlag{Name: "seen", Usage: "Mark message as read"},
			&ucli.BoolFlag{Name: "unseen", Usage: "Mark message as unread"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			uidStr := c.Args().First()
			if uidStr == "" {
				return fmt.Errorf("uid is required")
			}
			uid, _, err := parseEmailUID(uidStr)
			if err != nil {
				return err
			}
			if c.Bool("seen") == c.Bool("unseen") {
				return fmt.Errorf("exactly one of --seen or --unseen is required")
			}

			seen := c.Bool("seen")
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.MarkEmailMessage(c.Context, uid, &apiclient.MarkEmailMessageParams{
					Account: optStr(c, "account"),
					Folder:  optStr(c, "folder"),
				}, apiclient.MarkEmailMessageJSONRequestBody{Seen: seen})
			}); err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, map[string]any{"uid": uid, "seen": seen})
			}
			o := cli.Stdout(c)
			if seen {
				o.Printf("Message %d marked as read.\n", uid)
			} else {
				o.Printf("Message %d marked as unread.\n", uid)
			}
			return o.Err()
		},
	}
}

func parseEmailUID(value string) (int, uint32, error) {
	uid, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid uid %q: %w", value, err)
	}
	const maxIMAPUID = int64(^uint32(0))
	if uid < 1 || uid > maxIMAPUID {
		return 0, 0, fmt.Errorf("uid must be between 1 and 4294967295")
	}
	return int(uid), uint32(uid), nil
}

// optStr returns a pointer to the CLI flag value if non-empty, else nil.
func optStr(c *ucli.Context, name string) *string {
	if v := c.String(name); v != "" {
		return &v
	}
	return nil
}

// derefStr returns the string pointed to, or "" if nil.
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// derefStrSlice returns the slice pointed to, or nil if the pointer is nil.
func derefStrSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// maskConfigPasswords returns a copy of cfg with passwords replaced by "****".
func maskConfigPasswords(cfg *email.Config) map[string]any {
	accounts := make(map[string]any, len(cfg.Accounts))
	for name, acct := range cfg.Accounts {
		masked := acct
		if masked.Password != "" {
			masked.Password = "****"
		}
		accounts[name] = masked
	}
	return map[string]any{
		"default":  cfg.Default,
		"accounts": accounts,
	}
}
