package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var recoveryTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var recoveryServices = []string{"updater", "neptune", "gryphon", "wyvern"}

type RecoveryIdentity struct {
	Slug  string
	Token string
}

func SaveRecoveryStorage(runtime Runtime, gatewayURL string, identities map[string]RecoveryIdentity) error {
	if !validHTTPS(gatewayURL) {
		return errors.New("recovery Gateway URL must be HTTPS without userinfo, query or fragment")
	}
	if len(identities) == 0 {
		return errors.New("at least one recovery identity is required")
	}
	for service, identity := range identities {
		known := false
		for _, candidate := range recoveryServices {
			known = known || service == candidate
		}
		if !known {
			return fmt.Errorf("unknown recovery service %q", service)
		}
		if !hostIdentifier.MatchString(identity.Slug) {
			return fmt.Errorf("%s backup producer slug is invalid", service)
		}
		if !recoveryTokenPattern.MatchString(identity.Token) {
			return fmt.Errorf("%s backup producer token is invalid", service)
		}
	}
	configuration, err := LoadHost(runtime)
	if err != nil {
		return err
	}
	if configuration.RecoveryGatewayURL != "" && strings.TrimRight(gatewayURL, "/") != configuration.RecoveryGatewayURL {
		return errors.New("recovery Gateway differs from the configured service identities")
	}
	if configuration.RecoveryTokenFiles == nil {
		configuration.RecoveryTokenFiles = map[string]string{}
	}
	if configuration.RecoverySlugs == nil {
		configuration.RecoverySlugs = map[string]string{}
	}
	directory := filepath.Join(filepath.Dir(HostConfigFile(runtime)), "recovery-tokens")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	files := configuration.RecoveryTokenFiles
	slugs := configuration.RecoverySlugs
	for service, identity := range identities {
		filename := filepath.Join(directory, service+".token")
		staged, err := os.CreateTemp(directory, "."+service+"-*")
		if err != nil {
			return err
		}
		name := staged.Name()
		if err = staged.Chmod(0o600); err == nil {
			_, err = staged.WriteString(identity.Token + "\n")
		}
		if err == nil {
			err = staged.Sync()
		}
		if closeErr := staged.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, filename)
		}
		_ = os.Remove(name)
		if err != nil {
			return err
		}
		files[service] = filename
		slugs[service] = identity.Slug
	}
	configuration.RecoveryGatewayURL = strings.TrimRight(gatewayURL, "/")
	configuration.RecoveryTokenFiles = files
	configuration.RecoverySlugs = slugs
	return SaveHost(runtime, configuration)
}

func RecoveryToken(configuration HostConfig, service string) (string, error) {
	filename := configuration.RecoveryTokenFiles[service]
	if filename == "" {
		return "", fmt.Errorf("%s recovery producer token is not configured", service)
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o037 != 0 {
		return "", fmt.Errorf("%s recovery producer token file must be owned by the Updater user, regular and private", service)
	}
	body, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(body))
	if !recoveryTokenPattern.MatchString(token) {
		return "", fmt.Errorf("%s recovery producer token is invalid", service)
	}
	return token, nil
}
