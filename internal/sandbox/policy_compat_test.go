package sandbox

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"

	localplugin "github.com/vaayne/anna/plugins/sandbox/local"
)

// PolicyCompatibilityTests verify fail-closed behavior for unsupported policy/backend combinations.

func TestPolicyValidation(t *testing.T) {
	t.Run("ValidPolicy", func(t *testing.T) {
		policy := Policy{
			Backend: "local",
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode: NetworkDisabled,
			},
		}

		if err := policy.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})

	t.Run("MissingWorkingDir", func(t *testing.T) {
		policy := Policy{
			Backend: "local",
			Network: NetworkPolicy{
				Mode: NetworkDisabled,
			},
		}

		if err := policy.Validate(); err == nil {
			t.Error("Validate() = nil, want error for missing WorkingDir")
		}
	})

	t.Run("InvalidNetworkMode", func(t *testing.T) {
		policy := Policy{
			Backend: "local",
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode: "invalid_mode",
			},
		}

		if err := policy.Validate(); err == nil {
			t.Error("Validate() = nil, want error for invalid network mode")
		}
	})

	t.Run("EmptyWhitelist", func(t *testing.T) {
		policy := Policy{
			Backend: "local",
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode:      NetworkWhitelist,
				Allowlist: []string{},
			},
		}

		if err := policy.Validate(); err == nil {
			t.Error("Validate() = nil, want error for empty whitelist")
		}
	})
}

func TestRegistryFailClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("UnknownBackendFailsClosed", func(t *testing.T) {
		registry := NewRegistry()
		_ = registry.Register(localplugin.NewFactory())

		policy := Policy{
			Backend: "unknown_backend",
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
		}

		_, err := registry.CreateSession(ctx, policy)
		if err == nil {
			t.Fatal("CreateSession with unknown backend should fail closed")
		}

		compatErr := &PolicyCompatibilityError{}
		ok := errors.As(err, &compatErr)
		if !ok {
			t.Fatalf("error should be PolicyCompatibilityError, got %T: %v", err, err)
		}

		if compatErr.Backend != "unknown_backend" {
			t.Errorf("Backend = %q, want %q", compatErr.Backend, "unknown_backend")
		}
	})

	t.Run("UnsupportedPolicyFailsClosed", func(t *testing.T) {
		registry := NewRegistry()
		// Only register local factory
		_ = registry.Register(localplugin.NewFactory())

		// Try to create session with strict whitelist (local doesn't support this)
		policy := Policy{
			Backend: "local",
			Relaxed: false, // Strict mode
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode:      NetworkWhitelist,
				Allowlist: []string{"example.com"},
			},
		}

		_, err := registry.CreateSession(ctx, policy)
		if err == nil {
			t.Fatal("CreateSession with unsupported policy should fail closed")
		}

		compatErr := &PolicyCompatibilityError{}
		ok := errors.As(err, &compatErr)
		if !ok {
			t.Fatalf("error should be PolicyCompatibilityError, got %T: %v", err, err)
		}

		if !compatErr.RelaxedWouldHelp {
			t.Error("RelaxedWouldHelp should be true for this case")
		}
	})

	t.Run("RelaxedModeAllowsPartialSupport", func(t *testing.T) {
		registry := NewRegistry()
		_ = registry.Register(localplugin.NewFactory())

		// Same policy but with Relaxed=true
		policy := Policy{
			Backend: "local",
			Relaxed: true, // Explicit opt-in
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode:      NetworkWhitelist,
				Allowlist: []string{"example.com"},
			},
		}

		session, err := registry.CreateSession(ctx, policy)
		if err != nil {
			t.Fatalf("CreateSession with Relaxed=true should succeed: %v", err)
		}
		defer func() { _ = session.Close() }()

		if session == nil {
			t.Error("session should not be nil")
		}
	})

	t.Run("AutoSelectNoCompatibleBackend", func(t *testing.T) {
		registry := NewRegistry()
		// Empty registry

		policy := Policy{
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode: NetworkDisabled,
			},
		}

		_, err := registry.CreateSession(ctx, policy)
		if err == nil {
			t.Fatal("CreateSession with empty registry should fail closed")
		}

		compatErr := &PolicyCompatibilityError{}
		ok := errors.As(err, &compatErr)
		if !ok {
			t.Fatalf("error should be PolicyCompatibilityError, got %T: %v", err, err)
		}

		if compatErr.Backend != "any" {
			t.Errorf("Backend = %q, want %q", compatErr.Backend, "any")
		}
	})
}

