package plugins

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Contract versions describe the supported host surface, not the application
// release or the raw WASM calling convention (PluginABIVersion/2).
const BackendContractVersion = "1.0.0"
const FrontendContractVersion = "1.0.0"
const DefaultRegistryURL = "https://github.com/szilab/RunPilot/releases/download/plugin-catalog/catalog.json"

func ValidateVersion(version string) error {
	_, err := semver.StrictNewVersion(version)
	if err != nil {
		return fmt.Errorf("invalid semantic version %q: %w", version, err)
	}
	return nil
}

func CompareVersions(a, b string) int {
	av, ae := semver.StrictNewVersion(a)
	bv, be := semver.StrictNewVersion(b)
	if ae != nil || be != nil {
		return strings.Compare(a, b)
	} // invalid discovery entries sort before validation
	if n := av.Compare(bv); n != 0 {
		return n
	}
	return 0 // build metadata never implies an update
}

func CheckConstraint(constraint, host string) error {
	c, err := semver.NewConstraint(constraint)
	if err != nil || strings.TrimSpace(constraint) == "" {
		return fmt.Errorf("invalid contract constraint %q", constraint)
	}
	v, err := semver.StrictNewVersion(host)
	if err != nil {
		return err
	}
	if !c.Check(v) {
		return fmt.Errorf("requires %s; host is %s", constraint, host)
	}
	return nil
}

func (r Requires) Validate() error {
	for _, constraint := range []string{r.Backend, r.Frontend} {
		if constraint == "" {
			continue
		}
		if _, err := semver.NewConstraint(constraint); err != nil || strings.TrimSpace(constraint) == "" {
			return fmt.Errorf("invalid contract constraint %q", constraint)
		}
	}
	return nil
}

func (m Manifest) CompatibilityError(goos string) error {
	if m.Requires.RunPilotAPI != 0 && m.Requires.RunPilotAPI != PluginABIVersion && m.Requires.RunPilotAPI != PluginABIVersion2 {
		return fmt.Errorf("unsupported WASM ABI %d (host supports 1 and 2)", m.Requires.RunPilotAPI)
	}
	if len(m.Platforms) > 0 {
		found := false
		for _, p := range m.Platforms {
			if strings.EqualFold(p, goos) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("requires platform %s; host is %s", strings.Join(m.Platforms, ", "), goos)
		}
	}
	for _, item := range []struct{ name, requirement, host string }{{"backend", m.Requires.Backend, BackendContractVersion}, {"frontend", m.Requires.Frontend, FrontendContractVersion}} {
		if item.requirement != "" {
			if err := CheckConstraint(item.requirement, item.host); err != nil {
				return fmt.Errorf("%s contract: %w", item.name, err)
			}
		}
	}
	return nil
}
