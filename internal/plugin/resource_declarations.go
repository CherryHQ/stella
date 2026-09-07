package plugin

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateResourceDeclarations checks complete release resources and, when
// supplied, the release-owned OAuth provider index. Scoped configuration
// ownership is enforced separately by ValidatePayload.
func ValidateResourceDeclarations(payload ResourcePayload, name string, providerIDs map[string]struct{}) error {
	if err := validateResources(payload, name, true, true); err != nil {
		return err
	}
	if providerIDs == nil {
		return nil
	}
	var errs []error
	if payload.OAuthProvider != "" {
		if _, ok := providerIDs[payload.OAuthProvider]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown oauth_provider %q", name, payload.OAuthProvider))
		}
	}
	for _, requirement := range payload.OAuth {
		if _, ok := providerIDs[requirement.Provider]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown OAuth provider %q", name, requirement.Provider))
		}
	}
	return errors.Join(errs...)
}

func validateCompleteResources(payload ResourcePayload, name string) error {
	var errs []error
	for i, binary := range payload.Binaries {
		if binary.Name == "" {
			errs = append(errs, fmt.Errorf("%s binary[%d]: name is required", name, i))
		}
		if binary.Tool == "" {
			errs = append(errs, fmt.Errorf("%s binary[%d]: tool is required (e.g. uv or github:owner/repo)", name, i))
		}
	}
	for i, skill := range payload.Skills {
		if skill.Name == "" {
			errs = append(errs, fmt.Errorf("%s skill[%d]: name is required", name, i))
		}
	}
	for i, env := range payload.SessionEnvs {
		if env.EnvVar == "" {
			errs = append(errs, fmt.Errorf("%s session_env[%d]: env_var is required", name, i))
		}
		if env.Source == "" {
			errs = append(errs, fmt.Errorf("%s session_env[%d]: source is required", name, i))
		}
		if strings.HasPrefix(env.Source, "oauth.") && payload.OAuthProvider == "" && !hasOAuthEnvBinding(payload.OAuth, env.EnvVar) {
			errs = append(errs, fmt.Errorf("%s session_env[%d]: oauth source requires oauth_provider", name, i))
		}
	}
	return errors.Join(errs...)
}

func hasOAuthEnvBinding(requirements []OAuthRequirement, envVar string) bool {
	for _, requirement := range requirements {
		for _, binding := range requirement.Bindings {
			if binding.EnvVar == envVar {
				return true
			}
		}
	}
	return false
}

func validateOAuthBindings(p ResourcePayload) []error {
	var errs []error
	envBindings := make(map[string]struct{})
	for _, requirement := range p.OAuth {
		if requirement.Provider == "" {
			errs = append(errs, fmt.Errorf("oauth requirement needs a provider"))
		}
		for _, scope := range requirement.Scopes {
			if scope == "" {
				errs = append(errs, fmt.Errorf("oauth requirement contains an empty scope"))
			}
			if err := validateString(scope, "OAuth scope"); err != nil {
				errs = append(errs, err)
			}
		}
		for _, binding := range requirement.Bindings {
			if (binding.EnvVar == "") == (binding.Connection == "") {
				errs = append(errs, fmt.Errorf("oauth binding requires exactly one env_var or connection"))
				continue
			}
			if !knownOAuthField(binding.Credential) {
				errs = append(errs, fmt.Errorf("unknown OAuth credential %q", binding.Credential))
			}
			if binding.EnvVar != "" {
				if err := validateString(binding.EnvVar, "OAuth env var"); err != nil {
					errs = append(errs, err)
				}
				if _, duplicate := envBindings[binding.EnvVar]; duplicate {
					errs = append(errs, fmt.Errorf("duplicate OAuth env binding %q", binding.EnvVar))
				}
				envBindings[binding.EnvVar] = struct{}{}
				for _, env := range p.SessionEnvs {
					if env.EnvVar == binding.EnvVar && env.Source != "oauth."+binding.Credential {
						errs = append(errs, fmt.Errorf("oauth binding for %q does not match session_env source", binding.EnvVar))
					}
				}
			} else {
				errs = append(errs, fmt.Errorf("OAuth connection bindings are not supported; configure authentication on the MCP child"))
			}
		}
	}
	return errs
}
