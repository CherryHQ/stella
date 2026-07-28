//go:build releaseplatform

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/vault"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const (
	kindNodeImage = "kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f"
)

func runDockerSmoke(
	_ string,
	run releasecontract.Run,
	manifest releasecontract.CandidateManifest,
	platform releasecontract.Platform,
	attempt int,
	log io.Writer,
) (returnErr error) {
	if err := requireNativeLinux(platform); err != nil {
		return err
	}
	imageRef, err := candidateImageRef(manifest, platform)
	if err != nil {
		return err
	}
	resource := releaseResourceName(run.ID, platform.Arch, attempt)
	networkName := resource + "-net"
	containerName := resource + "-docker"
	if exec.Command("docker", "inspect", containerName).Run() == nil ||
		exec.Command("docker", "network", "inspect", networkName).Run() == nil {
		return fmt.Errorf("refusing pre-existing Docker release-test resource %s", resource)
	}

	var (
		networkCreated   bool
		containerCreated bool
		postgres         *appdb.Embedded
	)
	tempDir, err := os.MkdirTemp("", resource+"-*")
	if err != nil {
		return err
	}
	defer func() {
		// A Docker command can return an error after creating its resource. The
		// preflight above proved any matching object found here belongs to this
		// test, so existence checks safely close that partial-failure gap.
		containerExists := containerCreated || exec.Command("docker", "inspect", containerName).Run() == nil
		if returnErr != nil && containerExists {
			_ = runLogged(log, "docker", "logs", containerName)
		}
		if containerExists {
			returnErr = errors.Join(returnErr, runLogged(log, "docker", "rm", "--force", containerName))
		}
		if postgres != nil {
			returnErr = errors.Join(returnErr, postgres.Stop())
		}
		networkExists := networkCreated || exec.Command("docker", "network", "inspect", networkName).Run() == nil
		if networkExists {
			returnErr = errors.Join(returnErr, runLogged(log, "docker", "network", "rm", networkName))
		}
		if err := os.RemoveAll(tempDir); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}()

	if err := runLogged(log, "docker", "pull", imageRef); err != nil {
		return err
	}
	if err := runLogged(log, "docker", "network", "create", networkName); err != nil {
		return err
	}
	networkCreated = true
	gateway, err := outputLogged(log, "docker", "network", "inspect", "--format", "{{(index .IPAM.Config 0).Gateway}}", networkName)
	if err != nil {
		return err
	}
	subnet, err := outputLogged(log, "docker", "network", "inspect", "--format", "{{(index .IPAM.Config 0).Subnet}}", networkName)
	if err != nil {
		return err
	}
	postgres, err = appdb.StartTestNetworkEmbedded(gateway, subnet, 0)
	if err != nil {
		return fmt.Errorf("start pinned Stella PostgreSQL runtime: %w", err)
	}
	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		return fmt.Errorf("generate deployment vault key: %w", err)
	}
	envPath := filepath.Join(tempDir, "container.env")
	envData := strings.Join([]string{
		"STELLA_DATABASE_URL=" + postgres.DSNForHost(gateway, "stella"),
		"STELLA_VAULT_KEY=" + vaultKey,
		"STELLA_SANDBOX_BACKEND=none",
		"STELLA_HTTP_SHUTDOWN_TIMEOUT=5s",
		"STELLA_RIVER_SOFT_STOP_TIMEOUT=10s",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(envData), 0o600); err != nil {
		return fmt.Errorf("write Docker test environment: %w", err)
	}
	if err := runLogged(
		log,
		"docker",
		"run",
		"--detach",
		"--name", containerName,
		"--network", networkName,
		"--env-file", envPath,
		"--publish", "127.0.0.1::25678",
		imageRef,
	); err != nil {
		return err
	}
	containerCreated = true

	portOutput, err := outputLogged(log, "docker", "port", containerName, "25678/tcp")
	if err != nil {
		return err
	}
	port, err := mappedPort(portOutput)
	if err != nil {
		return err
	}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	if err := waitReady(baseURL, 3*time.Minute); err != nil {
		return err
	}
	if err := assertCandidateStatus(baseURL, run); err != nil {
		return err
	}
	if err := assertCandidateMigrations(postgres.DSN()); err != nil {
		return err
	}

	if err := runLogged(log, "docker", "stop", "--time", "30", containerName); err != nil {
		return err
	}
	exitCode, err := outputLogged(log, "docker", "inspect", "--format", "{{.State.ExitCode}}", containerName)
	if err != nil {
		return err
	}
	if exitCode != "0" {
		return fmt.Errorf("candidate Docker container exited with code %s", exitCode)
	}
	if err := runLogged(log, "docker", "rm", containerName); err != nil {
		return err
	}
	containerCreated = false
	if err := postgres.Stop(); err != nil {
		return err
	}
	postgres = nil
	if err := runLogged(log, "docker", "network", "rm", networkName); err != nil {
		return err
	}
	networkCreated = false
	if exec.Command("docker", "inspect", containerName).Run() == nil {
		return fmt.Errorf("candidate Docker container %s remains after cleanup", containerName)
	}
	if exec.Command("docker", "network", "inspect", networkName).Run() == nil {
		return fmt.Errorf("candidate Docker network %s remains after cleanup", networkName)
	}
	return nil
}

