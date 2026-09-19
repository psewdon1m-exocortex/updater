package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
	"updater/internal/component"
	"updater/internal/config"
)

func handleWyvern(runtime config.Runtime, args []string) {
	if len(args) == 1 && args[0] == "capabilities" {
		printJSON(map[string]any{"schema": "exocortex.wyvern.updater.v1", "api_version": 1})
		return
	}
	if os.Geteuid() != 0 {
		fatal("Wyvern host operations require root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Called by systemd while the invoking deployment holds the host lock.
	if len(args) == 1 && args[0] == "prepare-runtime" {
		exitIf((component.WyvernDeployment{}).Prepare(ctx))
		return
	}
	unlock := acquireHostOperation(runtime, "")
	defer unlock()
	switch {
	case len(args) == 1 && args[0] == "repair":
		exitIf((component.WyvernDeployment{}).Repair(ctx))
	case len(args) == 3 && args[0] == "rotate-client" && args[1] == "--client":
		exitIf((component.WyvernManager{}).RotateClient(ctx, args[2]))
	case len(args) == 3 && args[0] == "install" && args[1] == "--manifest":
		exitIf(component.InstallWyvernManifest(ctx, args[2]))
	case len(args) == 5 && args[0] == "bootstrap" && args[1] == "--head" && args[3] == "--manifest":
		exitIf(component.BootstrapWyvern(ctx, runtime, args[2], args[4]))
	case len(args) == 3 && (args[0] == "install" || args[0] == "link") && args[1] == "--head":
		var random [16]byte
		_, err := rand.Read(random[:])
		exitIf(err)
		_, err = component.EnsureWyvern(runtime, args[2], "cli-link-"+hex.EncodeToString(random[:]))
		exitIf(err)
	case len(args) == 1 && args[0] == "connect":
		var input struct {
			KernelURL  string `json:"kernel_url"`
			AccessKey  string `json:"access_key"`
			InstanceID string `json:"instance_id"`
		}
		exitIf(wyvernStdin(&input))
		err := (component.WyvernManager{}).Connect(ctx, input.KernelURL, input.AccessKey, input.InstanceID)
		input.AccessKey = ""
		exitIf(err)
	case len(args) == 9 && args[0] == "export-link" && args[1] == "--client" && args[3] == "--service" && args[5] == "--url" && args[7] == "--output":
		var random [16]byte
		_, err := rand.Read(random[:])
		exitIf(err)
		exitIf((component.WyvernManager{}).ExportLink(ctx, args[2], args[4], args[6], args[8], "export-"+hex.EncodeToString(random[:])))
	case len(args) == 3 && args[0] == "import-link" && args[1] == "--head":
		head, err := config.LoadHead(runtime, args[2])
		exitIf(err)
		if !component.ConsumesHelper(head.Service, "wyvern") {
			fatal("Head does not consume Wyvern")
		}
		var link component.WyvernLink
		exitIf(wyvernStdin(&link))
		err = (component.WyvernManager{}).ImportLink(ctx, args[2], link)
		link.Token = ""
		exitIf(err)
	default:
		fatal("usage: updater wyvern install --manifest <signed-json> | install --head <id> | link --head <id> | connect < protected-input.json | import-link --head <id> < protected-link.json | export-link --client <id> --service <service> --url <https-url> --output <protected-file> | repair")
	}
	printJSON(map[string]any{"schema": "exocortex.wyvern.operation.v1", "completed": true})
}
func wyvernStdin(value any) error {
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("Expected one bounded JSON object on standard input")
	}
	return nil
}
