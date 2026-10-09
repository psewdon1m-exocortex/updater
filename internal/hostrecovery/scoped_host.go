package hostrecovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type ScopedArchive struct {
	Scope string
	Bytes []byte
}

func runningUnit(unit string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
}

func recoveryUnits(includeUpdater bool) []string {
	result := []string{}
	if includeUpdater && runningUnit("updater.service") {
		result = append(result, "updater.service")
	}
	return append(result, runningHelpers()...)
}

func recordRecoveryUnits(unitsToRestore []string) error {
	body, _ := json.Marshal(unitsToRestore)
	if err := durableFile(activeHelpersFile, body); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(activeHelpersFile))
}

// ExportScopes creates one independently authenticated archive per service at
// a single quiesced recovery point. The caller owns remote publication.
func ExportScopes(password string) (archives []ScopedArchive, err error) {
	if PendingHostRecovery() {
		return nil, errors.New("an interrupted host recovery must be resumed first")
	}
	running := recoveryUnits(true)
	if err = recordRecoveryUnits(running); err != nil {
		return nil, err
	}
	defer func() {
		restartErr := units("start", running)
		err = errors.Join(err, restartErr)
		if restartErr == nil {
			err = errors.Join(err, os.Remove(activeHelpersFile))
		}
	}()
	if err = units("stop", running); err != nil {
		return nil, err
	}
	for _, scope := range RecoveryScopes {
		entries, collectErr := CollectScope("/", scope)
		if collectErr != nil {
			return nil, collectErr
		}
		sealed, sealErr := SealScope(entries, password, scope)
		for index := range entries {
			clear(entries[index].Data)
		}
		if sealErr != nil {
			return nil, fmt.Errorf("%s recovery export failed: %w", scope, sealErr)
		}
		archives = append(archives, ScopedArchive{Scope: scope, Bytes: sealed})
	}
	return archives, nil
}

// ExportScope quiesces and captures only one installed host service. It does
// not require any sibling helper to exist or to have Saturn credentials.
func ExportScope(password, scope string) (archive ScopedArchive, err error) {
	if !ValidScope(scope) {
		return ScopedArchive{}, errors.New("unknown helper recovery scope")
	}
	if PendingHostRecovery() {
		return ScopedArchive{}, errors.New("an interrupted host recovery must be resumed first")
	}
	unit := serviceUnit(scope)
	running := []string{}
	if runningUnit(unit) {
		running = append(running, unit)
	}
	if err = recordRecoveryUnits(running); err != nil {
		return ScopedArchive{}, err
	}
	defer func() {
		restartErr := units("start", running)
		err = errors.Join(err, restartErr)
		if restartErr == nil {
			err = errors.Join(err, os.Remove(activeHelpersFile))
		}
	}()
	if err = units("stop", running); err != nil {
		return ScopedArchive{}, err
	}
	entries, err := CollectScope("/", scope)
	if err != nil {
		return ScopedArchive{}, err
	}
	defer func() {
		for index := range entries {
			clear(entries[index].Data)
		}
	}()
	sealed, err := SealScope(entries, password, scope)
	if err != nil {
		return ScopedArchive{}, fmt.Errorf("%s recovery export failed: %w", scope, err)
	}
	return ScopedArchive{Scope: scope, Bytes: sealed}, nil
}

func serviceUnit(scope string) string {
	if scope == "updater" {
		return "updater.service"
	}
	return scope + ".service"
}

func RestoreScope(archive []byte, password, expectedScope string) error {
	if !ValidScope(expectedScope) {
		return errors.New("unknown helper recovery scope")
	}
	entries, err := OpenScope(archive, password, expectedScope)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	defer func() {
		for index := range entries {
			clear(entries[index].Data)
		}
	}()
	unit := serviceUnit(expectedScope)
	if _, err := os.Stat("/etc/systemd/system/" + unit); err != nil {
		return fmt.Errorf("install trusted %s binaries before recovery", expectedScope)
	}
	running := []string{}
	if runningUnit(unit) {
		running = append(running, unit)
	}
	if PendingHostRecovery() {
		return errors.New("an interrupted host recovery must be resumed first")
	}
	if err := recordRecoveryUnits(running); err != nil {
		return err
	}
	defer func() {
		if !PendingRecovery("/") {
			_ = os.Remove(activeHelpersFile)
		}
	}()
	if err := units("stop", running); err != nil {
		_ = units("start", running)
		return err
	}
	err = ApplyScoped("/", entries, expectedScope, func() error {
		if err := ownershipForRoots(scopedRoots[expectedScope]); err != nil {
			return err
		}
		if err := units("start", []string{unit}); err != nil {
			return err
		}
		if expectedScope != "updater" {
			if err := health(unit); err != nil {
				_ = units("stop", []string{unit})
				return err
			}
			if err := reconnectHeads(); err != nil {
				_ = units("stop", []string{unit})
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = ownershipForRoots(scopedRoots[expectedScope])
		_ = units("start", running)
		if expectedScope != "updater" {
			_ = reconnectHeads()
		}
		return err
	}
	return os.Remove(activeHelpersFile)
}
