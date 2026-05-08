package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	apiclient "github.com/CherryHQ/stella/api/client"
	"github.com/CherryHQ/stella/internal/email"
	ucli "github.com/urfave/cli/v2"
	"golang.org/x/term"
)

const emailConfigKey = "EMAIL_CONFIG"

// ---------------------------------------------------------------------------
// Top-level command
// ---------------------------------------------------------------------------

func emailCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "email",
		Usage: "Email client",
		Subcommands: []*ucli.Command{
			emailConfigCommand(),
			emailFoldersCommand(),
			emailListCommand(),
			emailReadCommand(),
			emailSendCommand(),
		},
	}
}

// ---------------------------------------------------------------------------
// Vault helpers
// ---------------------------------------------------------------------------

func loadVaultEmailConfig(ctx context.Context, api *apiclient.Client) (*email.Config, error) {
	resp, err := api.GetVaultEntry(ctx, emailConfigKey)
	if err != nil {
		return nil, wrapServerErr(err)
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
	if err := decodeJSON(resp, &envelope); err != nil {
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
	resp, err := api.SetVaultEntry(ctx, emailConfigKey, apiclient.SetVaultEntryJSONRequestBody{
		Value: string(data),
	})
	if err != nil {
		return wrapServerErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return decodeJSON(resp, nil)
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
	api, apiErr := newAPIClient()
	if apiErr != nil {
		return nil, fmt.Errorf("EMAIL_CONFIG env var not set (vault API fallback also unavailable: %v)", apiErr) //nolint:errorlint
	}
	return loadVaultEmailConfig(ctx, api)
}

// ---------------------------------------------------------------------------
// email config
// ---------------------------------------------------------------------------

func emailConfigCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "config",
		Usage: "Manage email account configuration",
		Subcommands: []*ucli.Command{
			emailConfigAddCommand(),
			emailConfigRemoveCommand(),
			emailConfigListCommand(),
			emailConfigShowCommand(),
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
		},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := newAPIClient()
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
				fmt.Fprint(os.Stderr, "Password (leave blank to keep existing): ")
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
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
			fmt.Printf("Account %q saved.\n", name)
			return nil
		},
	}
}

func emailConfigRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove an email account",
		ArgsUsage: "[name]",
		Flags:     []ucli.Flag{configNameFlag},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := newAPIClient()
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
			fmt.Printf("Account %q removed.\n", name)
			return nil
		},
	}
}

func emailConfigListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List configured email accounts",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			api, err := newAPIClient()
			if err != nil {
				return err
			}

			cfg, err := loadVaultEmailConfig(c.Context, api)
			if err != nil {
				return err
			}

			if c.Bool("json") {
				// Mask passwords before printing.
				masked := maskConfigPasswords(cfg)
				return printJSON(masked)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
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

func emailConfigShowCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "show",
		Usage:     "Show details for an email account",
		ArgsUsage: "[name]",
		Flags: []ucli.Flag{
			configNameFlag,
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := newAPIClient()
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

			if c.Bool("json") {
				return printJSON(acct)
			}

			fmt.Printf("Name:      %s\n", name)
			fmt.Printf("IMAP Host: %s\n", acct.IMAPHost)
			fmt.Printf("IMAP Port: %d\n", acct.IMAPPort)
			fmt.Printf("IMAP TLS:  %s\n", acct.IMAPTLS)
			fmt.Printf("SMTP Host: %s\n", acct.SMTPHost)
			fmt.Printf("SMTP Port: %d\n", acct.SMTPPort)
			fmt.Printf("SMTP TLS:  %s\n", acct.SMTPTLS)
			fmt.Printf("Username:  %s\n", acct.Username)
			fmt.Printf("From:      %s\n", acct.From)
			fmt.Printf("Password:  %s\n", acct.Password)
			fmt.Printf("Default:   %v\n", cfg.Default == name)
			return nil
		},
	}
}

func emailConfigDefaultCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "default",
		Usage:     "Set the default email account",
		ArgsUsage: "[name]",
		Flags:     []ucli.Flag{configNameFlag},
		Action: func(c *ucli.Context) error {
			name, err := configAccountName(c)
			if err != nil {
				return err
			}

			api, err := newAPIClient()
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
			fmt.Printf("Default account set to %q.\n", name)
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// Runtime commands (use EMAIL_CONFIG env var)
// ---------------------------------------------------------------------------

func emailFoldersCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "folders",
		Usage: "List available mail folders",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name (uses default if not set)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			cfg, err := loadEmailConfig(c.Context)
			if err != nil {
				return err
			}
			acct, err := cfg.Resolve(c.String("account"))
			if err != nil {
				return err
			}

			folders, err := email.Folders(acct)
			if err != nil {
				return err
			}

			if c.Bool("json") {
				return printJSON(folders)
			}
			for _, f := range folders {
				fmt.Println(f)
			}
			return nil
		},
	}
}

func emailListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List emails",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name"},
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
			cfg, err := loadEmailConfig(c.Context)
			if err != nil {
				return err
			}
			acct, err := cfg.Resolve(c.String("account"))
			if err != nil {
				return err
			}

			opts := email.ListOptions{
				Folder:  c.String("folder"),
				Limit:   c.Int("limit"),
				Unread:  c.Bool("unread"),
				From:    c.String("from"),
				Subject: c.String("subject"),
			}

			if since := c.String("since"); since != "" {
				t, err := time.Parse("2006-01-02", since)
				if err != nil {
					return fmt.Errorf("parse --since: %w", err)
				}
				opts.Since = &t
			}
			if before := c.String("before"); before != "" {
				t, err := time.Parse("2006-01-02", before)
				if err != nil {
					return fmt.Errorf("parse --before: %w", err)
				}
				opts.Before = &t
			}

			msgs, err := email.List(acct, opts)
			if err != nil {
				return err
			}

			if c.Bool("json") {
				return printJSON(msgs)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "UID\tDATE\tFROM\tSUBJECT\tFLAGS")
			for _, m := range msgs {
				subject := m.Subject
				if len(subject) > 80 {
					subject = subject[:80]
				}
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
					m.UID,
					m.Date.Format("2006-01-02 15:04"),
					m.From,
					subject,
					strings.Join(m.Flags, ","),
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
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name"},
			&ucli.StringFlag{Name: "folder", Usage: "Folder name", Value: "INBOX"},
			&ucli.BoolFlag{Name: "raw", Usage: "Print full raw message"},
			&ucli.StringFlag{Name: "save-attachments", Usage: "Directory to save attachments"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			uidStr := c.Args().First()
			if uidStr == "" {
				return fmt.Errorf("uid is required")
			}
			var uid uint32
			if _, err := fmt.Sscan(uidStr, &uid); err != nil {
				return fmt.Errorf("invalid uid %q: %w", uidStr, err)
			}

			cfg, err := loadEmailConfig(c.Context)
			if err != nil {
				return err
			}
			acct, err := cfg.Resolve(c.String("account"))
			if err != nil {
				return err
			}

			folder := c.String("folder")

			if dir := c.String("save-attachments"); dir != "" {
				paths, err := email.SaveAttachments(acct, folder, uid, dir)
				if err != nil {
					return err
				}
				for _, p := range paths {
					fmt.Println(p)
				}
				return nil
			}

			msg, err := email.Read(acct, folder, uid)
			if err != nil {
				return err
			}

			if c.Bool("json") {
				return printJSON(msg)
			}

			if c.Bool("raw") {
				fmt.Printf("UID:     %d\n", msg.UID)
				fmt.Printf("From:    %s\n", msg.From)
				fmt.Printf("To:      %s\n", strings.Join(msg.To, ", "))
				fmt.Printf("Date:    %s\n", msg.Date.Format(time.RFC1123Z))
				fmt.Printf("Subject: %s\n", msg.Subject)
				fmt.Printf("Flags:   %s\n", strings.Join(msg.Flags, ", "))
				fmt.Println("---")
				fmt.Println(msg.TextBody)
				if msg.HTMLBody != "" {
					fmt.Println("--- HTML ---")
					fmt.Println(msg.HTMLBody)
				}
				return nil
			}

			// Default formatted output.
			fmt.Printf("From:    %s\n", msg.From)
			fmt.Printf("To:      %s\n", strings.Join(msg.To, ", "))
			fmt.Printf("Date:    %s\n", msg.Date.Format(time.RFC1123Z))
			fmt.Printf("Subject: %s\n", msg.Subject)
			fmt.Println()
			body := msg.TextBody
			if body == "" {
				body = msg.HTMLBody
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
			&ucli.StringFlag{Name: "account", Aliases: []string{"a"}, Usage: "Account name"},
			&ucli.StringSliceFlag{Name: "to", Aliases: []string{"t"}, Usage: "Recipient(s)"},
			&ucli.StringSliceFlag{Name: "cc", Usage: "CC recipient(s)"},
			&ucli.StringSliceFlag{Name: "bcc", Usage: "BCC recipient(s)"},
			&ucli.StringFlag{Name: "subject", Aliases: []string{"s"}, Usage: "Subject (required)", Required: true},
			&ucli.StringFlag{Name: "body", Aliases: []string{"b"}, Usage: "Message body"},
			&ucli.StringFlag{Name: "body-file", Aliases: []string{"f"}, Usage: "File containing message body"},
			&ucli.BoolFlag{Name: "html", Usage: "Send as HTML"},
			&ucli.StringSliceFlag{Name: "attach", Usage: "Attachment file path(s)"},
			&ucli.StringFlag{Name: "from", Usage: "Override From address"},
			&ucli.StringFlag{Name: "reply-to", Usage: "Reply-To address"},
			&ucli.BoolFlag{Name: "dry-run", Usage: "Print composed message without sending"},
		},
		Action: func(c *ucli.Context) error {
			cfg, err := loadEmailConfig(c.Context)
			if err != nil {
				return err
			}
			acct, err := cfg.Resolve(c.String("account"))
			if err != nil {
				return err
			}

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
			}

			if c.Bool("dry-run") {
				fmt.Println(email.FormatDryRun(acct, opts))
				return nil
			}

			if err := email.Send(acct, opts); err != nil {
				return err
			}
			fmt.Println("Email sent successfully.")
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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
