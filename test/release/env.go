package release

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	// EnvRunID carries the release Run ID into every runner.
	EnvRunID = "STELLA_RELEASE_RUN_ID"
	// EnvVersion carries the candidate version into every runner.
	EnvVersion = "STELLA_RELEASE_VERSION"
	// EnvCommit carries the immutable candidate commit into every runner.
	EnvCommit = "STELLA_RELEASE_COMMIT"
	// EnvSecretNames lists comma-separated environment variable names whose
	// values must not appear in release diagnostics.
	EnvSecretNames = "STELLA_RELEASE_SECRET_ENVS"
)

// RunFromEnv loads the shared Run identity. It returns present=false when none
// of the release variables are set and rejects partially configured runners.
func RunFromEnv() (run Run, present bool, err error) {
	// Keep literal names here so the repository's environment-read audit can
	// verify this release-only exception precisely.
	values := map[string]string{
		EnvRunID:   os.Getenv("STELLA_RELEASE_RUN_ID"),
		EnvVersion: os.Getenv("STELLA_RELEASE_VERSION"),
		EnvCommit:  os.Getenv("STELLA_RELEASE_COMMIT"),
	}
	var set, missing []string
	for name, value := range values {
		if value == "" {
			missing = append(missing, name)
			continue
		}
		set = append(set, name)
	}
	if len(set) == 0 {
		return Run{}, false, nil
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Run{}, false, fmt.Errorf("release metadata is incomplete; missing %s", strings.Join(missing, ", "))
	}
	run = Run{ID: values[EnvRunID], Version: values[EnvVersion], Commit: values[EnvCommit]}
	if err := run.Validate(); err != nil {
		return Run{}, false, fmt.Errorf("validate release metadata: %w", err)
	}
	return run, true, nil
}

// SecretNamesFromEnv returns the configured comma-separated secret variable
// names. Parsing remains in the runner so command-line overrides behave the
// same as environment configuration.
func SecretNamesFromEnv() string {
	return os.Getenv("STELLA_RELEASE_SECRET_ENVS")
}

// SecretValuesFromEnv resolves secret values from an explicit list of
// environment variable names without including any value in an error.
func SecretValuesFromEnv(names []string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, fmt.Errorf("secret environment variable name cannot be empty")
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("secret environment variable %s is listed more than once", name)
		}
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			return nil, fmt.Errorf("secret environment variable %s is missing or empty", name)
		}
		values[name] = value
	}
	return values, nil
}
