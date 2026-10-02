package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"updater/internal/config"
)

type missingImageRunner struct {
	pulled bool
	wrong  bool
	calls  []string
}

func (r *missingImageRunner) Run(_ context.Context, name string, args, _ []string, _ string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if name != "docker" {
		return nil, errors.New("unexpected command")
	}
	if len(args) > 0 && args[0] == "pull" {
		r.pulled = true
		return nil, nil
	}
	if len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
		if !r.pulled {
			return nil, errors.New("absent")
		}
		letter := "a"
		if r.wrong {
			letter = "b"
		}
		return []byte("sha256:" + strings.Repeat(letter, 64)), nil
	}
	return nil, errors.New("unexpected command")
}

func TestRollbackImagePreflightPullsOnlyVerifiedDigest(t *testing.T) {
	target := "sha256:" + strings.Repeat("a", 64)
	pull := "ghcr.io/psewdon1m-exocortex/kernel@sha256:" + strings.Repeat("c", 64)
	for _, wrong := range []bool{false, true} {
		runner := &missingImageRunner{wrong: wrong}
		engine := &Engine{runner: runner}
		err := engine.ensureImageLocal(context.Background(), config.HeadConfig{}, target, pull)
		if (err != nil) != wrong || !runner.pulled {
			t.Fatalf("wrong=%v, err=%v, calls=%v", wrong, err, runner.calls)
		}
		for _, call := range runner.calls {
			if strings.Contains(call, "compose") || strings.Contains(call, " stop ") {
				t.Fatalf("preflight mutated deployment: %s", call)
			}
		}
	}
	runner := &missingImageRunner{}
	engine := &Engine{runner: runner}
	if err := engine.ensureImageLocal(context.Background(), config.HeadConfig{}, target, "latest"); err == nil || runner.pulled {
		t.Fatalf("mutable reference was accepted: %v", err)
	}
}