func TestLocalFactorySupported(t *testing.T) {
	factory := localplugin.NewFactory()

	t.Run("DisabledNetworkRequiresRelaxed", func(t *testing.T) {
		policy := Policy{
			Relaxed: false,
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode: NetworkDisabled,
			},
		}

		if err := factory.Supported(policy); err == nil {
			t.Error("Supported() = nil, want error for network disabled without relaxed")
		}
	})

	t.Run("AllowAllWithStrictFilesystemRequiresRelaxed", func(t *testing.T) {
		policy := Policy{
			Relaxed: false,
			Filesystem: FilesystemPolicy{
				WorkingDir:   t.TempDir(),
				AllowEscapes: false,
			},
			Network: NetworkPolicy{
				Mode: NetworkAllowAll,
			},
		}

		if err := factory.Supported(policy); err == nil {
			t.Error("Supported() = nil, want error for strict filesystem without relaxed")
		}
	})

	t.Run("WhitelistRequiresRelaxed", func(t *testing.T) {
		policy := Policy{
			Relaxed: false,
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode:      NetworkWhitelist,
				Allowlist: []string{"example.com"},
			},
		}

		err := factory.Supported(policy)
		if err == nil {
			t.Error("Supported() = nil, want error for whitelist without relaxed")
		}

		compatErr := &PolicyCompatibilityError{}
		ok := errors.As(err, &compatErr)
		if !ok {
			t.Fatalf("error should be PolicyCompatibilityError, got %T", err)
		}

		if !compatErr.RelaxedWouldHelp {
			t.Error("RelaxedWouldHelp should be true")
		}
	})

	t.Run("AllowAllWithEscapesStillRequiresRelaxed", func(t *testing.T) {
		policy := Policy{
			Relaxed: false,
			Filesystem: FilesystemPolicy{
				WorkingDir:   t.TempDir(),
				AllowEscapes: true,
			},
			Network: NetworkPolicy{
				Mode: NetworkAllowAll,
			},
		}

		if err := factory.Supported(policy); err == nil {
			t.Error("Supported() = nil, want error for local backend without relaxed even when escapes are allowed")
		}
	})

	t.Run("AllowAllWithRelaxedAllowed", func(t *testing.T) {
		policy := Policy{
			Relaxed: true,
			Filesystem: FilesystemPolicy{
				WorkingDir:   t.TempDir(),
				AllowEscapes: false,
			},
			Network: NetworkPolicy{
				Mode: NetworkAllowAll,
			},
		}

		if err := factory.Supported(policy); err != nil {
			t.Errorf("Supported() = %v, want nil", err)
		}
	})

	t.Run("WhitelistWithRelaxedAllowed", func(t *testing.T) {
		policy := Policy{
			Relaxed: true,
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode:      NetworkWhitelist,
				Allowlist: []string{"example.com"},
			},
		}

		if err := factory.Supported(policy); err != nil {
			t.Errorf("Supported() = %v, want nil for whitelist with relaxed", err)
		}
	})
}

func TestBoxshFactorySupported(t *testing.T) {
	factory := &boxshFactory{}

	t.Run("AvailabilityBasedOnPlatform", func(t *testing.T) {
		available := factory.Available()

		if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
			if !available {
				t.Error("boxsh should be available on linux/darwin")
			}
		} else {
			if available {
				t.Error("boxsh should not be available on non-linux/darwin")
			}
		}
	})

	t.Run("WhitelistNotSupported", func(t *testing.T) {
		if !factory.Available() {
			t.Skip("boxsh not available on this platform")
		}

		policy := Policy{
			Relaxed: false,
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode:      NetworkWhitelist,
				Allowlist: []string{"example.com"},
			},
		}

		err := factory.Supported(policy)
		if err == nil {
			t.Error("Supported() = nil, want error for whitelist on boxsh")
		}

		compatErr := &PolicyCompatibilityError{}
		ok := errors.As(err, &compatErr)
		if !ok {
			t.Fatalf("error should be PolicyCompatibilityError, got %T", err)
		}

		if !compatErr.RelaxedWouldHelp {
			t.Error("RelaxedWouldHelp should be true")
		}
	})

	t.Run("DisabledNetworkSupported", func(t *testing.T) {
		if !factory.Available() {
			t.Skip("boxsh not available on this platform")
		}

		policy := Policy{
			Relaxed: false,
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode: NetworkDisabled,
			},
		}

		if err := factory.Supported(policy); err != nil {
			t.Errorf("Supported() = %v, want nil for disabled network", err)
		}
	})
}

