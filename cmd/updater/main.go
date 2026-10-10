package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"updater/internal/api"
	"updater/internal/component"
	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/hostrecovery"
	"updater/internal/migration"
	"updater/internal/selfupdate"
	"updater/internal/socketmount"
	"updater/internal/state"
	"updater/internal/tui"
)

var version = "0.6.14"
var windowKeyBlob = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

func main() {
	if len(os.Args) < 2 {
		help()
		return
	}
	runtime := config.RuntimeFromEnv()
	runtime.UpdaterVersion = version
	switch os.Args[1] {
	case "tui":
		exitIf(tui.Run(os.Args[2:], runtime.OperatorSocketPath))
	case "serve":
		if hostrecovery.PendingHostRecovery() {
			if exec.Command("systemctl", "is-active", "--quiet", "exocortex-host-recovery.service").Run() == nil {
				fatal("host recovery is still active")
			}
			exitIf(exec.Command("systemd-run", "--unit=exocortex-host-recovery-resume", "--collect", "--wait", "--property=Type=exec", "--property=RuntimeMaxSec=600", "/usr/bin/updater", "host-recovery-resume").Run())
		}
		store, err := state.New(runtime.StateDir)
		exitIf(err)
		repairer := socketmount.New(runtime)
		server := api.Server{
			Version: version,
			Runtime: runtime,
			Store:   store,
			Engine:  engine.New(runtime, store, nil),
			Prepare: func() error {
				if err := store.ReconcileInterrupted(activeSupervisor); err != nil {
					return err
				}
				if err := engine.MigrateBackupRetention(runtime, store); err != nil {
					return err
				}
				if err := engine.CleanupVolatileRecovery(); err != nil {
					return err
				}
				if err := store.CleanupVolatileSpools(); err != nil {
					return err
				}
				if err := store.CleanupRecoveryStaging(); err != nil {
					return err
				}
				pruneJobs(runtime, store)
				return nil
			},
			OnReady: func() {
				go func() {
					// Reconcile after the socket is available and allow manual jobs
					// to take the same host lock. Missing bootstrap data and stale
					// helper socket mounts are retried.
					time.Sleep(5 * time.Second)
					for {
						pruneJobs(runtime, store)
						component.ReconcileHostHelpers(runtime, store)
						if release, lockErr := store.BeginOperation(""); lockErr == nil {
							ctx, cancel := context.WithTimeout(
								context.Background(),
								time.Duration(runtime.CommandTimeoutSec)*time.Second,
							)
							report := repairer.Repair(ctx)
							cancel()
							release()
							if len(report.Recreated) > 0 {
								fmt.Printf("recreated stale helper socket mounts for: %s\n", strings.Join(report.Recreated, ", "))
							}
							for _, warning := range report.Warnings {
								fmt.Fprintf(os.Stderr, "helper socket mount repair warning: %s\n", warning)
							}
						}
						time.Sleep(time.Minute)
					}
				}()
				go monitorSupervisors(store)
			},
		}
		fmt.Printf("updater %s listening on %s\n", version, runtime.SocketPath)
		exitIf(server.ListenAndServe())
	case "migrate-head":
		job, err := migration.Run(runtime, os.Args[2:], os.Stdin)
		exitIf(err)
		printJSON(job)
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
	case "host":
		if len(os.Args) == 3 && os.Args[2] == "capabilities" {
			printJSON(map[string]any{"schema": "exocortex.updater.host-dependencies.v1", "api_version": 1,
				"pinned_helpers": []string{"neptune", "gryphon"}, "machine_enrollment": true})
			return
		}
		if len(os.Args) == 5 && os.Args[2] == "seed-source" {
			if os.Geteuid() != 0 {
				fatal("host configuration requires root")
			}
			host, err := config.LoadHost(runtime)
			exitIf(err)
			if host.ReleaseSources == nil {
				host.ReleaseSources = map[string]string{}
			}
			if host.ReleaseSources[os.Args[3]] == "" {
				host.ReleaseSources[os.Args[3]] = os.Args[4]
				exitIf(config.SaveHost(runtime, host))
			}
			return
		}
		if (len(os.Args) != 7 && len(os.Args) != 8) || os.Args[2] != "configure-kernel" || os.Args[3] != "--url" || os.Args[5] != "--token-file" {
			fatal("usage: updater host configure-kernel --url <https-origin> --token-file <absolute-path> [host-id]")
		}
		if os.Geteuid() != 0 {
			fatal("host configuration requires root")
		}
		host, err := config.LoadHost(runtime)
		exitIf(err)
		id, err := config.LocalHostID()
		exitIf(err)
		if len(os.Args) == 8 {
			id = os.Args[7]
		}
		if host.KernelURL != "" && (host.KernelURL != os.Args[4] || host.KernelTokenFile != os.Args[6] || host.HostID != id) {
			fatal("existing host Kernel connection differs; use explicit migration")
		}
		host.KernelURL, host.KernelTokenFile, host.HostID = os.Args[4], os.Args[6], id
		_, err = config.HostKernelToken(host)
		exitIf(err)
		exitIf(config.SaveHost(runtime, host))
		fmt.Println("Updater host Kernel connection saved")
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
		store, err := state.New(runtime.StateDir)
		exitIf(err)
		job, ok := store.Get(os.Args[2])
		if !ok || (job.Service != "host-recovery" && job.Service != "host-recovery-export" && job.Service != "host-recovery-restore") {
			fatal("invalid host recovery job")
		}
		exitIf(runSupervised(runtime, os.Args[2], job.Service))
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
	case "wyvern":
		handleWyvern(runtime, os.Args[2:])
	case "window":
		if os.Geteuid() != 0 {
			fatal("Window management requires root")
		}
		if len(os.Args) == 5 && os.Args[2] == "pair" && os.Args[3] == "--key-base64" {
			if len(os.Args[4]) > 128 || !windowKeyBlob.MatchString(os.Args[4]) {
				fatal("Invalid Window public key")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			fingerprint, err := api.PairWindowPublicKey(ctx, "ssh-ed25519 "+os.Args[4])
			exitIf(err)
			fmt.Printf("Window development PC paired: %s\n", fingerprint)
			break
		}
		if len(os.Args) != 5 || os.Args[2] != "install" || os.Args[3] != "--version" {
			fatal("usage: sudo updater window install --version <exact-version> | pair --key-base64 <public-key-blob>")
		}
		release := acquireHostOperation(runtime, "")
		defer release()
		exitIf(component.InstallWindowBootstrap(runtime, os.Args[4], version))
		fmt.Printf("Window %s is installed and healthy\n", os.Args[4])
	case "gryphon":
		if len(os.Args) == 5 && os.Args[2] == "link" && os.Args[3] == "--head" {
			release := acquireHostOperation(runtime, "")
			defer release()
			if _, err := component.InstalledVersion("gryphon"); err != nil {
				fatal("Install Gryphon before linking a service")
			}
			_, err := component.InitializeGryphon(runtime, os.Args[4])
			exitIf(err)
			fmt.Printf("Gryphon client %s is linked\n", os.Args[4])
			return
		}
		if len(os.Args) == 5 && os.Args[2] == "install" && os.Args[3] == "--bundle" {
			release := acquireHostOperation(runtime, "")
			defer release()
			selected, err := component.InstallPinnedHelper(runtime, "gryphon", os.Args[4])
			exitIf(err)
			fmt.Printf("Gryphon %s is installed\n", selected)
			return
		}
		if len(os.Args) < 3 || os.Args[2] != "install" || len(os.Args) != 3 && (len(os.Args) != 5 || os.Args[3] != "--head") {
			fatal("usage: updater gryphon install [--head <id>]")
		}
		headID := ""
		if len(os.Args) == 5 {
			headID = os.Args[4]
		}
		release := acquireHostOperation(runtime, "")
		defer release()
		selected, err := component.InitializeGryphon(runtime, headID)
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
	fmt.Println("  updater tui [--demo] [--no-color] [-window]")
	fmt.Println("  updater register-head <id> <env-file>")
	fmt.Println("  updater migrate-head --head <id> --version <version> --saved-backup-stdin --confirm-saved")
	fmt.Println("  updater status")
	fmt.Println("  updater jobs")
	fmt.Println("  updater update [--head <id>]")
	fmt.Println("  updater host configure-kernel --url <https-origin> --token-file <absolute-path> [host-id]")
	fmt.Println("  updater host capabilities")
	fmt.Println("  updater host seed-source <component> <https-repository>")
	fmt.Println("  updater neptune install [--head <id>]")
	fmt.Println("  updater gryphon install [--head <id>]")
	fmt.Println("  updater gryphon link --head <id>")
	fmt.Println("  updater neptune|gryphon install --bundle <verified-directory>")
	fmt.Println("  updater window pair --key-base64 <public-key-blob>")
	fmt.Println("  updater window install --version <exact-version>")
	fmt.Println("  updater neptune enroll --head <id> --project <id> --export-url <loopback-url>")
	fmt.Println("  updater neptune doctor")
	fmt.Println("  updater version")
}

func handleNeptune(runtime config.Runtime, args []string) {
	if len(args) == 3 && args[0] == "install" && args[1] == "--bundle" {
		release := acquireHostOperation(runtime, "")
		defer release()
		selected, err := component.InstallPinnedHelper(runtime, "neptune", args[2])
		exitIf(err)
		fmt.Printf("Neptune Linux %s is installed\n", selected)
		return
	}
	if len(args) == 1 && args[0] == "doctor" {
		command := exec.Command("/usr/local/sbin/neptunectl", "doctor")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		exitIf(command.Run())
		return
	}
	if (len(args) == 1 && args[0] == "install") || (len(args) == 3 && args[0] == "install" && args[1] == "--head") {
		headID := ""
		if len(args) == 3 {
			headID = args[2]
		}
		release := acquireHostOperation(runtime, "")
		defer release()
		selected, err := component.InstallLatestNeptune(runtime, headID)
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
	fatal("usage: updater neptune install [--head <id>] | enroll --head <id> --project <id> --export-url <loopback-url> | doctor")
}

func printJSON(value interface{}) {
	body, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(body))
}

func pruneJobs(runtime config.Runtime, store *state.Store) {
	if runtime.MaxRetainedJobs <= 0 || runtime.RetentionDays <= 0 {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(runtime.RetentionDays) * 24 * time.Hour)
	if err := store.Prune(runtime.MaxRetainedJobs, cutoff); err != nil {
		fmt.Fprintf(os.Stderr, "updater job retention warning: %v\n", err)
	}
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