func runHelmSmoke(
	root string,
	run releasecontract.Run,
	manifest releasecontract.CandidateManifest,
	platform releasecontract.Platform,
	attempt int,
	log io.Writer,
) (returnErr error) {
	if platform != (releasecontract.Platform{OS: "linux", Arch: "amd64"}) {
		return fmt.Errorf("helm release smoke runs only on native linux/amd64, got %s/%s", platform.OS, platform.Arch)
	}
	if err := requireNativeLinux(platform); err != nil {
		return err
	}
	imageRef, err := candidateImageRef(manifest, platform)
	if err != nil {
		return err
	}
	resource := releaseResourceName(run.ID, platform.Arch, attempt)
	clusterName := resource + "-kind"
	contextName := "kind-" + clusterName
	namespace := "stella-release"
	kindNetworkPreexisting := exec.Command("docker", "network", "inspect", "kind").Run() == nil

	var (
		clusterCreated bool
		postgres       *appdb.Embedded
		portForward    *exec.Cmd
	)
	defer func() {
		if !clusterCreated {
			// kind can fail after creating the cluster container but before its
			// command returns success. The earlier preflight proved this name was
			// absent, so a newly listed cluster is safe for this test to delete.
			if clusters, err := exec.Command("kind", "get", "clusters").Output(); err == nil {
				clusterCreated = linePresent(string(clusters), clusterName)
			}
		}
		if returnErr != nil && clusterCreated {
			_ = runLogged(log, "kubectl", "--context", contextName, "-n", namespace, "get", "all", "-o", "wide")
			_ = runLogged(log, "kubectl", "--context", contextName, "-n", namespace, "logs", "deployment/stella", "--all-containers")
		}
		if portForward != nil && portForward.Process != nil {
			_ = portForward.Process.Kill()
			_ = portForward.Wait()
		}
		if clusterCreated {
			returnErr = errors.Join(returnErr, runLogged(log, "kind", "delete", "cluster", "--name", clusterName))
		}
		if postgres != nil {
			returnErr = errors.Join(returnErr, postgres.Stop())
		}
		if !kindNetworkPreexisting && exec.Command("docker", "network", "inspect", "kind").Run() == nil {
			returnErr = errors.Join(returnErr, runLogged(log, "docker", "network", "rm", "kind"))
		}
	}()

	clusters, err := outputLogged(log, "kind", "get", "clusters")
	if err != nil {
		return err
	}
	if linePresent(clusters, clusterName) {
		return fmt.Errorf("kind cluster %s already exists", clusterName)
	}
	if err := runLogged(
		log,
		"kind",
		"create", "cluster",
		"--name", clusterName,
		"--image", kindNodeImage,
		"--wait", "180s",
	); err != nil {
		return err
	}
	clusterCreated = true
	if err := runLogged(log, "docker", "pull", imageRef); err != nil {
		return err
	}
	if err := runLogged(log, "kind", "load", "docker-image", imageRef, "--name", clusterName); err != nil {
		return err
	}
	gateway, err := outputLogged(log, "docker", "network", "inspect", "--format", "{{(index .IPAM.Config 0).Gateway}}", "kind")
	if err != nil {
		return err
	}
	subnet, err := outputLogged(log, "docker", "network", "inspect", "--format", "{{(index .IPAM.Config 0).Subnet}}", "kind")
	if err != nil {
		return err
	}
	postgres, err = appdb.StartTestNetworkEmbedded(gateway, subnet, 0)
	if err != nil {
		return fmt.Errorf("start pinned Stella PostgreSQL runtime: %w", err)
	}
	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		return err
	}

	if err := runLogged(log, "kubectl", "--context", contextName, "create", "namespace", namespace); err != nil {
		return err
	}
	secret, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]string{
			"name":      "stella-secrets",
			"namespace": namespace,
		},
		"type": "Opaque",
		"stringData": map[string]string{
			"STELLA_VAULT_KEY":    vaultKey,
			"STELLA_DATABASE_URL": postgres.DSNForHost(gateway, "stella"),
		},
	})
	if err != nil {
		return err
	}
	if err := runLoggedInput(
		log,
		bytes.NewReader(secret),
		"kubectl",
		"--context", contextName,
		"apply", "-f", "-",
	); err != nil {
		return err
	}
	imageName, imageDigest, ok := strings.Cut(imageRef, "@")
	if !ok {
		return fmt.Errorf("candidate image %q is not digest-pinned", imageRef)
	}
	chartPath := filepath.Join(root, "deploy", "helm", "stella")
	if err := runLogged(
		log,
		"helm",
		"upgrade", "--install", "stella", chartPath,
		"--kube-context", contextName,
		"--namespace", namespace,
		"--set-string", "baseURL=http://stella.test",
		"--set-string", "secrets.existingSecret=stella-secrets",
		"--set-string", "sandbox.backend=none",
		"--set", "sandbox.allowUnsafeHostExecution=true",
		"--set", "persistence.enabled=false",
		"--set", "persistence.allowEphemeralDataLoss=true",
		"--set-string", "image.repository="+imageName,
		"--set-string", "image.digest="+imageDigest,
		"--set-string", "image.pullPolicy=IfNotPresent",
		"--set", "shutdown.preStopSeconds=1",
		"--set", "shutdown.httpSeconds=5",
		"--set", "shutdown.riverSoftStopSeconds=10",
		"--set", "shutdown.terminationGracePeriodSeconds=26",
		"--wait",
		"--timeout", "5m",
	); err != nil {
		return err
	}
	if err := runLogged(
		log,
		"kubectl",
		"--context", contextName,
		"-n", namespace,
		"rollout", "status", "deployment/stella",
		"--timeout=3m",
	); err != nil {
		return err
	}
	podImage, err := outputLogged(
		log,
		"kubectl",
		"--context", contextName,
		"-n", namespace,
		"get", "pod",
		"-l", "app.kubernetes.io/name=stella",
		"-o", "jsonpath={.items[0].spec.containers[0].image}",
	)
	if err != nil {
		return err
	}
	if podImage != imageRef {
		return fmt.Errorf("helm pod image is %q, want exact candidate %q", podImage, imageRef)
	}
	imageID, err := outputLogged(
		log,
		"kubectl",
		"--context", contextName,
		"-n", namespace,
		"get", "pod",
		"-l", "app.kubernetes.io/name=stella",
		"-o", "jsonpath={.items[0].status.containerStatuses[0].imageID}",
	)
	if err != nil {
		return err
	}
	if !strings.Contains(imageID, imageDigest) {
		return fmt.Errorf("helm pod imageID %q does not contain candidate digest %s", imageID, imageDigest)
	}

	localPort, err := freeTCPPort()
	if err != nil {
		return err
	}
	portForward = exec.Command(
		"kubectl",
		"--context", contextName,
		"-n", namespace,
		"port-forward", "service/stella",
		fmt.Sprintf("%d:25678", localPort),
		"--address=127.0.0.1",
	)
	portForward.Stdout = log
	portForward.Stderr = log
	if err := portForward.Start(); err != nil {
		return fmt.Errorf("start Helm port-forward: %w", err)
	}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(localPort)
	if err := waitReady(baseURL, 90*time.Second); err != nil {
		return err
	}
	if err := assertCandidateStatus(baseURL, run); err != nil {
		return err
	}
	if err := assertCandidateMigrations(postgres.DSN()); err != nil {
		return err
	}
	_ = portForward.Process.Kill()
	_ = portForward.Wait()
	portForward = nil

	if err := runLogged(
		log,
		"helm",
		"uninstall", "stella",
		"--kube-context", contextName,
		"--namespace", namespace,
		"--wait",
		"--timeout", "2m",
	); err != nil {
		return err
	}
	if err := runLogged(
		log,
		"kubectl",
		"--context", contextName,
		"delete", "namespace", namespace,
		"--wait=true",
		"--timeout=2m",
	); err != nil {
		return err
	}
	if err := runLogged(log, "kind", "delete", "cluster", "--name", clusterName); err != nil {
		return err
	}
	clusterCreated = false
	if err := postgres.Stop(); err != nil {
		return err
	}
	postgres = nil
	if !kindNetworkPreexisting && exec.Command("docker", "network", "inspect", "kind").Run() == nil {
		if err := runLogged(log, "docker", "network", "rm", "kind"); err != nil {
			return err
		}
	}
	clusters, err = outputLogged(log, "kind", "get", "clusters")
	if err != nil {
		return err
	}
	if linePresent(clusters, clusterName) {
		return fmt.Errorf("kind cluster %s remains after cleanup", clusterName)
	}
	return nil
}

