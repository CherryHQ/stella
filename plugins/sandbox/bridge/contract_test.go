//go:build evalbridge && unix

// Contract test for the bridge backend against a real container served by the
// Python dev harness (test/evals/harbor/stella_harbor/bridge_dev.py). Requires
// docker and uv on PATH. Run with:
//
//	go test -tags evalbridge ./plugins/sandbox/bridge/ -run TestBridgeContract -v
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func TestBridgeContract(t *testing.T) {
	for _, bin := range []string{"docker", "uv"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// A bare container standing in for a benchmark task container.
	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--rm", "-w", "/app", "debian:13-slim", "sleep", "300").Output()
	if err != nil {
		t.Skipf("docker run failed (daemon down?): %v", err)
	}
	container := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	if err := exec.CommandContext(ctx, "docker", "exec", container, "mkdir", "-p", "/app").Run(); err != nil {
		t.Fatal(err)
	}

	// Short socket path: macOS caps sun_path at 104 bytes and t.TempDir is long.
	sockDir, err := os.MkdirTemp("/tmp", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	bindingDir := filepath.Join(sockDir, "bind")
	socket := filepath.Join(sockDir, "b.sock")
	const userID = "eval-user-1"

	py := exec.CommandContext(ctx, "uv", "run", "--project", filepath.Join(repoRoot(t), "test/evals/harbor"),
		"python", "-m", "stella_harbor.bridge_dev",
		"--container", container, "--workdir", "/app",
		"--binding-dir", bindingDir, "--user-id", userID, "--socket", socket)
	py.Stderr = os.Stderr
	// uv re-execs python; kill the whole group or the child keeps our stdout
	// pipe open and `go test` waits on it.
	py.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	py.WaitDelay = 5 * time.Second
	stdout, err := py.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := py.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-py.Process.Pid, syscall.SIGTERM)
		_, _ = py.Process.Wait()
	})
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "READY") {
				ready <- sc.Text()
				return
			}
		}
		close(ready)
	}()
	select {
	case line, ok := <-ready:
		if !ok {
			t.Fatal("bridge server exited before READY")
		}
		t.Log(line)
	case <-time.After(90 * time.Second):
		t.Fatal("bridge server did not become ready")
	}

	factory := NewFactory(Config{BindingDir: bindingDir, UserID: userID})
	policy := sandboxpkg.Policy{Env: map[string]string{"STELLA_TEST_MARK": "1"}}
	session, err := factory.CreateSession(ctx, policy)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if wd := session.WorkingDir(); wd != "/app" {
		t.Fatalf("working dir: got %q", wd)
	}
	env := session.Policy().Env
	if env["HOME"] == "" || env["TMPDIR"] == "" {
		t.Fatalf("policy env missing HOME/TMPDIR: %v", env)
	}
	execOptions := sandboxpkg.ExecOptions{Timeout: 30 * time.Second}

	// Exec: cwd, env passthrough, exit code, stderr.
	res, err := session.Exec(ctx, `pwd; echo "$STELLA_TEST_MARK"; echo err >&2; exit 3`, execOptions)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.HasPrefix(res.Stdout, "/app\n1\n") || res.ExitCode != 3 || !strings.Contains(res.Stderr, "err") {
		t.Fatalf("exec result mismatch: %+v", res)
	}

	files := session.Files()

	// WriteFile / ReadFile round trip incl. binary, unicode path, relative name.
	payload := []byte{0, 1, 2, 255, 'x', '\n'}
	if err := files.WriteFile("子目录/数据.bin", payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := files.ReadFile("/app/子目录/数据.bin")
	if err != nil || string(got) != string(payload) {
		t.Fatalf("read back: %v %q", err, got)
	}
	res, _ = session.Exec(ctx, `stat -c %a "/app/子目录/数据.bin"`, execOptions)
	if strings.TrimSpace(res.Stdout) != "600" {
		t.Fatalf("mode not applied: %q", res.Stdout)
	}

	// Stat + ReadDir.
	info, err := files.Stat("子目录")
	if err != nil || !info.IsDir {
		t.Fatalf("stat dir: %v %+v", err, info)
	}
	info, err = files.Stat("子目录/数据.bin")
	if err != nil || info.IsDir || info.Size != int64(len(payload)) {
		t.Fatalf("stat file: %v %+v", err, info)
	}
	entries, err := files.ReadDir("/app")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name == "子目录" && e.IsDir {
			found = true
		}
	}
	if !found {
		t.Fatalf("readdir missing entry: %+v", entries)
	}

	// Missing file → fs.ErrNotExist so core tools report it normally.
	if _, err := files.ReadFile("/app/nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: want ErrNotExist, got %v", err)
	}
	if _, err := files.Stat("/app/nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing stat: want ErrNotExist, got %v", err)
	}

	// Projection: publish, idempotent republish, conflict on different content.
	tree := []sandboxpkg.ProjectedFile{
		{Path: "SKILL.md", Content: []byte("# skill\n"), Mode: 0o644},
		{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
	}
	if err := files.ProjectFiles("skills/demo", tree); err != nil {
		t.Fatalf("project: %v", err)
	}
	if err := files.ProjectFiles("skills/demo", tree); err != nil {
		t.Fatalf("project idempotent: %v", err)
	}
	tree2 := append([]sandboxpkg.ProjectedFile(nil), tree...)
	tree2[0].Content = []byte("# changed\n")
	if err := files.ProjectFiles("skills/demo", tree2); !errors.Is(err, sandboxpkg.ErrProjectionConflict) {
		t.Fatalf("project conflict: want ErrProjectionConflict, got %v", err)
	}
	res, _ = session.Exec(ctx, `/app/skills/demo/scripts/run.sh`, execOptions)
	if strings.TrimSpace(res.Stdout) != "hi" || res.ExitCode != 0 {
		t.Fatalf("projected script not executable: %+v", res)
	}
	visible, err := files.ProjectTempFiles("tmpskill", tree)
	if err != nil || !strings.HasPrefix(visible, env["TMPDIR"]) {
		t.Fatalf("project temp: %v %q", err, visible)
	}
	if _, err := files.ReadFile(visible + "/SKILL.md"); err != nil {
		t.Fatalf("temp projection unreadable: %v", err)
	}

	// Nonce mismatch: a session bound with a different nonce must be refused.
	raw, _ := os.ReadFile(filepath.Join(bindingDir, userID+".json"))
	var b map[string]any
	_ = json.Unmarshal(raw, &b)
	b["nonce"] = "wrong"
	bad, _ := json.Marshal(b)
	if err := os.WriteFile(filepath.Join(bindingDir, "other-user.json"), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFactory(Config{BindingDir: bindingDir, UserID: "other-user"}).CreateSession(ctx, policy); err == nil || !errors.Is(err, errBadNonce) {
		t.Fatalf("wrong nonce: want errBadNonce, got %v", err)
	}
	// No binding at all: refuse, never fall back.
	if _, err := NewFactory(Config{BindingDir: bindingDir, UserID: "nobody"}).CreateSession(ctx, policy); !errors.Is(err, ErrNoBinding) {
		t.Fatalf("no binding: want ErrNoBinding, got %v", err)
	}
	// Group sessions are outside the eval scope.
	if _, err := NewFactory(Config{BindingDir: bindingDir, UserID: userID, GroupID: "g"}).CreateSession(ctx, policy); err == nil {
		t.Fatal("group session must be refused")
	}
}
