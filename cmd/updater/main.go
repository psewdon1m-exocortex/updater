package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"updater/internal/api"
	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/selfupdate"
	"updater/internal/socketmount"
	"updater/internal/state"
)

var version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		help()
		return
	}
	runtime := config.RuntimeFromEnv()
	runtime.UpdaterVersion = version
	switch os.Args[1] {
	case "serve":
		store, err := state.New(runtime.StateDir)
		exitIf(err)
		repairer := socketmount.New(runtime)
		server := api.Server{
			Version: version,
			Runtime: runtime,
			Store:   store,
			Engine:  engine.New(runtime, store, nil),
			OnReady: func() {
				go func() {
					ctx, cancel := context.WithTimeout(
						context.Background(),
						time.Duration(runtime.CommandTimeoutSec)*time.Second,
					)
					defer cancel()
					report := repairer.Repair(ctx)
					if len(report.Recreated) > 0 {
						fmt.Printf("recreated stale updater socket mounts for: %s\n", strings.Join(report.Recreated, ", "))
					}
					for _, warning := range report.Warnings {
						fmt.Fprintf(os.Stderr, "updater socket mount repair warning: %s\n", warning)
					}
				}()
			},
		}
		fmt.Printf("updater %s listening on %s\n", version, runtime.SocketPath)
		exitIf(server.ListenAndServe())
	case "register-head":
		if len(os.Args) != 4 {
			fatal("usage: updater register-head <id> <env-file>")
		}
		exitIf(config.RegisterHead(runtime.RegistryPath, os.Args[2], os.Args[3]))
		fmt.Printf("registered head %s\n", os.Args[2])
	case "status":
		var result map[string]interface{}
		exitIf(api.Request(runtime.SocketPath, http.MethodGet, "/v1/health", nil, &result))
		printJSON(result)
	case "jobs":
		var result map[string]interface{}
		exitIf(api.Request(runtime.SocketPath, http.MethodGet, "/v1/jobs", nil, &result))
		printJSON(result)
	case "version":
		fmt.Println(version)
	case "update":
		headID := ""
		if len(os.Args) == 4 && os.Args[2] == "--head" {
			headID = os.Args[3]
		} else if len(os.Args) != 2 {
			fatal("usage: updater update [--head <id>]")
		}
		exitIf(selfupdate.Run(runtime, headID))
		fmt.Println("updater was updated successfully")
	case "help", "--help", "-h":
		help()
	default:
		fatal("unknown updater command: " + os.Args[1])
	}
}

func help() {
	fmt.Println("updater - local Exocortex VPS update worker")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  updater serve")
	fmt.Println("  updater register-head <id> <env-file>")
	fmt.Println("  updater status")
	fmt.Println("  updater jobs")
	fmt.Println("  updater update [--head <id>]")
	fmt.Println("  updater version")
}

func printJSON(value interface{}) {
	body, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(body))
}

func exitIf(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
