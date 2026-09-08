package kubernetes

import (
	"testing"

	core "k8s.io/api/core/v1"
)

func TestOwnerRequiresDirectHomePVCMount(t *testing.T) {
	cfg := Config{PVC: "home", StellaHome: "/data/stella"}
	owner := &core.Pod{Spec: core.PodSpec{
		Volumes:    []core.Volume{{Name: "home", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "home"}}}},
		Containers: []core.Container{{VolumeMounts: []core.VolumeMount{{Name: "home", MountPath: "/data"}}}},
	}}
	if prefix, err := ownerHomePrefix(owner, cfg); err != nil || prefix != "stella" {
		t.Fatalf("direct mount: %q %v", prefix, err)
	}
	for _, change := range []func(*core.Pod){
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].ReadOnly = true },
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].MountPath = "/" },
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPath = "tenant" },
		func(p *core.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPathExpr = "$(TENANT)" },
		func(p *core.Pod) { p.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = "other" },
	} {
		bad := owner.DeepCopy()
		change(bad)
		if _, err := ownerHomePrefix(bad, cfg); err == nil {
			t.Fatal("accepted unsupported home mount")
		}
	}
	cfg.StellaHome = "/outside"
	if _, err := ownerHomePrefix(owner, cfg); err == nil {
		t.Fatal("accepted home outside PVC")
	}
}
