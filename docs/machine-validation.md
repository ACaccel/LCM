# Local Machine validation

Validated on 2026-11-01, Linux/amd64, on branch `feat/local-machine-lifecycle`. This records controller implementation checks; privileged installation and real power acceptance remain separate. Current behavior and constraints are defined in [machines.md](machines.md).

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

## Deployment acceptance still open

The following require a separately authorized maintenance window and manual recovery arrangement:

- Root-owned wrapper/pinned helper/sudoers installation, trusted path permissions, ledger provisioning and local systemd/logind compatibility. Source artifacts and fake-command tests do not validate an installed helper.
- Selected-unit journal permissions, persistent journal availability and actual cross-boot backfill. Missing optional access remains visible as limited coverage.
- Real reboot completion and startup ordering; graceful shutdown followed by manual power-on. Same-host observation cannot prove physical power-off, and power-on remains unsupported.
- Consistent production DB/binary/config/secrets backup and paired restore, with power disabled and independent receipt reconciliation. Never roll back or reset the helper ledger with the controller database.
- Long-duration native polling/journal load on the target workstation. The synthetic load above does not replace this deployment measurement.

The current implementation plan remains open for these deployment checks. Remote machines, cloud VMs, broader approval controls and monitoring extensions are tracked in the [roadmap](roadmap.md).
