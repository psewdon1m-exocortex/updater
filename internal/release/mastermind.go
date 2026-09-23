package release

import (
	"errors"
	"regexp"
	"updater/internal/model"
)

var pinnedComponent = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]+@sha256:[a-f0-9]{64}$`)
var sourceRevision = regexp.MustCompile(`^[a-f0-9]{40}$`)
var artifactHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// This profile owns exactly three containers. Consumer requests cannot supply
// a fourth service, an image, a command, a repository or a host path.
func ValidateMastermind(manifest model.ReleaseManifest) error {
	group := manifest.Mastermind
	if manifest.Service != "mastermind" || group == nil || group.Profile != "mastermind.components.v1" ||
		group.Platform != "linux/amd64" || !sourceRevision.MatchString(group.SourceSHA) ||
		group.HealthProfile != "mastermind.functional.v1" || group.SavedCopyProtocol != 2 || group.BridgeVersion != manifest.Version ||
		!semver.MatchString(group.ObsidianVersion) || !artifactHash.MatchString(group.ModelSHA256) ||
		group.MinimumSourceSchema < 1 || group.MaximumSourceSchema < group.MinimumSourceSchema || manifest.DatabaseSchema < 1 {
		return errors.New("Mastermind release metadata is incomplete or incompatible")
	}
	if len(group.Components) != 3 {
		return errors.New("Mastermind requires exactly core, runtime and worker components")
	}
	for _, name := range []string{"core", "runtime", "worker"} {
		if !pinnedComponent.MatchString(group.Components[name]) {
			return errors.New("Mastermind components must have immutable SHA-256 digests")
		}
	}
	if group.Components["core"] != manifest.Image.Reference+"@"+manifest.Image.Digest {
		return errors.New("Mastermind primary image differs from its component group")
	}
	for _, name := range []string{"kernel", "volt", "saturn", "chronos", "neptune", "updater"} {
		if !semver.MatchString(group.Dependencies[name]) {
			return errors.New("Mastermind release requires the exact tested dependency tuple")
		}
	}
	return nil
}
