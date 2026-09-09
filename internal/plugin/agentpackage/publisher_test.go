package agentpackage

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPublishDirectoryPublishesFixtureAndHashesScriptContents(t *testing.T) {
	source := copyFixture(t, "plain")
	destination := t.TempDir()

	base, err := PublishDirectory(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if base.Digest == "" || base.Root == "" || base.Package == nil {
		t.Fatalf("published package = %#v, want digest, root, and parsed package", base)
	}
	if base.Package.Manifest.Name != "acme.tools" || len(base.Package.Skills) != 1 {
		t.Fatalf("published package = %#v, want plain fixture contents", base.Package)
	}

	withScript := copyFixture(t, "plain")
	scriptPath := filepath.Join(withScript, "scripts", "setup.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const script = "#!/bin/sh\nprintf '%s\\n' setup\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	withScriptPublished, err := PublishDirectory(withScript, destination)
	if err != nil {
		t.Fatal(err)
	}
	if withScriptPublished.Digest == base.Digest {
		t.Fatalf("adding script kept digest %q", base.Digest)
	}
	got, err := os.ReadFile(filepath.Join(withScriptPublished.Root, "scripts", "setup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != script {
		t.Fatalf("published script = %q, want %q", got, script)
	}
}

func TestPublishDirectorySourceChangesDoNotMutatePublishedPackage(t *testing.T) {
	source := copyFixture(t, "plain")
	destination := t.TempDir()

	first, err := PublishDirectory(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "skills", "acme-tools", "SKILL.md")
	changed := "---\nname: acme-tools\ndescription: changed source\n---\n\n# changed\n"
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := PublishDirectory(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == second.Digest {
		t.Fatalf("source mutation kept digest %q", first.Digest)
	}
	published, err := os.ReadFile(filepath.Join(first.Root, "skills", "acme-tools", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(published) == changed {
		t.Fatal("source mutation changed the already published directory")
	}
}

func TestPublishDirectoryRejectsSymlink(t *testing.T) {
	source := copyFixture(t, "plain")
	if err := os.Symlink(filepath.Join("..", "plugin.json"), filepath.Join(source, "linked.json")); err != nil {
		t.Fatal(err)
	}

	_, err := PublishDirectory(source, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink publish error = %v, want symlink rejection", err)
	}
}

func TestPublishDirectoryRejectsSingleFileOverLimit(t *testing.T) {
	source := copyFixture(t, "plain")
	largePath := filepath.Join(source, "scripts", "large.bin")
	if err := os.MkdirAll(filepath.Dir(largePath), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPublishedFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = PublishDirectory(source, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized file publish error = %v, want size-limit rejection", err)
	}
}

func TestPublishDirectoryRejectsTamperedExistingDigestPath(t *testing.T) {
	source := copyFixture(t, "plain")
	destination := t.TempDir()
	published, err := PublishDirectory(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(published.Root, "plugin.json"), []byte(`{"tampered":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := PublishDirectory(source, destination); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("republish after tamper error = %v, want digest mismatch", err)
	}
}

func TestPublishDirectoryConcurrentSameSourceReusesDigest(t *testing.T) {
	source := copyFixture(t, "plain")
	destination := t.TempDir()
	const workers = 8

	results := make(chan PublishedPackage, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			published, err := PublishDirectory(source, destination)
			if err != nil {
				errs <- err
				return
			}
			results <- published
		}()
	}
	group.Wait()
	close(results)
	close(errs)

	if len(errs) != 0 {
		var messages []string
		for err := range errs {
			messages = append(messages, err.Error())
		}
		t.Fatalf("concurrent publish errors = %s", strings.Join(messages, "; "))
	}
	var digest, root string
	for published := range results {
		if digest == "" {
			digest, root = published.Digest, published.Root
			continue
		}
		if published.Digest != digest || published.Root != root {
			t.Fatalf("concurrent publish result = %#v, want digest/root %s/%s", published, digest, root)
		}
	}
}
