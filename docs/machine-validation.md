# Local Machine validation

Implementation checks completed on 2026-11-01, Linux/amd64; the implementation was merged to `main` as `72f7aa4`. The operator confirmed all remaining acceptance complete on 2026-11-03 and requested plan closure. This record distinguishes implementation checks from operator-confirmed deployment and hardware acceptance. Current behavior and constraints are defined in [machines.md](machines.md).

> This record covers the pre-`lcm/v2` implementation (`Machine` documents, `/machines` API paths and `lcm machine` commands). Since 2026-11-05 the machine is a [Resource](resources.md) with the same IDs and behavior; use the current paths and `lcm resource` commands from [machines.md](machines.md).

## Automated checks

- `make check`: formatter, vet, golangci-lint, full Go race tests, frontend typecheck/lint, 117 Vitest tests across 19 files, generated OpenAPI types. Passed.
- `make build`: production console/precompressed assets and a CGO-free binary. Passed.
- `gitleaks dir --no-banner --redact .` and staged-content review: no findings.
- Machine/App fixtures cover document versions and archives, health/freshness/identity, CPU baseline resets, journal cursor/event atomicity, maintenance impact hashes, Service/Policy/Module/config-sync gates, idempotent handoff and recovery, ambiguous shutdown, missing uptime, expired authorization, late reboot evidence, and observations started before recovery anchors. No fixture invokes actual power.
- Deferred engine recovery preserves maintenance at startup; a partial recovery error cannot permanently prevent trigger/config-sync producers from starting after exit. Existing Service/engine/config-sync/trigger tests remain applicable.
- A synthetic schema-8 database migrated through 9/10 while preserving a running Service, suspended run checkpoint and ownership/conflict data. SQLite integrity and foreign-key checks passed. Future schemas are refused; self-install refuses changed/unknown binary schemas before replacement. This does not establish a production backup/restore drill.

## Synthetic event load

Command: `go test ./internal/machine -run '^$' -bench BenchmarkJournalBurst -benchtime=220x -benchmem`.

The test writes 220 batches of 1,000 normalized synthetic events against temporary SQLite storage, checks the 200,000-event ceiling, then advances the fake clock and verifies retention deletion. Results from one run:

| Measurement | Result |
|---|---:|
| Wall time per 1,000-event batch | 24.73 ms |
| Process CPU time per batch | 25.53 ms |
| Peak process RSS | 33,148,928 bytes |
| SQLite file plus WAL before retention | 100,212,824 bytes |
| Allocated bytes per batch | 2,484,118 |

These are synthetic single-process measurements, not long-duration production guarantees. Polls remain sequential; journal command/output limits and row budgets constrain ingestion. SQLite reuses freed pages; retention does not promise that the file shrinks immediately. Operation receipts and completion evidence are retained separately from prunable telemetry.

## Browser and process smoke

The collaborative browser used a dedicated temporary data directory and loopback server with `--machine-power=false`, an empty Service catalog, no selected units and journal disabled. It did not modify the configured deployment database.

- Created a Machine through the YAML form; observed initial unknown state followed by native CPU/memory/filesystem observations and health transitions.
- Entered maintenance using the reviewed preview, partial-coverage acknowledgement and version/hash checks. Restarted only the temporary LCM process, confirmed maintenance persisted and automation recovery deferred, then exited explicitly after observation.
- Viewed raw/minute history, the CPU/memory plot, durable events and history pagination (74 archived raw points across two pages).
- Unmanaged and opened the archive through its immutable ID; confirmed the configuration was read-only. Recreated the same name and verified a new ID with separate history.
- Confirmed reboot/shutdown were disabled and power-on explained the absent independent channel. No power request was submitted.
- Checked the history layout at 390 CSS pixels: document width remained 390, the chart fit its container, and no alert was rendered.
- A second `serve` process against the same temporary directory was refused with `another controller is using this data directory`.

Journal permission/gap rendering, reconnect and uncertain-operation UI use synthetic tests. The browser run did not grant journal access, read selected-unit journals or test a real cross-boot backfill. Temporary processes and data are removed after verification.

## Operator-confirmed deployment acceptance

On 2026-11-03, the operator confirmed that all acceptance was complete, covering the previously open items below. This is an operator-reported result; no new privileged installation or hardware test was performed during documentation closure, and no additional measurements or operation IDs were supplied.

- [x] Root-owned wrapper/pinned helper/sudoers installation, trusted path permissions, ledger provisioning and local systemd/logind compatibility.
- [x] Selected-unit journal permissions, persistent journal availability and actual cross-boot backfill.
- [x] Real reboot completion and startup ordering; graceful shutdown followed by manual power-on.
- [x] Consistent production DB/binary/config/secrets backup and paired restore, with power disabled and independent receipt reconciliation.
- [x] Long-duration native polling/journal load on the target workstation.

Implementation and acceptance are closed, and the temporary local Machine plan has been removed. Acceptance does not change the runtime contract: same-host observation cannot prove physical power-off, power-on remains unsupported, and the independent helper ledger must never be reset or rolled back with the controller database. Other installations require their own deployment validation. Remote machines, cloud VMs, broader approval controls and monitoring extensions are tracked in the [roadmap](roadmap.md).
