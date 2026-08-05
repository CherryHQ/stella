package fsops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

func TestPublishManagedSkillCreatesAndReusesExactRevision(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	files := []sandbox.ManagedSkillTreeEntry{
		streamTreeEntry(".stella-skill.json", 0o644, []byte("{}\n")),
		streamTreeEntry("SKILL.md", 0o644, []byte("---\nname: example\n---\nbody\n")),
		streamTreeEntry("nested/blob", 0o640, []byte{0, 1, 2, 255}),
	}
	digest, err := sandbox.DigestManagedSkillTreeV1(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PublishManagedSkillAt(context.Background(), ".", "example", digest, sandbox.ManagedSkillPublication{Files: files}); err != nil {
		t.Fatal(err)
	}
	assertManagedSkillTarget(t, r, "example", digest)
	if err := r.PublishManagedSkillAt(context.Background(), ".", "example", digest, sandbox.ManagedSkillPublication{Files: files}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.root.Open(managedSkillRevisionPath("example", digest) + "/nested/blob"); err != nil {
		t.Fatal(err)
	}
}

func TestPublishManagedSkillPostLinkTamperIsOutcomeUnknown(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	files := []sandbox.ManagedSkillTreeEntry{streamTreeEntry(".stella-skill.json", 0o644, []byte("{}\n")), streamTreeEntry("SKILL.md", 0o644, []byte("body"))}
	digest, err := sandbox.DigestManagedSkillTreeV1(files)
	if err != nil {
		t.Fatal(err)
	}
	r.afterManagedSkillRename = func() {
		f, e := r.root.OpenFile(managedSkillRevisionPath("skill", digest)+"/SKILL.md", os.O_WRONLY|os.O_TRUNC, 0)
		if e == nil {
			_, _ = f.Write([]byte("evil"))
			_ = f.Close()
		}
	}
	err = r.PublishManagedSkillAt(context.Background(), ".", "skill", digest, sandbox.ManagedSkillPublication{Files: files})
	if !sandbox.IsOutcomeUnknown(err) {
		t.Fatalf("post-link tamper = %v", err)
	}
}

func TestPublishManagedSkillSyncsNestedCatalogAncestors(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	files := managedSkillFiles("nested")
	digest, err := sandbox.DigestManagedSkillTreeV1(files)
	if err != nil {
		t.Fatal(err)
	}
	var synced []string
	r.syncManagedDirectory = func(p string) { synced = append(synced, p) }
	if err := r.PublishManagedSkillAt(context.Background(), "a/b", "skill", digest, sandbox.ManagedSkillPublication{Files: files}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a/b", "a", "."} {
		found := slices.Contains(synced, want)
		if !found {
			t.Fatalf("missing ancestor sync %q: %v", want, synced)
		}
	}
}

func TestPublishManagedSkillRefusesOrdinaryCatalogDirectory(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	if err := r.root.Mkdir("ordinary", 0o755); err != nil {
		t.Fatal(err)
	}
	file := streamTreeEntry("SKILL.md", 0o644, []byte("x"))
	digest, _ := sandbox.DigestManagedSkillTreeV1([]sandbox.ManagedSkillTreeEntry{file})
	if err := r.PublishManagedSkillAt(context.Background(), ".", "ordinary", digest, sandbox.ManagedSkillPublication{Files: []sandbox.ManagedSkillTreeEntry{file}}); err == nil {
		t.Fatal("ordinary directory replaced")
	}
}

func TestPublishManagedSkillWrongDigestLeavesNoPublicationArtifacts(t *testing.T) {
	r, directory := managedSkillTestRoot(t)
	files := managedSkillFiles("body")
	if err := r.PublishManagedSkillAt(context.Background(), ".", "skill", strings.Repeat("0", 64), sandbox.ManagedSkillPublication{Files: files}); err == nil {
		t.Fatal("wrong digest accepted")
	}
	if _, managed, err := r.ManagedSkillTarget(context.Background(), "skill"); err != nil || managed {
		t.Fatalf("wrong digest created an active target: managed=%t err=%v", managed, err)
	}
	assertNoManagedSkillTemps(t, directory)
	if entries, err := os.ReadDir(filepath.Join(directory, ".stella-revisions", "skill")); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".stage-") {
				t.Fatalf("stage remains: %s", entry.Name())
			}
		}
	}
}

func TestPublishManagedSkillUpdateRetainsOldRevision(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	oldFiles := managedSkillFiles("old")
	oldDigest, err := sandbox.DigestManagedSkillTreeV1(oldFiles)
	if err != nil {
		t.Fatal(err)
	}
	newFiles := managedSkillFiles("new")
	newDigest, err := sandbox.DigestManagedSkillTreeV1(newFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		digest string
		files  []sandbox.ManagedSkillTreeEntry
	}{{oldDigest, oldFiles}, {newDigest, newFiles}} {
		if err := r.PublishManagedSkillAt(context.Background(), ".", "skill", tc.digest, sandbox.ManagedSkillPublication{Files: tc.files}); err != nil {
			t.Fatal(err)
		}
	}
	assertManagedSkillTarget(t, r, "skill", newDigest)
	for _, digest := range []string{oldDigest, newDigest} {
		if _, err := r.root.Open(managedSkillRevisionPath("skill", digest) + "/SKILL.md"); err != nil {
			t.Fatalf("revision %s missing: %v", digest, err)
		}
	}
}