func TestIsPolicyCompatibilityError(t *testing.T) {
	t.Run("WithPolicyCompatibilityError", func(t *testing.T) {
		err := &PolicyCompatibilityError{
			Backend: "test",
			Reason:  "test reason",
		}

		if !IsPolicyCompatibilityError(err) {
			t.Error("IsPolicyCompatibilityError should return true for PolicyCompatibilityError")
		}
	})

	t.Run("WithRegularError", func(t *testing.T) {
		err := errors.New("regular error")

		if IsPolicyCompatibilityError(err) {
			t.Error("IsPolicyCompatibilityError should return false for regular error")
		}
	})

	t.Run("WithNil", func(t *testing.T) {
		if IsPolicyCompatibilityError(nil) {
			t.Error("IsPolicyCompatibilityError should return false for nil")
		}
	})
}

func TestPolicyAccessors(t *testing.T) {
	t.Run("IsRelaxed", func(t *testing.T) {
		relaxed := Policy{Relaxed: true}
		if !relaxed.IsRelaxed() {
			t.Error("IsRelaxed() should return true")
		}

		strict := Policy{Relaxed: false}
		if strict.IsRelaxed() {
			t.Error("IsRelaxed() should return false")
		}
	})

	t.Run("RequiresWhitelist", func(t *testing.T) {
		whitelist := Policy{
			Network: NetworkPolicy{Mode: NetworkWhitelist},
		}
		if !whitelist.RequiresWhitelist() {
			t.Error("RequiresWhitelist() should return true for whitelist mode")
		}

		disabled := Policy{
			Network: NetworkPolicy{Mode: NetworkDisabled},
		}
		if disabled.RequiresWhitelist() {
			t.Error("RequiresWhitelist() should return false for disabled mode")
		}
	})

	t.Run("NetworkModeOrDefault", func(t *testing.T) {
		empty := Policy{
			Network: NetworkPolicy{Mode: ""},
		}
		if got := empty.NetworkModeOrDefault(); got != NetworkDisabled {
			t.Errorf("NetworkModeOrDefault() = %q, want %q", got, NetworkDisabled)
		}

		explicit := Policy{
			Network: NetworkPolicy{Mode: NetworkAllowAll},
		}
		if got := explicit.NetworkModeOrDefault(); got != NetworkAllowAll {
			t.Errorf("NetworkModeOrDefault() = %q, want %q", got, NetworkAllowAll)
		}
	})
}

func TestPolicyCompatibilityErrorMessage(t *testing.T) {
	t.Run("WithRelaxedWouldHelp", func(t *testing.T) {
		err := &PolicyCompatibilityError{
			Backend:          "test",
			Reason:           "whitelist not supported",
			RelaxedWouldHelp: true,
		}

		msg := err.Error()
		if !containsCompat(msg, "Relaxed=true") {
			t.Errorf("Error message should mention Relaxed=true, got: %s", msg)
		}
	})

	t.Run("WithoutRelaxedWouldHelp", func(t *testing.T) {
		err := &PolicyCompatibilityError{
			Backend:          "test",
			Reason:           "not available",
			RelaxedWouldHelp: false,
		}

		msg := err.Error()
		if containsCompat(msg, "Relaxed") {
			t.Errorf("Error message should not mention Relaxed, got: %s", msg)
		}
	})
}

