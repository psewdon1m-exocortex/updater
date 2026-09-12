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
	"updater/internal/hostrecovery"
	"updater/internal/selfupdate"
	"updater/internal/socketmount"
	"updater/internal/state"
)

var version = "0.4.2"

func main() {
	if len(os.Args) < 2 {
		help()
		return
	}
	runtime := config.RuntimeFromEnv()
	runtime.UpdaterVersion = version
	switch os.Args[1] {
	case "serve":
		if hostrecovery.PendingHostRecovery() {
			if exec.Command("systemctl", "is-active", "--quiet", "exocortex-host-recovery.service").Run() == nil {
				fatal("host recovery is still active")
			}
			exitIf(exec.Command("systemd-run", "--unit=exocortex-host-recovery-resume", "--collect", "--wait", "--property=Type=exec", "--property=RuntimeMaxSec=600", "/usr/bin/updater", "host-recovery-resume").Run())
		}
		store, err := state.New(runtime.StateDir)
		exitIf(err)
		exitIf(store.ReconcileInterrupted(activeSupervisor))
		exitIf(store.CleanupRecoveryStaging())
		repairer := socketmount.New(runtime)
		server := api.Server{
			Version: version,
			Runtime: runtime,
			Store:   store,
			Engine:  engine.New(runtime, store, nil),
			OnReady: func() {
				go func() {
					// Reconcile after the socket is available and allow manual jobs
					// to take the same host lock. Missing bootstrap data is retried.
					time.Sleep(5 * time.Second)
					for {
						component.ReconcileHostHelpers(runtime, store)
						time.Sleep(time.Minute)
					}
				}()
				go monitorSupervisors(store)
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
		store, err := state.New(runtime.StateDir)
		exitIf(err)
		printJSON(store.List())
	case "version":
		fmt.Println(version)
	case "host-recovery-resume":
		exitIf(hostrecovery.ResumeInterruptedHost())
	case "host-recovery":
		release := acquireHostOperation(runtime, "")
		defer release()
		if len(os.Args) != 6 || os.Args[4] != "--key-file" || (os.Args[2] != "export" && os.Args[2] != "restore") {
			fatal("usage: updater host-recovery export|restore <archive> --key-file <protected-passphrase-file>")
		}
		key, err := os.ReadFile(os.Args[5])
		exitIf(err)
		if os.Args[2] == "export" {
			archive, err := hostrecovery.Export(strings.TrimSpace(string(key)))
			clear(key)
			exitIf(err)
			exitIf(os.WriteFile(os.Args[3], archive, 0600))
		} else {
			archive, err := os.ReadFile(os.Args[3])
			exitIf(err)
			err = hostrecovery.Restore(archive, strings.TrimSpace(string(key)))
			clear(key)
			exitIf(err)
		}
		fmt.Println("Helper recovery operation completed")
	case "host-recovery-job":
		if len(os.Args) != 3 {
			fatal("host recovery job ID is required")
		}
		exitIf(runSupervised(runtime, os.Args[2], "host-recovery"))
	case "update":
		headID := ""
		if len(os.Args) == 4 && os.Args[2] == "--head" {
			headID = os.Args[3]
		} else if len(os.Args) != 2 {
			fatal("usage: updater update [--head <id>]")
		}
		release := acquireHostOperation(runtime, "")
		defer release()
		exitIf(selfupdate.Run(runtime, headID))
		fmt.Println("updater was updated successfully")
	case "neptune":
		handleNeptune(runtime, os.Args[2:])
	case "gryphon":
		if len(os.Args) != 5 || os.Args[2] != "install" || os.Args[3] != "--head" {
			fatal("usage: updater gryphon install --head <id>")
		}
		release := acquireHostOperation(runtime, "")
		defer release()
		selected, err := component.InitializeGryphon(runtime, os.Args[4])
		exitIf(err)
		fmt.Printf("Gryphon %s is installed\n", selected)
	case "self-update-job":
		if len(os.Args) != 3 {
			fatal("self-update job ID is required")
		}
		exitIf(runSupervised(runtime, os.Args[2], "updater-self-update"))
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
		release := acquireHostOperation(runtime, "")
		defer release()
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
		release := acquireHostOperation(runtime, "")
		defer release()
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
