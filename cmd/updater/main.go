package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"updater/internal/api"
	"updater/internal/component"
	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/selfupdate"
	"updater/internal/socketmount"
	"updater/internal/state"
)

var version = "0.3.0"

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
	case "neptune":
		handleNeptune(runtime, os.Args[2:])
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
	fmt.Println("  updater neptune install --head <id>")
	fmt.Println("  updater neptune enroll --head <id> --project <id> --export-url <loopback-url>")
	fmt.Println("  updater neptune doctor")
	fmt.Println("  updater version")
}

func handleNeptune(runtime config.Runtime, args []string) {
	if len(args) == 1 && args[0] == "doctor" {
		command := exec.Command("/usr/local/sbin/neptunectl", "doctor")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		exitIf(command.Run())
		return
	}
	if len(args) == 3 && args[0] == "install" && args[1] == "--head" {
		selected, err := component.InstallLatestNeptune(runtime, args[2])
		exitIf(err)
		fmt.Printf("Neptune Linux %s is installed\n", selected)
		return
	}
	if len(args) == 7 && args[0] == "enroll" && args[1] == "--head" && args[3] == "--project" && args[5] == "--export-url" {
		if input, statErr := os.Stdin.Stat(); statErr == nil && input.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprint(os.Stderr, "Saturn one-time setup code: ")
		}
		code, err := bufio.NewReader(os.Stdin).ReadString('\n')
		exitIf(err)
		result, err := component.EnrollNeptuneProject(runtime, args[2], args[4], args[6], strings.TrimSpace(code))
		exitIf(err)
		printJSON(result)
		return
	}
	fatal("usage: updater neptune install --head <id> | enroll --head <id> --project <id> --export-url <loopback-url> | doctor")
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
