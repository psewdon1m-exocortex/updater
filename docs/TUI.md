# Updater terminal console

## Scope and decision

The operator requested an Updater-bundled, arrow-key console for Updater,
Neptune and Gryphon, with Wyvern reserved for a later integration. The console
runs on the Linux host over an ordinary SSH PTY, including Termius. It ships in
the existing binary and release, as `sudo updater tui`. `updater tui --demo`
uses synthetic data and never contacts a service or changes the host.

This implements the terminal profile authorized in the September 19, 2026
conversation. Browser pixel/font/layout requirements are translated to terminal
cells, one accent, visible text focus and a compact single-column layout. The
service-agent ownership and update-verification contracts remain authoritative.

## Applicability and baseline

| Central Part | Application to this change |
| --- | --- |
| 00 | Existing working-tree changes and protocol 2 are the baseline; implementation, tests and limitations are recorded here. |
| 01 | Terminal-specific layout, arrows/Enter/Esc/Tab, narrow viewport, text status and safe input. The terminal selects the font. |
| 02 | Bounded, sanitized status and job summaries. Credentials never enter terminal history or job output. Existing job retention remains in force. |
| 03 | No new authoritative state or backup format. Jobs use the existing durable store; setup codes and bot tokens remain memory-only input. |
| 04 | Included in the existing Updater binary and installer; a separate private operator socket is added to the systemd runtime directories. No public listener. |
| 05 | Existing signed helper installation/update paths, exact version checks, host mutation lock and durable jobs are reused. No head-service update/restore UI. |
| 06 | Targeted API/security/UI tests, complete Go checks, build and PTY exercise; no push or release is performed. |
| 07 | Root-only operator socket, separate from sockets mounted by service containers; typed operations and bounded bodies, no arbitrary shell commands. |
| 08 | Not applicable: no public/indexable web surface is added. |
| 09 | One host Updater; helper enrollment/installation/update only. Neptune schedules remain owned by Saturn. |
| 10 | Agent states, separate bot registration, explicit mutation confirmation and durable results adapted to the terminal. |
| 11 | No deployment-topology change beyond the private local operator entry point. |
| 12 | No release qualification is claimed. Socket isolation, stale socket handling, actual version checks and reconnect are covered at the changed boundaries. |

The baseline has an existing Go CLI, service-scoped HTTP over Unix sockets,
durable helper update jobs, Neptune initialization and Gryphon bot registration.
Its working tree already contains protocol-2 work. The console adds an operator
adapter without replacing those changes or bypassing service token checks.

## Implementation and acceptance plan

1. Add a typed root-only operator API and client, including bounded observed
   component state, eligible registered heads, bot summaries and job history.
2. Reuse existing helper operations and release checks; supply stable request
   IDs and never tie accepted jobs to the UI/SSH process lifetime.
3. Add the arrow-key console, masked credential forms, confirmation, reconnect,
   explicit demo mode and built-in operator help.
4. Test authorization, validation, idempotency, credential/terminal-output
   safety, keyboard navigation, narrow/short/large resize, reconnect and PTY
   behavior. Build the Linux binary and run the complete existing Go suite.

## Compatibility and recovery

The console and daemon ship together. An older daemon without the operator
socket reports unavailable; the existing CLI remains available for diagnosis.
After a self-update the console reconnects and reads the durable job result;
relaunch the console to use the newly installed UI code. Closing the console
does not cancel accepted jobs. A host/daemon failure follows the existing job
reconciliation and rollback rules, rather than claiming automatic resume for
every operation.

No new database migration is required. Rolling back the Updater executable and
unit removes the console entry point; existing job metadata stays compatible.
The new socket contains no persisted configuration or credentials.

## Terminal contract

Use arrows to select, Enter to open/confirm and Esc to cancel/back. Tab moves
between form fields. `--no-color` (or `NO_COLOR`) keeps focus/status readable.
ASCII structure is used; no Nerd Font, emoji, mouse or graphics extension is
required. Narrow terminals use stacked navigation. Resize does not submit a
form or reset a secret field. Pasted control sequences cannot invoke actions.

Actual Termius desktop/mobile acceptance must include navigation, paste,
on-screen keyboard, orientation change, network loss and reconnection. A Linux
PTY test exercises terminal behavior but is not evidence of a real Termius run.

## Verification results

Verified on September 19, 2026, against the working tree containing protocol-2
and migration work. Tests ran on Linux in WSL; the target remains a Linux host.

