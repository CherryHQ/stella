package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/config"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	larkCLIPluginID            = "tool/lark-cli"
	larkCLIProfileName         = "stella-channel"
	larkCLIBindingMarker       = ".stella-channel-binding.json"
	larkCLIBindingMarkerFormat = 1
	larkCLIBootstrapTimeout    = 30 * time.Second
)

// larkCLIAppConfig is the trusted Channel credential material passed directly
// from Stella's control plane to the bootstrap process. It must never be added
// to prompts, process arguments, ordinary session env, or user-visible errors.
type larkCLIAppConfig struct {
	ChannelID string
	AppID     string
	AppSecret string
	Brand     string
}

type larkCLIAppConfigResolver func(context.Context, string) (*larkCLIAppConfig, error)

type larkCLIChannelStore interface {
	ListChannelsByType(context.Context, string) ([]config.Channel, error)
}

// resolveLarkCLIAppConfig selects the current Agent's unique enabled Feishu
// Channel. Existing rows without brand predate international Channel support
// and therefore retain the domestic Feishu default.
func resolveLarkCLIAppConfig(ctx context.Context, store larkCLIChannelStore, agentID string) (*larkCLIAppConfig, error) {
	channels, err := store.ListChannelsByType(ctx, pkgchannel.PlatformFeishu)
	if err != nil {
		return nil, fmt.Errorf("resolve lark-cli channel: %w", err)
	}

	var selected *config.Channel
	for i := range channels {
		ch := &channels[i]
		if !ch.Enabled || ch.AgentID != agentID {
			continue
		}
		if selected != nil {
			return nil, errors.New("resolve lark-cli channel: multiple enabled Feishu channels are bound to this agent")
		}
		selected = ch
	}
	if selected == nil {
		return nil, nil
	}

	var channelCfg pkgchannel.FeishuConfig
	if err := json.Unmarshal([]byte(selected.Config), &channelCfg); err != nil {
		return nil, errors.New("resolve lark-cli channel: invalid Feishu channel configuration")
	}
	if channelCfg.AppID == "" || channelCfg.AppSecret == "" {
		return nil, errors.New("resolve lark-cli channel: Feishu channel app credentials are incomplete")
	}
	brand := channelCfg.EffectiveBrand()
	if brand != pkgchannel.FeishuBrand && brand != pkgchannel.LarkBrand {
		return nil, errors.New("resolve lark-cli channel: Feishu channel brand must be feishu or lark")
	}

	return &larkCLIAppConfig{
		ChannelID: selected.ID,
		AppID:     channelCfg.AppID,
		AppSecret: channelCfg.AppSecret,
		Brand:     brand,
	}, nil
}

type larkCLIBinding struct {
	Format       int    `json:"format"`
	ChannelID    string `json:"channel_id"`
	AppID        string `json:"app_id"`
	Brand        string `json:"brand"`
	SecretDigest string `json:"secret_digest"`
}

func newLarkCLIBinding(app larkCLIAppConfig) larkCLIBinding {
	digest := sha256.Sum256([]byte(app.AppSecret))
	return larkCLIBinding{
		Format:       larkCLIBindingMarkerFormat,
		ChannelID:    app.ChannelID,
		AppID:        app.AppID,
		Brand:        app.Brand,
		SecretDigest: hex.EncodeToString(digest[:]),
	}
}

func (b larkCLIBinding) matches(other larkCLIBinding) bool {
	return b == other
}

func (b larkCLIBinding) requiresUserStateReset(other larkCLIBinding) bool {
	return b.AppID != other.AppID || b.Brand != other.Brand
}

var larkCLIBootstrapLocks sync.Map

