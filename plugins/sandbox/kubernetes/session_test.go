package kubernetes

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/types"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestSubPath(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := subPath(root, child); err != nil || got != "child" {
		t.Fatalf("%q %v", got, err)
	}
	for _, source := range []string{root, filepath.Dir(root), filepath.Join(root, "missing")} {
		if _, err := subPath(root, source); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	if _, err := subPath(root, link); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestLive(t *testing.T) {
	if os.Getenv("STELLA_KUBERNETES_LIVE") != "1" {
		t.Skip("run mise run test:kubernetes -- --context <context>")
	}
	home := t.TempDir()
	bundle, err := os.Readlink("/opt/stella/skills/builtin")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Namespace: os.Getenv("STELLA_KUBERNETES_NAMESPACE"), OwnerName: os.Getenv("STELLA_KUBERNETES_POD_NAME"), OwnerUID: types.UID(os.Getenv("STELLA_KUBERNETES_POD_UID")), NodeName: os.Getenv("STELLA_KUBERNETES_NODE_NAME"), ServerURL: os.Getenv("STELLA_SANDBOX_SERVER_URL"), Deployment: "testbed", PVC: "home", Image: os.Getenv("STELLA_KUBERNETES_IMAGE"), StellaHome: home, BundleRevision: strings.TrimPrefix(bundle, "../bundles/")}
	client, err := NewInCluster(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	makeMode := func(t *testing.T, mode sandbox.NetworkMode) *session {
		t.Helper()
		dir, err := os.MkdirTemp(home, "workspace-")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := client.Factory(map[string]string{sandbox.MountWorkspace: dir}).CreateSession(t.Context(), sandbox.Policy{Network: sandbox.NetworkPolicy{Mode: mode}, Filesystem: sandbox.FilesystemPolicy{WorkingDir: "/workspace", Mounts: []sandbox.Mount{{SandboxPath: "/workspace", Access: sandbox.MountReadWrite}}}, Env: map[string]string{"SENTINEL": "secret-not-in-pod-spec"}})
		if err != nil {
			t.Fatal(err)
		}
		s := raw.(*session)
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
		return s
	}
	makeSession := func(t *testing.T) *session { return makeMode(t, sandbox.NetworkAllowAll) }
	storage := func(t *testing.T) {
		s := makeSession(t)
		spec, _ := json.Marshal(s.pod)
		if strings.Contains(string(spec), "secret-not-in-pod-spec") {
			t.Fatal("secret in PodSpec")
		}
		if s.pod.Spec.AutomountServiceAccountToken == nil || *s.pod.Spec.AutomountServiceAccountToken {
			t.Fatal("sandbox token enabled")
		}
		filename := "中 文/binary.bin"
		content := []byte{0, 1, 2, 255, 10}
		if err := s.Files().WriteFile(filename, content, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := s.Exec(t.Context(), "python3 -c 'from pathlib import Path; p=Path(\"中 文/binary.bin\"); b=p.read_bytes(); assert b==bytes([0,1,2,255,10]); p.write_bytes(b+b)'", sandbox.ExecOptions{})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%+v %v", result, err)
		}
		got, err := s.Files().ReadFile(filename)
		if err != nil || string(got) != string(append(content, content...)) {
			t.Fatalf("round trip %v %v", got, err)
		}
		temp, err := s.Files().ProjectTempFiles("projection", []sandbox.ProjectedFile{{Path: "hello", Content: []byte("projected"), Mode: 0o600}})
		if err != nil {
			t.Fatal(err)
		}
		result, err = s.Exec(t.Context(), "cat "+temp+"/hello", sandbox.ExecOptions{})
		if err != nil || result.Stdout != "projected" {
			t.Fatalf("projection %+v %v", result, err)
		}
		result, err = s.Exec(t.Context(), "test ! -e /data; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token; ln -s /etc/passwd escape", sandbox.ExecOptions{})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("isolation %+v %v", result, err)
		}
		if _, err = s.Files().ReadFile("escape"); err == nil {
			t.Fatal("symlink escape accepted")
		}
		other := makeSession(t)
		result, err = other.Exec(t.Context(), "test ! -e '中 文/binary.bin'", sandbox.ExecOptions{})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("principal isolation %+v %v", result, err)
		}
	}
	process := func(t *testing.T) {
		s := makeSession(t)
		result, err := s.Exec(t.Context(), "printf out; printf err >&2; exit 7", sandbox.ExecOptions{})
		if err != nil || result.ExitCode != 7 || result.Stdout != "out" || result.Stderr != "err" {
			t.Fatalf("exit %+v %v", result, err)
		}
		s.RefreshEnv(map[string]string{"REFRESH": "rotated"})
		result, err = s.Exec(t.Context(), "printf '%s:%s' \"$REFRESH\" \"$PWD\"", sandbox.ExecOptions{})
		if err != nil || result.Stdout != "rotated:/workspace" {
			t.Fatalf("env %+v %v", result, err)
		}
		handle, err := s.StartProcess(t.Context(), sandbox.ProcessRequest{Path: "/bin/cat"})
		if err != nil {
			t.Fatal(err)
		}
		out := make(chan string, 1)
		go func() { b, _ := io.ReadAll(handle.Stdout()); out <- string(b) }()
		go func() { _, _ = io.Copy(io.Discard, handle.Stderr()) }()
		if _, err = io.WriteString(handle.Stdin(), "stdin EOF\n"); err != nil {
			t.Fatal(err)
		}
		_ = handle.Stdin().Close()
		for range 2 {
			result, err = handle.Wait(t.Context())
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("wait %+v %v", result, err)
			}
		}
		if got := <-out; got != "stdin EOF\n" {
			t.Fatalf("stdin %q", got)
		}
		result, err = s.Exec(t.Context(), "python3 -c 'print(\"x\"*(11*1024*1024))'", sandbox.ExecOptions{})
		if err != nil || !strings.Contains(result.Stdout, "output truncated") {
			t.Fatalf("cap length=%d err=%v", len(result.Stdout), err)
		}
		result, err = s.Exec(t.Context(), "while true; do echo tick >> counter; sleep 0.1; done", sandbox.ExecOptions{Timeout: 2 * time.Second})
		if err == nil || !result.TimedOut || s.Alive() {
			t.Fatalf("timeout %+v %v alive=%v", result, err, s.Alive())
		}
		// The retained provider source proves writes stop after fencing the generation.
		counter := filepath.Join(s.sources["/workspace"], "counter")
		before, readErr := os.ReadFile(counter)
		if readErr != nil {
			t.Fatal(readErr)
		}
		time.Sleep(500 * time.Millisecond)
		after, readErr := os.ReadFile(counter)
		if readErr != nil || string(before) != string(after) {
			t.Fatal("writes continued after cancellation")
		}

		if _, err = s.Files().ReadFile("counter"); err == nil {
			t.Fatal("closed FileView still operational")
		}
	}
	network := func(t *testing.T) {
		listener, err := net.Listen("tcp", "0.0.0.0:25777")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "callback") }), ReadHeaderTimeout: time.Second}
		go func() { _ = server.Serve(listener) }()
		defer func() { _ = server.Close() }()
		command := `python3 -c 'import urllib.request; print(urllib.request.urlopen("http://stella-testbed:25777",timeout=2).read().decode())'`
		allowed := makeSession(t)
		result, err := allowed.Exec(t.Context(), command, sandbox.ExecOptions{})
		if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "callback" {
			t.Fatalf("callback %+v %v", result, err)
		}
		disabled := makeMode(t, sandbox.NetworkDisabled)
		result, err = disabled.Exec(t.Context(), command, sandbox.ExecOptions{})
		if err != nil || result.ExitCode == 0 {
			t.Fatalf("disabled callback %+v %v", result, err)
		}
		result, err = disabled.Exec(t.Context(), `test -z "$STELLA_SERVER_URL"`, sandbox.ExecOptions{})
		if err != nil || result.ExitCode != 0 {
			t.Fatal("disabled callback URL was injected")
		}
		// Default-deny must prevent both Kubernetes API and metadata connections.
		for _, target := range []string{os.Getenv("KUBERNETES_SERVICE_HOST"), "169.254.169.254"} {
			command := `python3 -c 'import socket; s=socket.socket(); s.settimeout(2); s.connect(("` + target + `",443))'`
			result, err = allowed.Exec(t.Context(), command, sandbox.ExecOptions{})
			if err != nil || result.ExitCode == 0 {
				t.Fatalf("forbidden egress %s: %+v %v", target, result, err)
			}
		}
	}
	recovery := func(t *testing.T) {
		s := makeSession(t)
		if err := s.Files().WriteFile("persist", []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := client.api.CoreV1().Pods(cfg.Namespace).Delete(t.Context(), s.pod.Name, meta.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-s.Done():
		case <-time.After(40 * time.Second):
			t.Fatal("deleted sandbox did not terminate")
		}
		p := s.Policy()
		p.Filesystem.Mounts = []sandbox.Mount{{SandboxPath: "/workspace", Access: sandbox.MountReadWrite}}
		replacement, err := client.Factory(s.sources).CreateSession(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := replacement.Close(); err != nil {
				t.Error(err)
			}
		}()
		result, err := replacement.Exec(t.Context(), "cat persist", sandbox.ExecOptions{})
		if err != nil || result.Stdout != "kept" {
			t.Fatalf("recovery %+v %v", result, err)
		}
	}
	for _, suite := range []string{"storage", "process", "all"} {
		t.Run(suite, func(t *testing.T) {
			if suite != "process" {
				t.Run("storage", storage)
			}
			if suite != "storage" {
				t.Run("process", process)
			}
			if suite == "all" {
				t.Run("network", network)
				t.Run("recovery", recovery)
			}
		})
	}
}
