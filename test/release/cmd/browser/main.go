//go:build linux

// Command browser runs Stella's blocking release Browser E2E suite against one
// exact candidate binary and emits the shared release Result contract.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/pgruntime"
	"github.com/CherryHQ/stella/internal/vault"
	releasecontract "github.com/CherryHQ/stella/test/release"
)

const (
	browserReadyTimeout = 120 * time.Second
	browserTestTimeout  = 15 * time.Minute
)

type browserCredentials struct {
	AdminEmail     string
	AdminPassword  string
	UserEmail      string
	UserPassword   string
	UserID         string
	SignupEmail    string
	SignupPassword string
	FixturePrefix  string
	SecretProbe    string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "release browser test failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	rootFlag := flag.String("root", "", "absolute repository root (defaults to auto-detection)")
	flag.Parse()
	if runtime.GOARCH != "amd64" {
		return fmt.Errorf("release Browser E2E is defined only for linux/amd64, got linux/%s", runtime.GOARCH)
	}

	root, err := repositoryRoot(*rootFlag)
	if err != nil {
		return err
	}
	runIdentity, err := browserRunIdentity(root)
	if err != nil {
		return err
	}
	attempt, err := browserAttempt()
	if err != nil {
		return err
	}
	binary, err := candidateBinary(root)
	if err != nil {
		return err
	}
	runtimeRoot, err := installedPostgresRuntime()
	if err != nil {
		return err
	}

	runDir := releasecontract.RunDirectory(root, runIdentity.ID)
	artifactRoot := filepath.Join(runDir, "artifacts", "browser")
	if err := os.MkdirAll(artifactRoot, 0o755); err != nil {
		return fmt.Errorf("create browser artifact directory: %w", err)
	}
	probeSuffix, err := randomHex(8)
	if err != nil {
		return fmt.Errorf("generate browser secret probe: %w", err)
	}
	secretProbe := "Release-Secret-Probe-" + probeSuffix + "-Z9!"

	outcomes, executionErr := executeBrowser(
		root,
		runIdentity,
		attempt,
		binary,
		runtimeRoot,
		artifactRoot,
		secretProbe,
	)
	if scanErr := scanBrowserArtifacts(artifactRoot, map[string]string{
		"STELLA_E2E_SECRET_PROBE": secretProbe,
	}); scanErr != nil {
		redactionPath, replaceErr := replaceUnsafeArtifacts(runDir, artifactRoot, scanErr)
		executionErr = errors.Join(executionErr, scanErr, replaceErr)
		if replaceErr == nil {
			now := time.Now().UTC()
			outcomes = failureOutcomes(now, now, "browser artifact secret scan failed; unsafe diagnostics were removed", []string{redactionPath})
		}
	}

	writeErr := writeBrowserResults(root, runIdentity, attempt, outcomes)
	finalErr := errors.Join(executionErr, writeErr)
	if writeErr == nil {
		for _, outcome := range outcomes {
			if outcome.Status != releasecontract.StatusPass {
				finalErr = errors.Join(finalErr, fmt.Errorf("%s finished with status %s", outcome.Definition.ScenarioID, outcome.Status))
			}
		}
	}
	if finalErr != nil {
		return finalErr
	}
	fmt.Printf("release browser test passed: %s (%d Scenarios)\n", runIdentity.ID, len(outcomes))
	return nil
}

