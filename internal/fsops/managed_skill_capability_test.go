package fsops

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestFilesystemInspectManagedSkillTargetUsesCanonicalCoordinate(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	managedSkillRevision(t, directory, "skill", managedDigestA, "old")
	if err := r.SwapManagedSkillTarget(context.Background(), "skill", managedDigestA); err != nil {
		t.Fatal(err)
	}
	filesystem, err := NewFilesystem([]Mount{{Path: sandbox.PathWorkspace, Directory: directory}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = filesystem.Close() })
	target, err := filesystem.InspectManagedSkillTarget(context.Background(), "/workspace/skill")
	if err != nil || !target.Managed || target.Digest != managedDigestA {
		t.Fatalf("target = %+v, %v", target, err)
	}
	if _, err := filesystem.InspectManagedSkillTarget(context.Background(), "/workspace/../skill"); err == nil {
		t.Fatal("noncanonical path accepted")
	}
}

func TestManagedSkillTargetHelperProtocolFailsClosed(t *testing.T) {
	for name, response := range map[string]Response{
		"unmanaged digest": {Version: ProtocolVersion, Kind: KindManagedSkillTarget, Digest: managedDigestA},
		"bad digest":       {Version: ProtocolVersion, Kind: KindManagedSkillTarget, Managed: true, Digest: "bad"},
		"payload":          {Version: ProtocolVersion, Kind: KindManagedSkillTarget, Managed: true, Digest: managedDigestA, BodyLength: 1},
	} {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var wire bytes.Buffer
			if err := writeFrame(&wire, payload); err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeResponse(&wire, KindManagedSkillTarget); err == nil {
				t.Fatal("malformed managed target response accepted")
			}
		})
	}
	request := Request{Version: ProtocolVersion, Operation: "managed_skill_target", Path: "skill"}
	encoded, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Serve(context.Background(), t.TempDir(), bytes.NewReader(encoded), &output); err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(&output, KindManagedSkillTarget)
	if err != nil || response.Managed || response.Digest != "" {
		t.Fatalf("unmanaged helper response = %+v, %v", response, err)
	}
}

func TestHelperRejectsManagedFieldsOutsideManagedTargetSchema(t *testing.T) {
	for _, kind := range []string{KindRead, KindStat, KindList, KindMutation} {
		t.Run(kind, func(t *testing.T) {
			payload := []byte(`{"version":1,"kind":"` + kind + `","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
			var wire bytes.Buffer
			if err := writeFrame(&wire, payload); err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeResponse(&wire, kind); err == nil {
				t.Fatal("managed field accepted by " + kind)
			}
		})
	}
	var wire bytes.Buffer
	if err := writeFrame(&wire, []byte(`{"version":1,"kind":"managed_skill_target","unknown":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResponse(&wire, KindManagedSkillTarget); err == nil {
		t.Fatal("unknown response field accepted")
	}
}
