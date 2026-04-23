package manifestplugins

import (
	"errors"
	"fmt"
)

var knownSources = map[string]struct{}{
	"static":            {},
	"github_token":      {},
	"lark_access_token": {},
	"lark_app_id":       {},
	"lark_brand":        {},
}

func Validate(m *Manifest) error {
	var errs []error
	for i, p := range m.Plugins {
		if p.ID == "" {
			errs = append(errs, fmt.Errorf("plugin[%d]: id is required", i))
		}
		if len(p.Binaries) == 0 && len(p.Skills) == 0 && len(p.SessionEnvs) == 0 {
			errs = append(errs, fmt.Errorf("plugin %q: must have at least one of binaries, skills, or session_env", p.ID))
		}
		for j, b := range p.Binaries {
			if b.Name == "" {
				errs = append(errs, fmt.Errorf("plugin %q binary[%d]: name is required", p.ID, j))
			}
			if b.Repo == "" {
				errs = append(errs, fmt.Errorf("plugin %q binary[%d]: repo is required", p.ID, j))
			}
			if len(b.AssetTemplates) == 0 {
				errs = append(errs, fmt.Errorf("plugin %q binary[%d]: at least one asset_template is required", p.ID, j))
			}
		}
		for j, s := range p.Skills {
			if s.Repo == "" {
				errs = append(errs, fmt.Errorf("plugin %q skill[%d]: repo is required", p.ID, j))
			}
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
			} else if _, ok := knownSources[se.Source]; !ok {
				errs = append(errs, fmt.Errorf("plugin %q session_env[%d]: unknown source %q", p.ID, j, se.Source))
			}
		}
	}
	return errors.Join(errs...)
}
