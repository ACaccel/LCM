# Local observation primitives

This package supplies read-only Linux snapshots and bounded system-journal polling. It does not install tools, change privileges, operate power, compute health or CPU utilization, schedule work, or persist checkpoints. Construction validates deployment-owned allowlists and limits before I/O. No parent Machine model is required.

## Snapshots

`NewLinux(Config, execx.Runner, System, clock.Clock)` constructs a `Probe`; nil `System` and clock select OS reads and real time. A runner is required when units are configured. `Observe(ctx)` returns a versioned `Snapshot` with independent `Sample[T].Coverage` for identity, boot ID, OS/init, uptime, CPU, load, memory/swap, selected filesystem capacities/inodes, interface operational state, optional PSI, and selected systemd unit states.

- Identity is the domain-separated SHA-256 of a validated machine-id. The raw value never enters results or errors. Cloned machine-ids remain indistinguishable. Boot IDs use lowercase UUID notation independently of device identity.
- CPU counters are cumulative ticks; guest ticks are already included in user/nice. The parent must persist baselines and calculate deltas, resetting utilization to unknown for the first sample, boot changes, counter resets, and stale intervals. CPU count accompanies raw load averages.
- Memory/filesystem sizes are bytes, PSI totals are microseconds, and uptime is `time.Duration` (nanoseconds when encoded as JSON). Zero inode counts do not establish inode availability. Interface `unknown` is an observed kernel state, not a healthy-link assertion.
- `Available` means the source was read and parsed, not that the machine is healthy. Failed samples contain zero values, never cached observations. A nil method error means polling completed; callers must inspect coverage. Caller cancellation also returns `ctx.Err()` and the partial snapshot.
- Reads use fixed `/etc`, `/usr/lib`, `/proc`, and `/sys` paths. There is no process, filesystem, interface, or unit discovery. Filesystem paths must be clean absolute paths; interfaces use literal names up to 15 characters; units use literal ASCII names with a recognized systemd suffix. Globs, escaped unit names, and option-like names are rejected.
- Filesystem/interface allowlists permit up to 32 entries each; units permit 64. Empty lists cause no reads for those sources. PSI defaults off. File reads are bounded to 1 MiB each. Commands default to 5 seconds and 1 MiB output, with validated ranges of 100 ms–30 seconds and 1 KiB–4 MiB. Units are queried serially using fixed `systemctl show` properties. The parent should also supply an overall observation deadline.
- `System` and `FSStats` allow deterministic filesystem/syscall tests. Reads and `statfs` are synchronous: context cancellation cannot interrupt a blocked kernel filesystem operation. Operators should allowlist local filesystems; the package does not prove a path is a mount point or exclude network mounts behind symlinks.

## Journal polling

`NewJournal(JournalConfig, execx.Runner, clock.Clock)` constructs a `JournalReader`. `Poll(ctx, JournalRequest)` returns `JournalBatch`. The default request selects the configured units; an empty host allowlist disables polling. Requests may select a subset, never add units. Only accessible system journals in the default namespace are queried; user journals, remote journals, and other namespaces are outside coverage.

`FieldVersion` is the normalized v1 schema, not a systemd version. Unknown configured versions fail construction. The command requires support for JSON, `--output-fields`, and oldest-first `--lines=+N`; unsupported flags report unsupported coverage, without falling back to broader output. See the [journalctl manual](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html) for source permissions, output-field and cursor semantics.

`JournalLimits` default to 256 consumed rows, 1 MiB output, and 24 hours of lookback; host maxima are 2048 rows, 4 MiB, and 7 days. A request can reduce these limits. Since times older than the window are clamped with a `lookback_exceeded` gap. Every query has explicit since/until bounds. One extra row determines `More`; a batch limit is pending work, not proof of event loss. Output truncation discards the entire command result because `execx` retains a tail.

The parser accepts one JSON object per line, rejects duplicate keys/cursors and invalid scalar types, and validates required cursor/time/boot fields. Binary/multivalued/null selected fields fail closed. Unknown fields are discarded. Exported events contain only v1 schema metadata, opaque cursor, fixed source, source/receive times, boot ID, unit, severity, generic `unit_log` classification, and historical tagging. There is no MESSAGE, environment, command line, stack trace, hostname, PID, or UID retention. PID/UID are used only to attribute PID 1/root manager records through `UNIT`; other writers cannot use that field to impersonate selected units. No OOM, crash, or causal classification is inferred from free text.

### Checkpoint contract

1. Use the same source/unit selection for a cursor. Cursors are private opaque strings, never sortable timestamps. When changing selection, explicitly start a new checkpoint.
2. A cursor request seeks inclusively and verifies exact equality with the first row before consuming subsequent rows. Missing/mismatched anchors report `cursor_unavailable` and retain the old checkpoint. Rotation, vacuum, changed permissions, and an anchor outside the lookback window may be indistinguishable. There is no automatic fallback that silently skips history; the parent must record the gap and explicitly reset the cursor when accepting a new bounded scan.
3. `Scanned` counts validated consumed rows, excluding the anchor and lookahead. Rows without allowed attribution are filtered but still advance `NextCursor`. This allows safe checkpoint progress without persisting unrelated metadata. Filtered rows also count toward the batch limit.
4. Persist events, deduplication keys `(source, cursor)`, gaps, and `NextCursor` atomically before publication. Repeating an uncommitted request is safe; the reader has no checkpoint files or internal cursor state. Cancellation, command failure, malformed output, permission diagnostics, and truncation return no events and preserve the request cursor. Apart from invalid requests and caller cancellation, source failures are represented in coverage rather than method errors.
5. Use `Historical` for entire recovery batches or `HistoricalBefore` for a durable live watermark. The parent suppresses live notifications for historical events and owns deduplication, retention, and backoff.

Even successful polling has `Partial` coverage: selected units, caller-visible records, and a bounded time window only. Persistence/retention and complete journal access are unverified. Some rotation, retention, clock, and permission gaps cannot be detected from journalctl output; no event-loss count is invented. An empty batch does not prove that no OS events occurred. Diagnostics are mapped to stable reason codes; arbitrary stderr is never returned. Malformed or oversized rows may require a recorded gap and explicit parent recovery rather than repeated checkpoint advancement.

## Validation

Tests use `execx.Fake`, an injected clock, synthetic JSONL fixtures, and a fake `System`; they never read the host journal. The real bounded file reader is tested only against a temporary synthetic file.

```sh
go test -race ./internal/machine/observe
go vet ./internal/machine/observe
golangci-lint run ./internal/machine/observe/...
golangci-lint fmt --diff internal/machine/observe
CGO_ENABLED=0 go build ./internal/machine/observe
```
