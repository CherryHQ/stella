package host

// Issue #708 Section B: pluginhost seals its static registrations and capability
// bindings before runtime start. After Seal, LoadCatalog and the Set* capability
// binders refuse late changes, while the dynamic desired-state surface
// (Stop / Apply*) stays available.

import (
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestSealFreezesStaticRegistrationsButKeepsDynamic(t *testing.T) {
	h := New(&stubStore{plugins: map[string]config.Plugin{}}, WithChannelRuntimeServices(NewChannelRuntimeServices()))
	h.SetAccountEnrollment(fakeAccountEnroller{})
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
				t.Error("SetNotificationService after Seal should panic")
			}
		}()
		h.SetNotificationService(nil)
	}()

	// Runtime shutdown remains available after sealing static composition.
	if err := h.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