func TestPublishManagedSkillNestedShortCatalogRootAndReservedRoot(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	files := managedSkillFiles("nested")
	digest, err := sandbox.DigestManagedSkillTreeV1(files)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PublishManagedSkillAt(context.Background(), "a/b", "skill", digest, sandbox.ManagedSkillPublication{Files: files}); err != nil {
		t.Fatal(err)
	}
	got, managed, err := r.ManagedSkillTargetAt(context.Background(), "a/b", "skill")
	if err != nil || !managed || got != digest {
		t.Fatalf("nested target=%q managed=%t err=%v", got, managed, err)
	}
	for _, root := range []string{".stella-revisions", ".stella-skill-target-x", ".stage-x"} {
		if err := r.PublishManagedSkillAt(context.Background(), root, "skill", digest, sandbox.ManagedSkillPublication{Files: files}); err == nil {
			t.Fatalf("reserved root %q accepted", root)
		}
	}
}

func TestPublishManagedSkillConcurrentPublishersKeepValidRevisions(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	filesA, filesB := managedSkillFiles("A"), managedSkillFiles("B")
	digestA, _ := sandbox.DigestManagedSkillTreeV1(filesA)
	digestB, _ := sandbox.DigestManagedSkillTreeV1(filesB)
	var wg sync.WaitGroup
	results := make(chan struct {
		digest string
		err    error
	}, 4)
	for range 2 {
		wg.Add(1)
		go func(d string, files []sandbox.ManagedSkillTreeEntry) {
			defer wg.Done()
			results <- struct {
				digest string
				err    error
			}{d, r.PublishManagedSkillAt(context.Background(), ".", "skill", d, sandbox.ManagedSkillPublication{Files: files})}
		}(digestA, filesA)
		wg.Add(1)
		go func(d string, files []sandbox.ManagedSkillTreeEntry) {
			defer wg.Done()
			results <- struct {
				digest string
				err    error
			}{d, r.PublishManagedSkillAt(context.Background(), ".", "skill", d, sandbox.ManagedSkillPublication{Files: files})}
		}(digestB, filesB)
	}
	wg.Wait()
	close(results)
	active, managed, err := r.ManagedSkillTarget(context.Background(), "skill")
	if err != nil || !managed || (active != digestA && active != digestB) {
		t.Fatalf("active=%q managed=%t err=%v", active, managed, err)
	}
	for result := range results {
		if result.err != nil && !sandbox.IsOutcomeUnknown(result.err) {
			continue
		}
		if err := r.verifyPublishedTree(context.Background(), managedSkillRevisionPath("skill", result.digest), result.digest); err != nil {
			t.Fatalf("revision %s invalid: %v", result.digest, err)
		}
	}
}

func TestPublishManagedSkillHonorsDeclaredModesUnderUmask(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	old := setTestUmask(0o077)
	t.Cleanup(func() { setTestUmask(old) })
	files := managedSkillFiles("mode")
	digest, _ := sandbox.DigestManagedSkillTreeV1(files)
	if err := r.PublishManagedSkillAt(context.Background(), ".", "skill", digest, sandbox.ManagedSkillPublication{Files: files}); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		info, err := r.root.Stat(managedSkillRevisionPath("skill", digest) + "/" + file.Path)
		if err != nil || info.Mode().Perm() != file.Mode.Perm() {
			t.Fatalf("%s mode=%o err=%v want %o", file.Path, info.Mode().Perm(), err, file.Mode.Perm())
		}
	}
}

func TestPublishManagedSkillTamperedRevisionFailsBoundedly(t *testing.T) {
	r, _ := managedSkillTestRoot(t)
	files := managedSkillFiles("tamper")
	digest, _ := sandbox.DigestManagedSkillTreeV1(files)
	revision := filepath.Join(".stella-revisions", "skill", digest)
	if err := r.root.MkdirAll(revision, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.root.Symlink("missing", revision+"/evil"); err != nil {
		t.Fatal(err)
	}
	for i := range 513 {
		if err := r.root.Mkdir(filepath.Join(revision, "d"+fmt.Sprint(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.PublishManagedSkillAt(context.Background(), ".", "skill", digest, sandbox.ManagedSkillPublication{Files: files}); err == nil {
		t.Fatal("tampered oversized revision accepted")
	}
}

func TestValidateManagedSkillPublicationRejectsOversizeBeforeOpening(t *testing.T) {
	opened := false
	files := managedSkillFiles("oversize")
	files[0].Length = 8<<20 + 1
	files[0].Open = func() (io.ReadCloser, error) { opened = true; return io.NopCloser(strings.NewReader("x")), nil }
	if err := ValidateManagedSkillPublication(sandbox.ManagedSkillPublication{Files: files}); err == nil || opened {
		t.Fatalf("err=%v opened=%t", err, opened)
	}
}

func managedSkillFiles(body string) []sandbox.ManagedSkillTreeEntry {
	return []sandbox.ManagedSkillTreeEntry{streamTreeEntry(".stella-skill.json", 0o644, []byte("{}\n")), streamTreeEntry("SKILL.md", 0o644, []byte(body)), streamTreeEntry("nested/blob", 0o640, []byte("blob"))}
}

func streamTreeEntry(name string, mode uint32, body []byte) sandbox.ManagedSkillTreeEntry {
	return sandbox.ManagedSkillTreeEntry{Path: name, Mode: sandboxMode(mode), Length: int64(len(body)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }}
}
func sandboxMode(mode uint32) fs.FileMode { return fs.FileMode(mode) }