func executeBrowser(
	root string,
	runIdentity releasecontract.Run,
	attempt int,
	binary string,
	runtimeRoot string,
	artifactRoot string,
	secretProbe string,
) ([]scenarioOutcome, error) {
	startedAt := time.Now().UTC()
	serverLog := filepath.Join(artifactRoot, fmt.Sprintf("stellad-a%03d.log", attempt))
	playwrightLog := filepath.Join(artifactRoot, fmt.Sprintf("playwright-a%03d.log", attempt))
	rawReportPath := filepath.Join(artifactRoot, fmt.Sprintf("playwright-report-a%03d.json", attempt))
	outputDir := filepath.Join(artifactRoot, fmt.Sprintf("playwright-a%03d", attempt))
	fakeSummaryPath := filepath.Join(artifactRoot, fmt.Sprintf("fake-anthropic-summary-a%03d.json", attempt))

	fake, err := startFakeAnthropic()
	if err != nil {
		return failureOutcomes(startedAt, time.Now().UTC(), err.Error(), nil), err
	}

	home, err := os.MkdirTemp("", "stella-release-browser-home-*")
	if err != nil {
		closeErr := fake.Close()
		return failureOutcomes(startedAt, time.Now().UTC(), err.Error(), nil), errors.Join(err, closeErr)
	}
	defer func() { _ = os.RemoveAll(home) }()

	vaultKey, err := vault.GenerateMasterIdentity()
	if err != nil {
		closeErr := fake.Close()
		return failureOutcomes(startedAt, time.Now().UTC(), err.Error(), nil), errors.Join(err, closeErr)
	}
	process, baseURL, startErr := startCandidate(binary, root, home, runtimeRoot, vaultKey, serverLog)

	var credentials browserCredentials
	var setupErr error
	if startErr == nil {
		setupErr = verifyCandidateSPA(baseURL)
	}
	if startErr == nil && setupErr == nil {
		credentials, setupErr = bootstrapBrowserUsers(baseURL, secretProbe)
	}
	var playwrightErr error
	if startErr == nil && setupErr == nil {
		playwrightErr = runPlaywright(root, baseURL, fake.URL(), credentials, outputDir, rawReportPath, playwrightLog)
	}

	var stopErr error
	if process != nil {
		stopErr = process.Stop()
	}
	summaryErr := writeExclusiveJSON(fakeSummaryPath, fake.Summary())
	fakeCloseErr := fake.Close()
	harnessErr := errors.Join(startErr, setupErr, stopErr, summaryErr, fakeCloseErr)

	sharedPaths := existingPaths(serverLog, playwrightLog, rawReportPath, fakeSummaryPath)
	if startErr != nil || setupErr != nil {
		reason := errors.Join(startErr, setupErr, stopErr, summaryErr, fakeCloseErr)
		return failureOutcomes(startedAt, time.Now().UTC(), reason.Error(), sharedPaths), reason
	}

	report, reportErr := loadRawReport(rawReportPath)
	if reportErr != nil {
		reason := errors.Join(playwrightErr, reportErr, harnessErr)
		return failureOutcomes(startedAt, time.Now().UTC(), reason.Error(), sharedPaths), reason
	}
	outcomes, structureErr := outcomesFromReport(report, attempt, sharedPaths, harnessErr)
	return outcomes, errors.Join(playwrightErr, harnessErr, structureErr)
}

func bootstrapBrowserUsers(baseURL, secretProbe string) (browserCredentials, error) {
	suffix, err := randomHex(6)
	if err != nil {
		return browserCredentials{}, err
	}
	credentials := browserCredentials{
		AdminEmail:     "release-admin-" + suffix + "@example.invalid",
		AdminPassword:  "Release-Admin-" + suffix + "-A1!",
		UserEmail:      "release-user-" + suffix + "@example.invalid",
		UserPassword:   "Release-User-" + suffix + "-A1!",
		SignupEmail:    "release-signup-" + suffix + "@example.invalid",
		SignupPassword: "Release-Signup-" + suffix + "-A1!",
		FixturePrefix:  "rb-" + suffix,
		SecretProbe:    secretProbe,
	}
	adminClient, err := newCookieClient()
	if err != nil {
		return browserCredentials{}, err
	}
	if _, err := registerBrowserUser(adminClient, baseURL, "Release Browser Admin", credentials.AdminEmail, credentials.AdminPassword); err != nil {
		return browserCredentials{}, fmt.Errorf("register browser admin: %w", err)
	}
	userClient, err := newCookieClient()
	if err != nil {
		return browserCredentials{}, err
	}
	userID, err := registerBrowserUser(userClient, baseURL, "Release Browser User", credentials.UserEmail, credentials.UserPassword)
	if err != nil {
		return browserCredentials{}, fmt.Errorf("register browser user: %w", err)
	}
	credentials.UserID = userID
	return credentials, nil
}

