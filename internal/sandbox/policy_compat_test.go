package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"

	dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"
)

// PolicyCompatibilityTests verify fail-closed behavior for unsupported policy/backend combinations.

func TestPolicyValidation(t *testing.T) {
	t.Run("ValidPolicy", func(t *testing.T) {
		policy := Policy{
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
}

func TestRegistryFailClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("EmptyRegistryFailsClosed", func(t *testing.T) {
		registry := NewRegistry()

		policy := Policy{
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
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

func TestDockerFactorySupported(t *testing.T) {
	factory := dockerplugin.NewFactory(dockerplugin.Config{})

	t.Run("DisabledNetworkSupported", func(t *testing.T) {
		if !factory.Available() {
			t.Skip("docker daemon not reachable; skipping")
		}

		policy := Policy{
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

	t.Run("AllowAllNetworkSupported", func(t *testing.T) {
		if !factory.Available() {
			t.Skip("docker daemon not reachable; skipping")
		}

		policy := Policy{
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
			Network: NetworkPolicy{
				Mode: NetworkAllowAll,
			},
		}

		if err := factory.Supported(policy); err != nil {
			t.Errorf("Supported() = %v, want nil for allow-all network", err)
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
	t.Run("ErrorFormat", func(t *testing.T) {
		err := &PolicyCompatibilityError{
			Backend: "test",
			Reason:  "daemon not reachable",
		}

		msg := err.Error()
		if !containsCompat(msg, "test") {
			t.Errorf("Error message should mention backend, got: %s", msg)
		}
		if !containsCompat(msg, "daemon not reachable") {
			t.Errorf("Error message should mention reason, got: %s", msg)
		}
	})
}

func TestDefaultRegistry(t *testing.T) {
	registry := DefaultRegistry()

	if registry.Get("local") != nil {
		t.Error("DefaultRegistry should not include local factory")
	}

	// Docker is always registered (regardless of daemon availability)
	docker := registry.Get("docker")
	if docker == nil {
		t.Error("DefaultRegistry should include docker factory")
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
	return &PolicyCompatibilityError{Backend: f.name, Policy: policy, Reason: "unsupported"}
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

	session, err := registry.CreateSession(context.Background(), Policy{Filesystem: FilesystemPolicy{WorkingDir: t.TempDir()}})
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
	factory := orderedTestFactory{name: "mock", available: true, supported: true}

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
			Filesystem: FilesystemPolicy{
				WorkingDir: t.TempDir(),
			},
		}

		// Create multiple sessions concurrently
		const numSessions = 10
		sessions := make([]Session, numSessions)
		errs := make([]error, numSessions)

		var wg sync.WaitGroup
		wg.Add(numSessions)
		for i := range numSessions {
			go func(idx int) {
				defer wg.Done()
				sessions[idx], errs[idx] = registry.CreateSession(ctx, policy)
			}(i)
		}

		// Wait for all to complete
		wg.Wait()

		// Verify all succeeded
		for i := range numSessions {
			if errs[i] != nil {
				t.Errorf("CreateSession %d: %v", i, errs[i])
			}
			if sessions[i] != nil {
				_ = sessions[i].Close()
			}
		}
	})
}