func requireNativeLinux(platform releasecontract.Platform) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != platform.Arch || platform.OS != "linux" {
		return fmt.Errorf(
			"deployment smoke requires native %s/%s, runner is %s/%s",
			platform.OS,
			platform.Arch,
			runtime.GOOS,
			runtime.GOARCH,
		)
	}
	return nil
}

func candidateImageRef(
	manifest releasecontract.CandidateManifest,
	platform releasecontract.Platform,
) (string, error) {
	key := platform.OS + "/" + platform.Arch
	for _, image := range manifest.Images {
		if image.Platform == key {
			return image.Name + "@" + image.Digest, nil
		}
	}
	return "", fmt.Errorf("candidate image for %s is missing", key)
}

func releaseResourceName(runID, arch string, attempt int) string {
	sum := sha256.Sum256([]byte(runID))
	return fmt.Sprintf("stella-release-%s-%s-a%d", hex.EncodeToString(sum[:4]), arch, attempt)
}

func runLogged(log io.Writer, name string, args ...string) error {
	// Arguments are deliberately omitted because deployment commands can point
	// at temporary files containing credentials. The full command output remains
	// available without ever echoing a secret-bearing argument.
	_, _ = fmt.Fprintf(log, "$ %s [arguments omitted]\n", name)
	cmd := exec.Command(name, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func runLoggedInput(log io.Writer, input io.Reader, name string, args ...string) error {
	_, _ = fmt.Fprintf(log, "$ %s [arguments and stdin omitted]\n", name)
	cmd := exec.Command(name, args...)
	cmd.Stdin = input
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func outputLogged(log io.Writer, name string, args ...string) (string, error) {
	_, _ = fmt.Fprintf(log, "$ %s [arguments omitted]\n", name)
	cmd := exec.Command(name, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s failed: %w", name, err)
	}
	value := strings.TrimSpace(output.String())
	_, _ = fmt.Fprintln(log, value)
	return value, nil
}

func mappedPort(output string) (int, error) {
	line := strings.TrimSpace(strings.Split(output, "\n")[0])
	index := strings.LastIndexByte(line, ':')
	if index < 0 {
		return 0, fmt.Errorf("unexpected Docker port output %q", output)
	}
	port, err := strconv.Atoi(line[index+1:])
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("unexpected Docker port output %q", output)
	}
	return port, nil
}

func waitReady(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(baseURL + "/readyz")
		if err == nil {
			ok := resp.StatusCode == http.StatusOK
			_ = resp.Body.Close()
			if ok {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("candidate did not become ready within %s", timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func assertCandidateStatus(baseURL string, run releasecontract.Run) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/status")
	if err != nil {
		return fmt.Errorf("GET candidate status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET candidate status returned %d", resp.StatusCode)
	}
	var status struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("decode candidate status: %w", err)
	}
	if status.Status != "ok" {
		return fmt.Errorf("candidate status is %q", status.Status)
	}
	if status.Version != run.Version && status.Version != strings.TrimPrefix(run.Version, "v") {
		return fmt.Errorf("candidate version is %q, want %q", status.Version, run.Version)
	}
	if status.Commit == "" || !strings.HasPrefix(run.Commit, status.Commit) {
		return fmt.Errorf("candidate commit is %q, want prefix of %s", status.Commit, run.Commit)
	}
	return nil
}

func assertCandidateMigrations(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to deployment PostgreSQL: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM goose_db_version").Scan(&count); err != nil {
		return fmt.Errorf("query candidate migrations: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("candidate did not record any database migration")
	}
	return nil
}

func freeTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func linePresent(value, want string) bool {
	for line := range strings.SplitSeq(value, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
