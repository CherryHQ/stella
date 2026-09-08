package kubernetes

import (
	"encoding/base64"
	"testing"

	core "k8s.io/api/core/v1"
)

func TestOwnerRequiresDirectHomePVCMount(t *testing.T) {
	home := "/data/stella"
	owner := &core.Pod{Spec: core.PodSpec{
		Volumes:    []core.Volume{{Name: "home", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "home"}}}},
		Containers: []core.Container{{VolumeMounts: []core.VolumeMount{{Name: "home", MountPath: "/data"}}}},
	}}
	if claim, prefix, err := ownerHomeVolume(owner, home); err != nil || claim != "home" || prefix != "stella" {
		t.Fatalf("direct mount: %q %q %v", claim, prefix, err)
	}
	for _, change := range []func(*core.Pod){
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].ReadOnly = true },
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].MountPath = "/" },
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPath = "tenant" },
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPathExpr = "$(TENANT)" },
		func(p *core.Pod) { p.Spec.Volumes[0].PersistentVolumeClaim = nil },
		func(p *core.Pod) { p.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly = true },
	} {
		bad := owner.DeepCopy()
		change(bad)
		if _, _, err := ownerHomeVolume(bad, home); err == nil {
			t.Fatal("accepted unsupported home mount")
		}
	}
	if _, _, err := ownerHomeVolume(owner, "/outside"); err == nil {
		t.Fatal("accepted home outside PVC")
	}
}

func TestHomePVCDiscoveryWithSidecars(t *testing.T) {
	owner := &core.Pod{Spec: core.PodSpec{
		Volumes: []core.Volume{
			{Name: "logs", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "logs"}}},
			{Name: "data", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "stella-home"}}},
		},
		Containers: []core.Container{
			{Name: "sidecar", VolumeMounts: []core.VolumeMount{{Name: "logs", MountPath: "/logs"}}},
			{Name: "server", VolumeMounts: []core.VolumeMount{{Name: "data", MountPath: "/data"}}},
		},
	}}
	if claim, prefix, err := ownerHomeVolume(owner, "/data"); err != nil || claim != "stella-home" || prefix != "." {
		t.Fatalf("PVC discovery: %q %q %v", claim, prefix, err)
	}
	owner.Spec.Containers[0].VolumeMounts = []core.VolumeMount{{Name: "data", MountPath: "/data", ReadOnly: true}}
	if _, _, err := ownerHomeVolume(owner, "/data"); err != nil {
		t.Fatal(err)
	}
	owner.Spec.Containers[0].VolumeMounts[0].Name = "logs"
	if _, _, err := ownerHomeVolume(owner, "/data"); err == nil {
		t.Fatal("accepted ambiguous home PVCs")
	}
}

func TestPodIdentityFromProjectedToken(t *testing.T) {
	token := func(payload string) string {
		return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
	}
	owner, err := podIdentity(token(`{"kubernetes.io":{"namespace":"tenant","pod":{"name":"server-123","uid":"pod-uid"}}}`))
	if err != nil || owner.Namespace != "tenant" || owner.Name != "server-123" || owner.UID != "pod-uid" {
		t.Fatalf("identity: %+v %v", owner, err)
	}
	for _, invalid := range []string{"", "secret-sentinel", "header.!.signature", token("secret-sentinel"), token(`{}`), token(`{"kubernetes.io":{"namespace":"tenant","pod":{"name":"server"}}}`), token(`{"kubernetes.io":{"namespace":"tenant","pod":{"name":["secret-sentinel"],"uid":"pod-uid"}}}`)} {
		_, err := podIdentity(invalid)
		if err == nil || err.Error() != "kubernetes: projected Pod-bound service account token required" {
			t.Fatalf("invalid identity must fail without exposing claims: %v", err)
		}
	}
}