func verifyCandidateSPA(baseURL string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(baseURL + "/login")
	if err != nil {
		return fmt.Errorf("load candidate SPA: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("candidate SPA returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read candidate SPA: %w", err)
	}
	// A Go-only local build deliberately serves a tiny fallback shell. Browser
	// evidence is valid only when the candidate embeds the production JS bundle.
	if !bytes.Contains(body, []byte("<script")) || !bytes.Contains(body, []byte("/assets/")) {
		return fmt.Errorf("candidate does not embed the built Web SPA; build web before compiling stellad")
	}
	return nil
}

func registerBrowserUser(client *http.Client, baseURL, name, email, password string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"name": name, "email": email, "password": password, "confirm_password": password,
	})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/auth/local/register", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registration returned HTTP %d", response.StatusCode)
	}
	if closeErr != nil {
		return "", closeErr
	}

	response, err = client.Get(baseURL + "/api/auth/me")
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /api/auth/me returned HTTP %d", response.StatusCode)
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&me); err != nil {
		return "", err
	}
	if me.ID == "" {
		return "", fmt.Errorf("registered user has an empty id")
	}
	return me.ID, nil
}

func runPlaywright(
	root string,
	baseURL string,
	fakeProviderURL string,
	credentials browserCredentials,
	outputDir string,
	reportPath string,
	logPath string,
) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create Playwright output directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create Playwright log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), browserTestTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "vp", "exec", "playwright", "test", "--config=playwright.config.ts")
	command.Dir = filepath.Join(root, "web")
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = append(browserBaseEnv(),
		"CI=true",
		"STELLA_E2E_BASE_URL="+baseURL,
		"STELLA_E2E_OUTPUT_DIR="+outputDir,
		"STELLA_E2E_REPORT_PATH="+reportPath,
		"STELLA_E2E_ADMIN_EMAIL="+credentials.AdminEmail,
		"STELLA_E2E_ADMIN_PASSWORD="+credentials.AdminPassword,
		"STELLA_E2E_USER_EMAIL="+credentials.UserEmail,
		"STELLA_E2E_USER_PASSWORD="+credentials.UserPassword,
		"STELLA_E2E_USER_ID="+credentials.UserID,
		"STELLA_E2E_SIGNUP_EMAIL="+credentials.SignupEmail,
		"STELLA_E2E_SIGNUP_PASSWORD="+credentials.SignupPassword,
		"STELLA_E2E_FAKE_PROVIDER_URL="+fakeProviderURL,
		"STELLA_E2E_FIXTURE_PREFIX="+credentials.FixturePrefix,
		"STELLA_E2E_SECRET_PROBE="+credentials.SecretProbe,
	)
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("playwright exceeded %s", browserTestTimeout)
		}
		return fmt.Errorf("playwright failed; see %s: %w", logPath, err)
	}
	return nil
}

func browserRunIdentity(root string) (releasecontract.Run, error) {
	runIdentity, present, err := releasecontract.RunFromEnv()
	if err != nil || present {
		return runIdentity, err
	}
	commitBytes, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return releasecontract.Run{}, fmt.Errorf("resolve local candidate commit: %w", err)
	}
	suffix, err := randomHex(4)
	if err != nil {
		return releasecontract.Run{}, err
	}
	runIdentity = releasecontract.Run{
		ID:      "browser-" + time.Now().UTC().Format("20060102T150405") + "-" + suffix,
		Version: "browser-local",
		Commit:  strings.TrimSpace(string(commitBytes)),
	}
	if err := runIdentity.Validate(); err != nil {
		return releasecontract.Run{}, err
	}
	return runIdentity, nil
}

func browserAttempt() (int, error) {
	raw := strings.TrimSpace(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if raw == "" {
		return 1, nil
	}
	attempt, err := strconv.Atoi(raw)
	if err != nil || attempt < 1 {
		return 0, fmt.Errorf("GITHUB_RUN_ATTEMPT must be a positive integer")
	}
	return attempt, nil
}

func newCookieClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}, nil
}

func randomHex(byteCount int) (string, error) {
	data := make([]byte, byteCount)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func installedPostgresRuntime() (string, error) {
	source, ok := pgruntime.DefaultRuntimeSource()
	if !ok {
		return "", fmt.Errorf("no embedded PostgreSQL runtime is published for %s/%s: %s", runtime.GOOS, runtime.GOARCH, pgruntime.MissingRuntimeHint())
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	root := pgruntime.RuntimeRoot(filepath.Join(userHome, ".stella"), source)
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("PostgreSQL runtime is not installed at %s; run `mise run pg:runtime:download`: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("PostgreSQL runtime path %s is not a directory", root)
	}
	return root, nil
}
