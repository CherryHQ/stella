package kubernetes

import (
	"context"
	"errors"
	"testing"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func resourceTestClient(pod *core.Pod) *Client {
	return &Client{
		api:       fake.NewClientset(pod),
		owner:     &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: pod.Namespace}},
		authority: `{"namespace":"tenant","pvc_uid":"pvc-1"}`,
	}
}

func resourceTestIdentity(t *testing.T, c *Client, pod *core.Pod) sandbox.ResourceIdentity {
	t.Helper()
	s := &session{client: c, pod: pod}
	identity, err := s.ResourceIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestResourceProbeDistinguishesIdentityFailuresFromAbsence(t *testing.T) {
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "sandbox", Namespace: "tenant", UID: "uid-1"}}
	c := resourceTestClient(pod)
	identity := resourceTestIdentity(t, c, pod)
	controller, err := (&factory{client: c}).ResourceController(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	got, err := controller.Probe(t.Context(), identity)
	if err != nil || got.State != sandbox.ResourceStatePresent {
		t.Fatalf("present probe = %+v, %v", got, err)
	}

	missing := identity
	missing.Ref = `{"name":"sandbox","uid":"uid-missing"}`
	got, err = controller.Probe(t.Context(), missing)
	if err == nil || got.State != sandbox.ResourceStateUnknown {
		t.Fatalf("UID mismatch probe = %+v, %v", got, err)
	}

	changedNamespace := identity
	changedNamespace.Authority = `{"namespace":"other","pvc_uid":"pvc-1"}`
	got, err = controller.Probe(t.Context(), changedNamespace)
	if err == nil || got.State != sandbox.ResourceStateUnknown {
		t.Fatalf("namespace mismatch probe = %+v, %v", got, err)
	}

	api := c.api.(*fake.Clientset)
	api.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("API unavailable")
	})
	got, err = controller.Probe(t.Context(), identity)
	if err == nil || got.State != sandbox.ResourceStateUnknown {
		t.Fatalf("API failure probe = %+v, %v", got, err)
	}
}

func TestResourceProbeDoesNotTreatMissingObjectAsTerminationProof(t *testing.T) {
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "sandbox", Namespace: "tenant", UID: "uid-1"}}
	c := resourceTestClient(pod)
	identity := resourceTestIdentity(t, c, pod)
	if err := c.api.CoreV1().Pods(pod.Namespace).Delete(t.Context(), pod.Name, meta.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	controller, err := (&factory{client: c}).ResourceController(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := controller.Probe(t.Context(), identity)
	if err == nil || got.State != sandbox.ResourceStateUnknown {
		t.Fatalf("missing Pod probe = %+v, %v", got, err)
	}
}

func TestResourceTerminateUsesUIDPrecondition(t *testing.T) {
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:       "sandbox",
			Namespace:  "tenant",
			UID:        "uid-1",
			Labels:     map[string]string{labelBoot: "boot-1", "stella.cherryhq.io/generation": "generation-1"},
			Finalizers: []string{finalizer, "foreign.example/finalizer"},
		},
		Status: core.PodStatus{Phase: core.PodSucceeded},
	}
	c := resourceTestClient(pod)
	identity := resourceTestIdentity(t, c, pod)
	var deleted clienttesting.DeleteAction
	api := c.api.(*fake.Clientset)
	api.PrependReactor("delete", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		deleted = action.(clienttesting.DeleteAction)
		return true, nil, nil
	})
	controller, err := (&factory{client: c}).ResourceController(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := controller.Terminate(t.Context(), identity)
	if err != nil {
		t.Fatalf("terminate: %+v, %v", got, err)
	}
	if deleted == nil || deleted.GetDeleteOptions().Preconditions == nil || deleted.GetDeleteOptions().Preconditions.UID == nil || *deleted.GetDeleteOptions().Preconditions.UID != pod.UID {
		t.Fatalf("delete action did not carry exact UID precondition: %#v", deleted)
	}
	if got.State != sandbox.ResourceStateAbsent {
		t.Fatalf("terminal Pod with retained foreign finalizer = %+v, want absent", got)
	}
	current, err := c.api.CoreV1().Pods(pod.Namespace).Get(t.Context(), pod.Name, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current.Labels[labelBoot] != "boot-1" || current.Labels["stella.cherryhq.io/generation"] != "generation-1" {
		t.Fatalf("termination changed identity labels: %v", current.Labels)
	}
	if len(current.Finalizers) != 1 || current.Finalizers[0] != "foreign.example/finalizer" {
		t.Fatalf("termination removed foreign finalizer: %v", current.Finalizers)
	}
}

func TestResourceTerminateRefusesReplacementUID(t *testing.T) {
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "sandbox", Namespace: "tenant", UID: "uid-current"}}
	c := resourceTestClient(pod)
	identity := resourceTestIdentity(t, c, &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "sandbox", Namespace: "tenant", UID: "uid-old"}})
	called := false
	api := c.api.(*fake.Clientset)
	api.PrependReactor("delete", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		called = true
		return true, nil, apierrors.NewConflict(core.Resource("pods"), pod.Name, errors.New("UID mismatch"))
	})
	controller, err := (&factory{client: c}).ResourceController(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := controller.Terminate(context.Background(), identity)
	if err == nil || got.State != sandbox.ResourceStateUnknown {
		t.Fatalf("replacement UID termination = %+v, %v", got, err)
	}
	if called {
		t.Fatal("attempted delete after observing replacement UID")
	}
}
