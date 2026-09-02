package host

// Issue #708 Section B: pluginhost seals its static registrations and capability
// bindings before runtime start. After Seal, LoadCatalog and the Set* capability
// binders refuse late changes, while the dynamic desired-state surface
// (RegisterManifestPlugins / Apply*) stays available.

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
)

func TestSealFreezesStaticRegistrationsButKeepsDynamic(t *testing.T) {
	h := New(&stubStore{plugins: map[string]config.Plugin{}}, WithChannelRuntimeServices(NewChannelRuntimeServices()))
	if err := h.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}

	if err := h.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Seal is one-shot.
	if err := h.Seal(); err == nil {
		t.Error("second Seal should error")
	}

	// Late static catalog load is refused.
	if err := h.LoadDefaultCatalog(); err == nil {
		t.Error("LoadDefaultCatalog after Seal should error")
	}

	// Late capability binding panics (a composition bug, like a duplicate).
	func() {
		defer func() {
			if recover() == nil {
				t.Error("SetSchedulerService after Seal should panic")
			}
		}()
		h.SetSchedulerService(nil)
	}()

	// The dynamic desired-state surface remains available after seal: an empty
	// manifest re-registration reconciles zero plugins without panicking or
	// hitting the seal.
	h.RegisterManifestPlugins(&manifest.Manifest{})
}
