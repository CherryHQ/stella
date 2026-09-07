package manifest

import (
	"errors"
	"fmt"
	"strings"
)

var knownStaticSources = map[string]struct{}{
	"static": {},
}

func isValidSource(src string, providerIDs map[string]struct{}) bool {
	if _, ok := knownStaticSources[src]; ok {
		return true
	}
	if strings.HasPrefix(src, "oauth.") {
		// source is "oauth.<field>"; no further validation needed here
		return true
	}
	return false
}

func Validate(m *Manifest) error {
	var errs []error

	providerIDs := make(map[string]struct{}, len(m.OAuthProviders))
	for i, op := range m.OAuthProviders {
		if op.ID == "" {
			errs = append(errs, fmt.Errorf("oauth_provider[%d]: id is required", i))
		} else {
			if _, dup := providerIDs[op.ID]; dup {
				errs = append(errs, fmt.Errorf("oauth_provider[%d]: duplicate id %q", i, op.ID))
			}
			providerIDs[op.ID] = struct{}{}
		}
		if op.VaultKey == "" {
			errs = append(errs, fmt.Errorf("oauth_provider %q: vault_key is required", op.ID))
		}
		if len(op.Flows) == 0 {
			errs = append(errs, fmt.Errorf("oauth_provider %q: at least one flow is required", op.ID))
		}
		for j, f := range op.Flows {
			if f.Type == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: type is required", op.ID, j))
			} else if f.Type != "authorization_code" && f.Type != "device_code" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: unknown type %q", op.ID, j, f.Type))
			}
			if f.TokenURL == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: token_url is required", op.ID, j))
			}
			if f.Type == "authorization_code" && f.AuthURL == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: auth_url is required for authorization_code", op.ID, j))
			}
			if f.Type == "device_code" && f.DeviceAuthURL == "" {
				errs = append(errs, fmt.Errorf("oauth_provider %q flow[%d]: device_auth_url is required for device_code", op.ID, j))
			}
		}
	}

	errs = append(errs, validatePlugins(m.Plugins, providerIDs)...)
	return errors.Join(errs...)
}

// validatePlugins checks plugin declarations without requiring the provider
// document. Standard packages may contain metadata alone; Validate
// passes the central provider index so the full manifest still checks refs.
func validatePlugins(plugins []ManifestPlugin, providerIDs map[string]struct{}) []error {
	var errs []error
	for i, p := range plugins {
		if p.ID == "" {
			errs = append(errs, fmt.Errorf("plugin[%d]: id is required", i))
		}
		if p.Kind != "agent" && len(p.Binaries) == 0 && len(p.BundledBinaries) == 0 && len(p.Skills) == 0 && len(p.SessionEnvs) == 0 && len(p.OAuth) == 0 && len(p.MCPServers) == 0 && p.Prompt == "" {
			errs = append(errs, fmt.Errorf("plugin %q: must have at least one of binaries, bundled_binaries, skills, session_env, oauth, mcp_servers, or prompt", p.ID))
		}
		for j, b := range p.Binaries {
			if b.Name == "" {
				errs = append(errs, fmt.Errorf("plugin %q binary[%d]: name is required", p.ID, j))
			}
			if b.Tool == "" {
				errs = append(errs, fmt.Errorf("plugin %q binary[%d]: tool is required (e.g. uv or github:owner/repo)", p.ID, j))
			}
		}
		for j, name := range p.BundledBinaries {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
				errs = append(errs, fmt.Errorf("plugin %q bundled binary[%d]: unsafe name %q", p.ID, j, name))
			}
		}
		for j, s := range p.Skills {
			if s.Name == "" {
				errs = append(errs, fmt.Errorf("plugin %q skill[%d]: name is required", p.ID, j))
			}
		}
		for j, se := range p.SessionEnvs {
			if se.EnvVar == "" {
				errs = append(errs, fmt.Errorf("plugin %q session_env[%d]: env_var is required", p.ID, j))
			}
			if se.Source == "" {
				errs = append(errs, fmt.Errorf("plugin %q session_env[%d]: source is required", p.ID, j))
			} else if !isValidSource(se.Source, providerIDs) {
				errs = append(errs, fmt.Errorf("plugin %q session_env[%d]: unknown source %q", p.ID, j, se.Source))
			}
			if strings.HasPrefix(se.Source, "oauth.") && p.OAuthProvider == "" && !hasOAuthEnvBinding(p.OAuth, se.EnvVar) {
				errs = append(errs, fmt.Errorf("plugin %q session_env[%d]: oauth source requires oauth_provider", p.ID, j))
			}
		}
		errs = append(errs, validateOAuthBindings(p.ManifestPluginDefinition, providerIDs)...)
		if p.OAuthProvider != "" && providerIDs != nil {
			if _, ok := providerIDs[p.OAuthProvider]; !ok {
				errs = append(errs, fmt.Errorf("plugin %q: unknown oauth_provider %q", p.ID, p.OAuthProvider))
			}
		}

	}
	return errs
}

func hasOAuthEnvBinding(requirements []ManifestOAuthRequirement, envVar string) bool {
	for _, requirement := range requirements {
		for _, binding := range requirement.Bindings {
			if binding.EnvVar == envVar {
				return true
			}
		}
	}
	return false
}

func validateOAuthBindings(p ManifestPluginDefinition, providers map[string]struct{}) []error {
	var errs []error
	envBindings := make(map[string]struct{})
	for _, requirement := range p.OAuth {
		if requirement.Provider == "" {
			errs = append(errs, fmt.Errorf("oauth requirement needs a provider"))
		} else if providers != nil {
			if _, ok := providers[requirement.Provider]; !ok {
				errs = append(errs, fmt.Errorf("unknown OAuth provider %q", requirement.Provider))
			}
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
