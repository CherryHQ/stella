package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastschema/qjs"
	"github.com/vaayne/anna/internal/config"
)

const (
	fetchDefaultTimeout = 30 * time.Second
	fetchMaxTimeout     = 60 * time.Second
	fetchMaxBodySize    = 1 << 20 // 1 MB
)

// hostAPI holds state for JS host API implementations.
type hostAPI struct {
	logger    *slog.Logger
	pluginDir string
}

// registerHostAPIs sets log, readFile, writeFile, and fetch on the anna object.
func registerHostAPIs(ctx *qjs.Context, anna *qjs.Value, ha *hostAPI) {
	// anna.log(level, msg)
	anna.SetPropertyStr("log", ctx.Function(func(this *qjs.This) (*qjs.Value, error) {
		args := this.Args()
		if len(args) < 2 {
			return nil, errors.New("anna.log requires (level, msg)")
		}
		level := args[0].String()
		msg := args[1].String()
		switch strings.ToLower(level) {
		case "debug":
			ha.logger.Debug(msg)
		case "warn", "warning":
			ha.logger.Warn(msg)
		case "error":
			ha.logger.Error(msg)
		default:
			ha.logger.Info(msg)
		}
		return this.Context().NewUndefined(), nil
	}))

	// anna.readFile(path) → string
	anna.SetPropertyStr("readFile", ctx.Function(func(this *qjs.This) (*qjs.Value, error) {
		args := this.Args()
		if len(args) < 1 {
			return nil, errors.New("readFile requires a path argument")
		}
		resolved, err := ha.resolvePath(args[0].String())
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("readFile: %w", err)
		}
		return this.Context().NewString(string(data)), nil
	}))

	// anna.writeFile(path, content)
	anna.SetPropertyStr("writeFile", ctx.Function(func(this *qjs.This) (*qjs.Value, error) {
		args := this.Args()
		if len(args) < 2 {
			return nil, errors.New("writeFile requires (path, content)")
		}
		resolved, err := ha.resolvePath(args[0].String())
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return nil, fmt.Errorf("writeFile mkdir: %w", err)
		}
		if err := os.WriteFile(resolved, []byte(args[1].String()), 0o644); err != nil {
			return nil, fmt.Errorf("writeFile: %w", err)
		}
		return this.Context().NewBool(true), nil
	}))

	// anna.fetch(url, options?) → { status, body }
	anna.SetPropertyStr("fetch", ctx.Function(func(this *qjs.This) (*qjs.Value, error) {
		args := this.Args()
		if len(args) < 1 {
			return nil, errors.New("fetch requires a URL argument")
		}
		return ha.doFetch(this.Context(), args)
	}))
}

// resolvePath validates and resolves a file path, ensuring it's within allowed dirs.
// Allowed: plugin parent directory or $ANNA_HOME/workspace/.
func (ha *hostAPI) resolvePath(rawPath string) (string, error) {
	// Resolve relative paths against plugin directory.
	resolved := rawPath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(ha.pluginDir, resolved)
	}
	resolved = filepath.Clean(resolved)

	// Resolve symlinks to prevent escapes.
	evaled, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// File may not exist yet (for writes). Resolve the parent directory.
		parent, err2 := filepath.EvalSymlinks(filepath.Dir(resolved))
		if err2 != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		evaled = filepath.Join(parent, filepath.Base(resolved))
	}

	if ha.isAllowedPath(evaled) {
		return evaled, nil
	}
	return "", fmt.Errorf("access denied: path %q is outside allowed directories", rawPath)
}

// isAllowedPath checks if a resolved path is within allowed directories.
func (ha *hostAPI) isAllowedPath(resolved string) bool {
	// Allow plugin parent directory.
	pluginDir, err := filepath.EvalSymlinks(ha.pluginDir)
	if err == nil && isUnder(resolved, pluginDir) {
		return true
	}

	// Allow $ANNA_HOME/workspace/.
	workspace := filepath.Join(config.AnnaHome(), "workspace")
	workspace, err = filepath.EvalSymlinks(workspace)
	if err == nil && isUnder(resolved, workspace) {
		return true
	}

	return false
}

// isUnder checks if path is under or equal to dir.
func isUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// doFetch performs an HTTP request with safety constraints.
func (ha *hostAPI) doFetch(ctx *qjs.Context, args []*qjs.Value) (*qjs.Value, error) {
	rawURL := args[0].String()

	// Validate URL scheme.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("fetch: only http/https allowed, got %q", parsed.Scheme)
	}

	method := "GET"
	var body io.Reader
	headers := map[string]string{}
	timeout := fetchDefaultTimeout

	// Parse options if provided.
	if len(args) > 1 && !args[1].IsUndefined() && !args[1].IsNull() {
		opts := args[1]
		if m := opts.GetPropertyStr("method"); !m.IsUndefined() {
			method = strings.ToUpper(m.String())
		}
		if b := opts.GetPropertyStr("body"); !b.IsUndefined() {
			body = strings.NewReader(b.String())
		}
		if t := opts.GetPropertyStr("timeout"); !t.IsUndefined() {
			ms := t.Float64()
			if ms > 0 {
				d := time.Duration(ms) * time.Millisecond
				if d > fetchMaxTimeout {
					d = fetchMaxTimeout
				}
				timeout = d
			}
		}
		if h := opts.GetPropertyStr("headers"); !h.IsUndefined() && !h.IsNull() {
			headersMap, err := jsValueToGoMap(ctx, h)
			if err == nil {
				for k, v := range headersMap {
					if s, ok := v.(string); ok {
						headers[k] = s
					}
				}
			}
		}
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := &http.Client{
		Timeout:   timeout,
		Transport: newSSRFSafeTransport(),
	}
	req, err := http.NewRequestWithContext(reqCtx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read body with size limit.
	limitedReader := io.LimitReader(resp.Body, int64(fetchMaxBodySize)+1)
	respBody, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(respBody) > fetchMaxBodySize {
		ha.logger.Warn("fetch response truncated", "url", rawURL, "size", len(respBody))
		respBody = respBody[:fetchMaxBodySize]
	}

	// Return { status, body }.
	result := ctx.NewObject()
	result.SetPropertyStr("status", ctx.NewInt32(int32(resp.StatusCode)))
	result.SetPropertyStr("body", ctx.NewString(string(respBody)))
	return result, nil
}

// newSSRFSafeTransport returns an http.Transport that validates resolved IPs
// at connect time, preventing DNS rebinding attacks. The IP check happens in
// DialContext on the same resolution used for the actual connection, so there
// is no TOCTOU gap between validation and use.
func newSSRFSafeTransport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("ssrf: invalid address %q: %w", addr, err)
			}

			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("ssrf: resolve %q: %w", host, err)
			}

			// Filter to only safe (public) IPs.
			var safeAddrs []string
			for _, ip := range ips {
				if !isPrivateIP(ip.IP) {
					safeAddrs = append(safeAddrs, net.JoinHostPort(ip.IP.String(), port))
				}
			}
			if len(safeAddrs) == 0 {
				return nil, fmt.Errorf("fetch: requests to private/internal hosts are not allowed")
			}

			// Dial the first safe address.
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, safeAddrs[0])
		},
	}
}

// isPrivateIP checks if an IP is loopback, private, or link-local.
func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