| Check | Result |
| --- | --- |
| `go test -count=1 ./...` with Go 1.24.13 and Go 1.26.8 | PASS, including existing packages and the console/API tests. |
| `go vet ./...` and `go mod verify` | PASS. |
| `CGO_ENABLED=0 go build -o /tmp/updater ./cmd/updater` | PASS; Linux amd64 binary produced. |
| `sh -n bootstrap.sh install.sh scripts/build-release.sh` | PASS. |
| Root socket tests, compiled and run as root | PASS: socket modes, actual client/server round trip, collision/path handling, service-mount separation and denial of a UID 65534 peer even with deliberately weakened fixture permissions. |
| Client reconnect after socket replacement | PASS. |
| `python3 scripts/test-tui-pty.py /tmp/updater` | PASS with actual terminal input/output: normal and application-cursor arrows, release check, default cancellation, explicit confirmation, operation receipt, secret paste, resize and terminal restoration. |
| PTY sizes | PASS at 80x24, 40x16, 32x12 and 100x32. |
| Exit behavior | PASS: Ctrl+C and SIGTERM restore termios and the alternate screen; non-PTY input fails before raw mode. |
| `govulncheck` with Go 1.26.8, Linux target | Exit 0, no reachable findings. The report also includes module-level GO-2026-5024 in `golang.org/x/sys/windows`; that Windows package is not imported by the Linux target. |
| `git diff --check` | PASS. |

CI now runs the PTY exercise and privileged socket isolation tests alongside
the existing Go version matrix. Bubble Tea v1 and the selected input libraries
preserve the project's Go 1.24 minimum; dependencies are pinned in `go.mod`
and `go.sum`.

Real Termius desktop/mobile acceptance, live signed-release installation,
Neptune enrollment and Telegram bot registration have not been performed in
this task. The PTY exercise uses synthetic demo operations. API and engine tests
verify the operation contracts without deploying to a live host.

## Pre-push change-impact record

The outgoing TUI changes are based on `b5482cc`. The revision-bound pre-push
report is generated after committing and is repeated by CI for the pushed SHA.

| Required area | Result and evidence |
| --- | --- |
| Backup and restore | PASS: the UI adds no archive/state schema or backup action. Lifecycle request IDs use existing durable job metadata. Existing state, host-recovery and engine recovery tests remain in the full suite. |
| Service update | PASS for this branch push: existing typed install/update, signed artifact, version/health and rollback contracts are exercised by the full suite. The console adds no release format. Full live release qualification is not claimed. |
| Operator documentation | PASS: the built-in Help view covers actions, prerequisites, credentials, disconnect and recovery; terminal render tests exercise all screens, including Help. |
| Technical documentation | PASS: Updater README, deployment readiness and this document describe the CLI, socket, limits and test commands. The workspace README already records the new entry point. |
| Security | PASS: strict request/response limits, typed forwarding, input sanitization, secret masking and root peer authorization have positive/negative tests. The pre-push scanner checks outgoing files for high-risk paths and credential patterns. |
| Public SEO/GEO | N/A: Updater has no public/indexable surface. This change adds only a local terminal and Unix socket. |
| Private/concealed exposure | PASS: the operator routes are absent from the service listener, use no TCP listener and reject non-root peers even when fixture filesystem permissions are weakened. Existing service authorization tests remain in the full suite. |

## Implemented operations

| Component | Console actions |
| --- | --- |
| Updater | Observe process/API/version, check and confirm an exact available update. |
| Neptune | Observe status, install, enroll a registered service with a Saturn setup code and local export URL, check and confirm updates. |
| Gryphon | Observe status, install, list bots, connect a bot with masked token input, check and confirm updates. |
| Wyvern | Planned entry only; no executable action. |

Release operations require an eligible registered service and its existing
release configuration. Accepted operations appear in the retained job history.
The console reconnects after daemon loss and looks up uncertain requests by
their request ID; it does not automatically resubmit mutations. Diagnostic
instructions remain available when the operator API is unavailable.

The private API has four routes: `GET /v1/overview`, `GET /v1/bots`,
`POST /v1/check` and `POST /v1/actions`. Its default socket is
`/run/exocortex-admin/updater.sock`, mode `0600`, with a root peer credential
check. It is outside the service-mounted `/run/exocortex` directory. Requests
are limited to 16 KiB, client responses to 512 KiB, and displayed service, bot
and job lists to 100 items. Job output contains selected metadata and controlled
summaries rather than raw command output.

## Try the console

From the Updater repository on Linux:

```sh
CGO_ENABLED=0 go build -o /tmp/updater ./cmd/updater
/tmp/updater tui --demo
python3 scripts/test-tui-pty.py /tmp/updater --output /tmp/updater-tui-check
```

Demo mode needs no root privileges or running services. To administer the host,
install the matching daemon binary and systemd unit through the existing
Updater release path, open an SSH PTY, and run `sudo updater tui`. The daemon
provides the private socket; starting the UI alone does not upgrade an older
daemon. Add `--no-color` for monochrome rendering.