func TestRegistryCreateRelaxedSession(t *testing.T) {
	ctx := context.Background()
	registry := NewRegistry()
	_ = registry.Register(localplugin.NewFactory())

	base := Policy{
		Filesystem: FilesystemPolicy{
			WorkingDir: t.TempDir(),
		},
		Network: NetworkPolicy{
			Mode:      NetworkWhitelist,
			Allowlist: []string{"example.com"},
		},
	}

	// Should succeed with relaxed helper
	session, err := registry.CreateRelaxedSession(ctx, base)
	if err != nil {
		t.Fatalf("CreateRelaxedSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Verify the session has relaxed policy
	if !session.Policy().IsRelaxed() {
		t.Error("session policy should have Relaxed=true")
	}
}

func TestDefaultRegistry(t *testing.T) {
	registry := DefaultRegistry()

	// Should have local factory
	local := registry.Get("local")
	if local == nil {
		t.Error("DefaultRegistry should include local factory")
	}

	// Should have boxsh on supported platforms
	if PlatformRequiresBoxsh() {
		boxsh := registry.Get("boxsh")
		if boxsh == nil {
			t.Error("DefaultRegistry should include boxsh factory on supported platforms")
		}
	}

	// List should return available backends
	available := registry.AvailableBackends()
	if len(available) == 0 {
		t.Error("AvailableBackends should not be empty")
	}
}

func containsCompat(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsCompatSubstring(s, substr))
}

func containsCompatSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type orderedTestFactory struct {
	name      string
	available bool
	supported bool
	created   *string
}

func (f orderedTestFactory) Name() string { return f.name }
func (f orderedTestFactory) Available() bool {
	return f.available
}

func (f orderedTestFactory) Supported(policy Policy) error {
	if f.supported {
		return nil
	}
	return &PolicyCompatibilityError{Backend: f.name, Policy: policy, Reason: "unsupported", RelaxedWouldHelp: true}
}

func (f orderedTestFactory) CreateSession(_ context.Context, _ Policy) (Session, error) {
	if f.created != nil {
		*f.created = f.name
	}
	return NopSession(), nil
}

func TestRegistryPreservesRegistrationOrder(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(orderedTestFactory{name: "first", available: true, supported: true}); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	if err := registry.Register(orderedTestFactory{name: "second", available: true, supported: true}); err != nil {
		t.Fatalf("Register second: %v", err)
	}

	if got, want := registry.List(), []string{"first", "second"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("List() = %v, want %v", got, want)
	}

	registry.Unregister("first")
	if got, want := registry.List(), []string{"second"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("List() after Unregister = %v, want %v", got, want)
	}
}

func TestRegistryAutoSelectUsesRegistrationOrder(t *testing.T) {
	registry := NewRegistry()
	created := ""
	if err := registry.Register(orderedTestFactory{name: "preferred", available: true, supported: true, created: &created}); err != nil {
		t.Fatalf("Register preferred: %v", err)
	}
	if err := registry.Register(orderedTestFactory{name: "fallback", available: true, supported: true, created: &created}); err != nil {
		t.Fatalf("Register fallback: %v", err)
	}

	session, err := registry.CreateSession(context.Background(), Policy{Relaxed: true, Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()}})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if created != "preferred" {
		t.Fatalf("auto-selected backend = %q, want %q", created, "preferred")
	}
}

func TestRegistryAutoSelectSkipsUnsupportedBackendsInOrder(t *testing.T) {
	registry := NewRegistry()
	created := ""
	if err := registry.Register(orderedTestFactory{name: "unsupported", available: true, supported: false, created: &created}); err != nil {
		t.Fatalf("Register unsupported: %v", err)
	}
	if err := registry.Register(orderedTestFactory{name: "supported", available: true, supported: true, created: &created}); err != nil {
		t.Fatalf("Register supported: %v", err)
	}

	session, err := registry.CreateSession(context.Background(), Policy{Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()}})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if created != "supported" {
		t.Fatalf("auto-selected backend = %q, want %q", created, "supported")
	}
}

func TestRegistryConcurrency(t *testing.T) {
	registry := NewRegistry()
	factory := localplugin.NewFactory()

	// Test concurrent registration
	t.Run("ConcurrentRegister", func(t *testing.T) {
		// First registration should succeed
		if err := registry.Register(factory); err != nil {
			t.Errorf("first Register: %v", err)
		}

		// Second registration should fail
		if err := registry.Register(factory); err == nil {
			t.Error("second Register should fail")
		}
	})

	t.Run("ConcurrentCreateSession", func(t *testing.T) {
		ctx := context.Background()
		policy := Policy{
			Backend: "local",
			Relaxed: true,
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
		}

		// Create multiple sessions concurrently
		const numSessions = 10
		sessions := make([]Session, numSessions)
		errors := make([]error, numSessions)

		var wg sync.WaitGroup
		wg.Add(numSessions)
		for i := range numSessions {
			go func(idx int) {
				defer wg.Done()
				sessions[idx], errors[idx] = registry.CreateSession(ctx, policy)
			}(i)
		}

		// Wait for all to complete
		wg.Wait()

		// Verify all succeeded
		for i := range numSessions {
			if errors[i] != nil {
				t.Errorf("CreateSession %d: %v", i, errors[i])
			}
			if sessions[i] != nil {
				_ = sessions[i].Close()
			}
		}
	})
}