func larkCLIBootstrapLock(markerPath string) *sync.Mutex {
	value, _ := larkCLIBootstrapLocks.LoadOrStore(markerPath, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// bootstrapLarkCLI initializes lark-cli through its native configuration
// command before the model receives shell access. App Secret is passed only on
// stdin; stdout and stderr are drained and discarded to keep credentials and
// upstream diagnostics out of Stella logs.
func bootstrapLarkCLI(ctx context.Context, session pkgsandbox.Session, app larkCLIAppConfig) error {
	policyEnv := session.Policy().Env
	configDir := policyEnv[agentsandbox.LarkCLIConfigDirEnv]
	dataDir := policyEnv[agentsandbox.LarkCLIDataDirEnv]
	if configDir == "" || dataDir == "" {
		return errors.New("lark-cli bootstrap: isolated config directories are unavailable")
	}

	hostConfigDir, err := session.ResolveWritePath(configDir)
	if err != nil {
		return errors.New("lark-cli bootstrap: config directory is not writable")
	}
	hostDataDir, err := session.ResolveWritePath(dataDir)
	if err != nil {
		return errors.New("lark-cli bootstrap: data directory is not writable")
	}
	markerPath := filepath.Join(hostConfigDir, larkCLIBindingMarker)

	lock := larkCLIBootstrapLock(markerPath)
	lock.Lock()
	defer lock.Unlock()

	target := newLarkCLIBinding(app)
	previous, found := readLarkCLIBinding(markerPath)
	if found && previous.matches(target) {
		return nil
	}

	// Changing API brand or App ID makes the old user token unsafe to reuse.
	// Secret-only rotation keeps the native user records and token keychain.
	if found && previous.requiresUserStateReset(target) {
		if err := resetLarkCLIUserState(hostConfigDir, hostDataDir); err != nil {
			return errors.New("lark-cli bootstrap: failed to reset obsolete native auth state")
		}
	}

	processEnv := map[string]string{
		agentsandbox.LarkCLIConfigDirEnv: configDir,
		agentsandbox.LarkCLIDataDirEnv:   dataDir,
	}
	if err := runLarkCLIProcess(ctx, session, processEnv, []string{
		"config", "init",
		"--app-id", app.AppID,
		"--app-secret-stdin",
		"--brand", app.Brand,
		"--name", larkCLIProfileName,
	}, app.AppSecret+"\n"); err != nil {
		return err
	}
	if err := runLarkCLIProcess(ctx, session, processEnv, []string{
		"profile", "use", larkCLIProfileName,
	}, ""); err != nil {
		return err
	}
	if err := writeLarkCLIBinding(markerPath, target); err != nil {
		return errors.New("lark-cli bootstrap: failed to persist channel binding marker")
	}
	return nil
}

func readLarkCLIBinding(path string) (larkCLIBinding, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return larkCLIBinding{}, false
	}
	var binding larkCLIBinding
	if err := json.Unmarshal(data, &binding); err != nil || binding.Format != larkCLIBindingMarkerFormat {
		return larkCLIBinding{}, false
	}
	return binding, true
}

func writeLarkCLIBinding(path string, binding larkCLIBinding) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stella-channel-binding-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func resetLarkCLIUserState(configDir, dataDir string) error {
	if configDir == "" || dataDir == "" || configDir == string(filepath.Separator) || dataDir == string(filepath.Separator) {
		return errors.New("unsafe lark-cli state path")
	}
	if err := os.Remove(filepath.Join(configDir, "config.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(dataDir)
}

func runLarkCLIProcess(ctx context.Context, session pkgsandbox.Session, env map[string]string, args []string, stdin string) error {
	proc, err := session.StartProcess(ctx, pkgsandbox.ProcessRequest{
		Path:    "lark-cli",
		Args:    args,
		Cwd:     session.WorkingDir(),
		Env:     env,
		Timeout: larkCLIBootstrapTimeout,
	})
	if err != nil {
		return errors.New("lark-cli bootstrap: unable to start native configuration")
	}
	if proc == nil {
		return errors.New("lark-cli bootstrap: native configuration process is unavailable")
	}
	defer func() {
		_ = proc.Close()
	}()

	var drains sync.WaitGroup
	for _, reader := range []io.ReadCloser{proc.Stdout(), proc.Stderr()} {
		if reader == nil {
			continue
		}
		drains.Add(1)
		go func(r io.ReadCloser) {
			defer drains.Done()
			defer func() {
				_ = r.Close()
			}()
			_, _ = io.Copy(io.Discard, r)
		}(reader)
	}

	if input := proc.Stdin(); input != nil {
		if stdin != "" {
			if _, err := io.WriteString(input, stdin); err != nil {
				_ = input.Close()
				return errors.New("lark-cli bootstrap: failed to provide native configuration input")
			}
		}
		if err := input.Close(); err != nil {
			return errors.New("lark-cli bootstrap: failed to close native configuration input")
		}
	}

	result, waitErr := proc.Wait(ctx)
	drains.Wait()
	if waitErr != nil || result.ExitCode != 0 {
		return fmt.Errorf("lark-cli bootstrap: native configuration failed (exit_code=%d)", result.ExitCode)
	}
	return nil
}
