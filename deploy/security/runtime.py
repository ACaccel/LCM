#!/usr/bin/python3
"""Bounded, sanitized security collection and privileged report generation."""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import resource
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

UTC = dt.timezone.utc
MAX_STATE = 8 * 1024 * 1024
MAX_READ = 2 * 1024 * 1024
MAX_LINE = 16384
MAX_QUEUE = 1000
MAX_AGE = 48 * 3600
CATEGORIES = {"accounts", "ssh", "sudoers", "cron", "systemd", "deploy", "nginx", "probe"}
# Location categories the ingress component sets in $lcm_category; LCM stores
# traffic under these, and anything else counts as "other". health is not
# logged at all.
TRAFFIC = {"auth", "api", "upload", "socket", "static", "other"}
SERVICE = re.compile(r"[a-z][a-z0-9-]{0,62}")
CREDENTIAL = re.compile(r"[a-z][a-z0-9-]{0,62}")
MAX_AUDIT_GROUPS = 500
# Answers that a retry cannot change: invalid, conflicting or oversized items.
PERMANENT_REJECTIONS = {400, 409, 413, 422}


def stamp(value: float | None = None) -> str:
    return dt.datetime.fromtimestamp(value or time.time(), UTC).isoformat().replace("+00:00", "Z")


def epoch(value: str) -> float:
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


def atomic_json(path: Path, value: object, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix=".security-", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            os.fchmod(stream.fileno(), mode)
            json.dump(value, stream, separators=(",", ":"), sort_keys=True)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def read_json(path: Path, limit: int = MAX_STATE) -> dict:
    with path.open("rb") as stream:
        content = stream.read(limit + 1)
    if len(content) > limit:
        raise ValueError("JSON input exceeds size limit")
    value = json.loads(content)
    if not isinstance(value, dict):
        raise ValueError("Expected JSON object")
    return value


def trusted_root_path(path: Path) -> None:
    """Privileged helpers never traverse collector-owned or writable directories."""
    current = path
    while current != Path("/"):
        info = current.lstat()
        if stat.S_ISLNK(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError("Untrusted privileged helper path")
        current = current.parent


def protected_root_file(path: Path):
    trusted_root_path(path.parent)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    info = os.fstat(descriptor)
    if info.st_uid != 0 or info.st_nlink != 1 or not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022:
        os.close(descriptor)
        raise ValueError("Unsafe privileged helper file")
    os.fchmod(descriptor, 0o600)
    return os.fdopen(descriptor, "wb")


def collector_status(state_dir: Path) -> dict:
    """Read private status as the collector, never as a privileged file reader."""
    trusted_root_path(state_dir.parent)
    directory = os.open(state_dir, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        info = os.fstat(directory)
        if info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError("Unsafe collector state directory")
        descriptor = os.open("status.json", os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=directory)
        with os.fdopen(descriptor, "rb") as stream:
            info = os.fstat(stream.fileno())
            if info.st_uid != os.getuid() or info.st_nlink != 1 or not stat.S_ISREG(info.st_mode) or info.st_size > 65536:
                raise ValueError("Unsafe collector status file")
            raw = stream.read(65537)
        if len(raw) > 65536:
            raise ValueError("Oversized collector status file")
        value = json.loads(raw)
        if not isinstance(value, dict):
            raise ValueError("Invalid collector status object")
        return sanitized_status(value)
    finally:
        os.close(directory)


def sanitized_status(value: dict) -> dict:
    result = {}
    for key, maximum in (("queued", MAX_QUEUE), ("dropped", 10 ** 12)):
        number = value.get(key)
        result[key] = number if type(number) is int and 0 <= number <= maximum else None
    for key in ("observedAt", "lastDelivery"):
        raw = value.get(key)
        try:
            result[key] = stamp(epoch(raw)) if isinstance(raw, str) and len(raw) <= 40 else None
        except (ValueError, OverflowError, TypeError):
            result[key] = None
    allowed_errors = {"token unavailable or invalid", "ingest unavailable or rejected sanitized event"}
    error = value.get("deliveryError")
    result["deliveryError"] = error if isinstance(error, str) and error in allowed_errors else "unrecognized status" if error else None
    result["health"] = {}
    allowed = {"audit:loss", "audit:backlog", "audit:disabled", "queue:loss", "traffic:late", "traffic:capacity",
               "parse:nginx", "parse:audit-time", "capacity:audit", "capacity:nginx"}
    allowed.update(f"{prefix}:{source}" for prefix in ("input", "rotation", "truncate", "oversize", "partial", "backlog", "read")
                   for source in ("nginx", "audit"))
    allowed.update(f"{prefix}:{source}" for prefix in ("missing", "stale") for source in ("audit-status", "aide-status"))
    health = value.get("health", {})
    if isinstance(health, dict):
        for key in sorted(allowed):
            entry = health.get(key)
            if isinstance(entry, dict) and type(entry.get("problem")) is bool:
                result["health"][key] = {"problem": entry["problem"]}
    return result


def token_path(config: dict) -> Path:
    """The ingest token: a systemd credential of the collector unit, so that
    the file it comes from stays readable by root alone."""
    name = config.get("tokenCredential")
    if name is None:
        return Path(config["tokenFile"])
    directory = os.environ.get("CREDENTIALS_DIRECTORY")
    if not isinstance(name, str) or not CREDENTIAL.fullmatch(name) or not directory or not os.path.isabs(directory):
        raise OSError("Collector credential unavailable")
    return Path(directory) / name


def event(config: dict, source: str, fingerprint: str, summary: str, *,
          identity: str, observed: float | None = None, status: str = "firing",
          severity: str = "warning", historical: bool = False) -> dict:
    when = observed or time.time()
    if source not in {"nginx", "auditd", "aide", "collector"}:
        raise ValueError("Unsupported event source")
    if not re.fullmatch(r"[A-Za-z0-9._:-]{1,100}", fingerprint):
        raise ValueError("Invalid fingerprint")
    return {
        "eventId": hashlib.sha256((config["machineId"] + source + identity).encode()).hexdigest(),
        "machineId": config["machineId"], "source": source, "fingerprint": fingerprint,
        "status": status, "severity": severity, "summary": summary[:500],
        "observedAt": stamp(when), "historical": historical or time.time() - when > 600,
    }


class Collector:
    def __init__(self, config: dict, state_path: Path):
        self.config = config
        self.path = state_path
        self.state = read_json(state_path) if state_path.exists() else {
            "version": 1, "cursors": {}, "queue": [], "seen": {}, "health": {},
            "dropped": 0, "nginx": {}, "auditPending": {}, "lastDelivery": None,
        }
        if self.state.get("version") != 1:
            raise ValueError("Unsupported collector state version")
        self.now = time.time()

    def enqueue(self, item: dict) -> None:
        queue = self.state["queue"]
        if any(row.get("event", {}).get("eventId") == item["eventId"] for row in queue):
            return
        if len(queue) >= MAX_QUEUE:
            queue.pop(0)
            self.state["dropped"] += 1
        queue.append({"event": item, "created": self.now, "attempts": 0, "retryAt": 0})

    def enqueue_traffic(self, item: dict) -> None:
        identity = hashlib.sha256(json.dumps(item, sort_keys=True).encode()).hexdigest()
        if any(row.get("trafficId") == identity for row in self.state["queue"]):
            return
        if len(self.state["queue"]) >= MAX_QUEUE:
            self.state["queue"].pop(0)
            self.state["dropped"] += 1
        self.state["queue"].append({"kind": "traffic", "traffic": item, "trafficId": identity,
                                    "created": self.now, "attempts": 0, "retryAt": 0})

    def health(self, key: str, problem: bool, summary: str) -> None:
        current = self.state["health"].get(key)
        if current is None and not problem:
            self.state["health"][key] = {"problem": False, "changed": self.now}
            return
        if current and current["problem"] == problem:
            return
        self.state["health"][key] = {"problem": problem, "changed": self.now}
        self.enqueue(event(self.config, "collector", key, summary, identity=f"{key}:{self.now}",
                           status="firing" if problem else "resolved"))

    def lines(self, name: str, path: Path) -> list[bytes]:
        """Drain a retained predecessor before a new inode; checkpoint complete lines only."""
        try:
            info = path.stat()
            if not path.is_file():
                raise OSError("Not a regular input file")
        except OSError:
            self.health(f"input:{name}", True, f"Security input unavailable: {name}.")
            return []
        self.health(f"input:{name}", False, f"Security input restored: {name}.")
        cursor = self.state["cursors"].get(name)
        inode = [info.st_dev, info.st_ino]
        if cursor is None:
            self.state["cursors"][name] = {"inode": inode, "offset": info.st_size,
                                               "started": self.now}
            return []
        candidate = path
        rotated = cursor["inode"] != inode
        if rotated:
            candidate = None
            for index, child in enumerate(path.parent.iterdir()):
                if index >= 256:
                    break
                try:
                    stat = child.stat()
                    if [stat.st_dev, stat.st_ino] == cursor["inode"] and child.is_file():
                        candidate = child
                        break
                except OSError:
                    continue
            if candidate is None:
                self.health(f"rotation:{name}", True,
                            f"Security log rotation gap: {name}; predecessor no longer readable.")
                cursor.update(inode=inode, offset=0)
                candidate = path
                rotated = False
        result = []
        used = 0
        try:
            with candidate.open("rb") as stream:
                if os.fstat(stream.fileno()).st_size < cursor["offset"]:
                    self.health(f"truncate:{name}", True,
                                f"Security input truncated: {name}; collection gap recorded.")
                    cursor["offset"] = 0
                stream.seek(cursor["offset"])
                while used < MAX_READ:
                    position = stream.tell()
                    line = stream.readline(MAX_LINE + 1)
                    if not line:
                        break
                    used += len(line)
                    if len(line) > MAX_LINE:
                        self.health(f"oversize:{name}", True,
                                    f"Oversized security record discarded: {name}.")
                        while line and not line.endswith(b"\n") and used < MAX_READ:
                            line = stream.readline(MAX_LINE + 1)
                            used += len(line)
                        if not line.endswith(b"\n"):
                            # Skip the rest on a later poll without treating it as a record.
                            cursor["discard"] = True
                        cursor["offset"] = stream.tell()
                        continue
                    if cursor.get("discard"):
                        cursor["offset"] = stream.tell()
                        if line.endswith(b"\n"):
                            cursor.pop("discard", None)
                        continue
                    if not line.endswith(b"\n"):
                        stream.seek(position)
                        break
                    cursor["offset"] = stream.tell()
                    result.append(line)
                remaining = os.fstat(stream.fileno()).st_size - cursor["offset"]
            if rotated and 0 < remaining <= MAX_LINE and used < MAX_READ:
                self.health(f"partial:{name}", True,
                            f"Incomplete record in rotated security input discarded: {name}.")
                cursor["offset"] += remaining
                remaining = 0
            self.health(f"backlog:{name}", remaining > MAX_READ,
                        f"Security input backlog {'exceeds one read budget' if remaining > MAX_READ else 'cleared'}: {name}.")
            self.state.setdefault("inputRemaining", {})[name] = remaining
            if rotated and remaining == 0:
                cursor.update(inode=inode, offset=0)
                result.extend(self.lines(name, path))
        except OSError:
            self.health(f"read:{name}", True, f"Security input read failed: {name}.")
        return result

    def nginx(self, lines: list[bytes]) -> None:
        totals = self.state["nginx"]
        traffic = self.state.setdefault("traffic", {})
        finalized = self.state.setdefault("trafficFinalizedUntil", {})
        for line in lines:
            try:
                row = json.loads(line)
                when = epoch(str(row.get("time", row.get("timestamp", ""))))
                if when > self.now + 300 or when < self.now - 30 * 86400:
                    raise ValueError("Out-of-retention Nginx timestamp")
                category = row.get("category")
                if category in ("", "health", None):
                    continue
                route = category if category in TRAFFIC else "other"
                service = row.get("service")
                service = service if isinstance(service, str) and SERVICE.fullmatch(service) else ""
                code = int(row.get("status", 0))
                duration = float(row.get("request_time", 0)) * 1000
                response_bytes = int(row.get("bytes_sent", 0))
                request_bytes = int(row.get("request_length", 0))
                if not math.isfinite(duration) or not 0 <= duration <= 86400000 or min(response_bytes, request_bytes) < 0 or max(response_bytes, request_bytes) > 2 ** 63 - 1 or not 100 <= code <= 599:
                    raise ValueError("Invalid Nginx measurement")
                req = str(row.get("request_limit", row.get("limit_req", "")))
                conn = str(row.get("connection_limit", row.get("limit_conn", "")))
                limited = any(value in {"REJECTED", "DELAYED", "REJECTED_DRY_RUN", "DELAYED_DRY_RUN"}
                              for value in (req, conn)) or code == 429
                bucket = int(when // 60) * 60
                stream = f"{service}:{route}"
                key = f"{bucket}:{stream}"
                if bucket + 60 <= finalized.get(stream, 0):
                    self.health("traffic:late", True, "Nginx records arrived after finalized metric window; metrics gap recorded.")
                else:
                    initial = {"machineId": self.config["machineId"],
                        "windowStart": stamp(bucket), "windowEnd": stamp(bucket + 60), "category": route,
                        "requests": 0, "status429": 0, "status5xx": 0, "dryRunHits": 0,
                        "requestTimeSumMs": 0.0, "requestTimeMaxMs": 0.0, "responseBytes": 0, "requestBytes": 0}
                    if service:
                        initial["service"] = service
                    measurement = traffic.setdefault(key, initial)
                    measurement["requests"] += 1
                    measurement["status429"] += int(code == 429)
                    measurement["status5xx"] += int(code >= 500)
                    measurement["dryRunHits"] += int(any(value.endswith("_DRY_RUN") for value in (req, conn)))
                    measurement["requestTimeSumMs"] += duration
                    measurement["requestTimeMaxMs"] = max(measurement["requestTimeMaxMs"], duration)
                    measurement["responseBytes"] += response_bytes
                    measurement["requestBytes"] += request_bytes
                if not limited and code < 500:
                    continue
                reason = "limit-observation" if limited else "server-error"
                key = f"{bucket}:{stream}:{reason}"
                value = totals.setdefault(key, {"count": 0, "when": bucket, "service": service,
                                                "route": route, "reason": reason})
                value["count"] += 1
            except (TypeError, ValueError, OverflowError):
                self.health("parse:nginx", True, "Malformed Nginx security records discarded.")
        for key, value in list(totals.items()):
            if value["when"] >= int(self.now // 60) * 60:
                continue
            service = value.get("service", "")
            fingerprint = ":".join(part for part in (value["reason"], service, value["route"]) if part)
            where = f"service={service}, category={value['route']}" if service else f"category={value['route']}"
            self.enqueue(event(self.config, "nginx", fingerprint,
                               f"Nginx {value['reason']}: {where}, records={value['count']} in one minute; observational signal, not proof of attack.",
                               identity=key, observed=value["when"], severity="info"))
            if self.now - value["when"] <= 600:
                self.state.setdefault("nginxActive", {})[fingerprint] = value["when"]
            del totals[key]
        readable = not self.state["health"].get("input:nginx", {}).get("problem", True)
        caught_up = self.state.get("inputRemaining", {}).get("nginx", 0) == 0
        if readable and caught_up:
            for key, measurement in sorted(list(traffic.items()), key=lambda pair: pair[1]["windowStart"]):
                end = epoch(measurement["windowEnd"])
                if end <= self.now - 120:
                    self.enqueue_traffic(measurement)
                    stream = f"{measurement.get('service', '')}:{measurement['category']}"
                    finalized[stream] = max(finalized.get(stream, 0), end)
                    del traffic[key]
        if len(traffic) > 1000:
            self.health("traffic:capacity", True, "Nginx unfinalized metric window capacity exceeded; oldest data dropped.")
            for key in sorted(traffic)[:len(traffic) - 1000]:
                del traffic[key]
        for fingerprint, last in list(self.state.setdefault("nginxActive", {}).items()):
            if readable and caught_up and self.now - last >= 300:
                self.enqueue(event(self.config, "nginx", fingerprint,
                                   "Nginx signal quiet for five minutes while security log remains readable.",
                                   identity=f"recovery:{fingerprint}:{last}", status="resolved", severity="info"))
                del self.state["nginxActive"][fingerprint]
        if len(totals) > 100:
            self.health("capacity:nginx", True, "Nginx aggregation capacity exceeded; data discarded.")
            self.state["nginx"] = {}

    def audit(self, lines: list[bytes]) -> None:
        pending = self.state["auditPending"]
        for raw in lines:
            line = raw.decode("utf-8", errors="replace")
            record = re.search(r"\btype=(\w+).*?msg=audit\((\d+(?:\.\d+)?):(\d+)\)", line)
            if not record:
                continue
            kind, when, serial = record.groups()
            observed = float(when)
            if observed > self.now + 300 or observed < self.now - 30 * 86400:
                self.health("parse:audit-time", True, "Audit record timestamp outside retention or clock tolerance; collection gap.")
                continue
            identity = f"{when}:{serial}"
            row = pending.setdefault(identity, {"when": observed, "first": self.now})
            if kind == "SYSCALL":
                for key in ("auid", "uid", "euid", "syscall"):
                    match = re.search(rf"\b{key}=(-?\d+)\b", line)
                    if match:
                        row[key] = int(match.group(1))
                success = re.search(r"\bsuccess=(yes|no)\b", line)
                row["success"] = success.group(1) if success else "unknown"
                category = re.search(r'\bkey="?wssec_(\w+)', line)
                if category and category.group(1) in CATEGORIES:
                    row["category"] = category.group(1)
            if kind == "EOE":
                self.finish_audit(identity, row)
                pending.pop(identity, None)
        for identity, row in list(pending.items()):
            if self.now - row["first"] > 10:
                self.finish_audit(identity, row)
                del pending[identity]
        while len(pending) > 500:
            pending.pop(next(iter(pending)))
            self.health("capacity:audit", True, "Audit aggregation capacity exceeded; collection gap recorded.")
        self.flush_audit()

    def finish_audit(self, identity: str, row: dict) -> None:
        """Count a finished record in its group of the current minute.

        A deployment writes thousands of files below a watched directory;
        one summary per category, actor and result each minute keeps that
        from filling the bounded delivery queue."""
        if "category" not in row:
            return
        auid = row.get("auid")
        actor = "unset" if auid in (None, -1, 4294967295) else str(auid)
        effective = str(row.get("euid", "unknown"))
        result = str(row.get("success", "unknown"))
        bucket = int(self.now // 60) * 60
        groups = self.state.setdefault("auditGroups", {})
        key = f"{bucket}:{row['category']}:{actor}:{effective}:{result}"
        if key not in groups and len(groups) >= MAX_AUDIT_GROUPS:
            self.health("capacity:audit", True, "Audit aggregation capacity exceeded; collection gap recorded.")
            return
        group = groups.setdefault(key, {"bucket": bucket, "first": row["when"], "count": 0, "category": row["category"],
                                        "actor": actor, "effective": effective, "result": result})
        group["count"] += 1
        group["first"] = min(group["first"], row["when"])

    def flush_audit(self) -> None:
        groups = self.state.setdefault("auditGroups", {})
        current = int(self.now // 60) * 60
        for key, group in sorted(groups.items()):
            if group["bucket"] >= current:
                continue
            summary = (f"Sensitive change: category={group['category']}, loginUid={group['actor']}, "
                       f"effectiveUid={group['effective']}, result={group['result']}, records={group['count']} in one minute; "
                       "details remain in protected local audit log.")
            self.enqueue(event(self.config, "auditd", f"change:{group['category']}", summary,
                               identity=key, observed=group["first"],
                               severity="info" if group["category"] == "probe" else "warning"))
            del groups[key]

    def reports(self, reports: Path) -> None:
        for name, overdue in (("audit-status", 180), ("aide-status", 30 * 3600)):
            try:
                row = read_json(reports / f"{name}.json", 65536)
                observed = epoch(row["observedAt"])
                stale = self.now - observed > overdue or observed > self.now + 300
                self.health(f"stale:{name}", stale,
                            f"{name} report {'overdue or invalid' if stale else 'fresh again'}.")
                if name == "audit-status":
                    lost = int(row.get("lost", 0))
                    previous = self.state.get("auditLost")
                    loss = (previous is not None and lost > previous) or (previous is None and lost > 0)
                    self.health("audit:loss", loss, "Audit kernel lost counter increased; evidence gap." if loss else "Audit lost counter no longer increasing.")
                    self.state["auditLost"] = lost
                    backlog = int(row.get("backlog", 0))
                    limit = int(row.get("backlogLimit", 0))
                    self.health("audit:backlog", limit > 0 and backlog >= limit * 0.8,
                                "Audit backlog near capacity." if limit > 0 and backlog >= limit * 0.8 else "Audit backlog below warning threshold.")
                    self.health("audit:disabled", row.get("enabled") != 1 or not row.get("rulesLoaded", False),
                                "Audit is disabled, immutable, or managed watch rules are missing." if row.get("enabled") != 1 or not row.get("rulesLoaded", False) else "Audit enabled with managed watch rules.")
                else:
                    run_id = str(row.get("runId", ""))
                    if run_id and run_id != self.state.get("aideRun"):
                        self.state["aideRun"] = run_id
                        result = row.get("result")
                        if result in {"changed", "error", "baseline-missing"}:
                            self.enqueue(event(self.config, "aide", "integrity", f"AIDE result={result}; additionsDetected={bool(row.get('added', 0))}, removalsDetected={bool(row.get('removed', 0))}, changesDetected={bool(row.get('changed', 0))}; review protected local report; baseline not updated.",
                                               identity=run_id, observed=observed,
                                               severity="critical" if result == "error" else "warning"))
                        elif result == "clean":
                            self.enqueue(event(self.config, "aide", "integrity", "AIDE check clean against reviewed baseline.",
                                               identity=run_id, observed=observed, status="resolved", severity="info"))
            except (OSError, ValueError, KeyError, TypeError):
                self.health(f"missing:{name}", True, f"{name} report missing or invalid; monitoring gap.")
            else:
                self.health(f"missing:{name}", False, f"{name} report available again.")

    def delivery(self) -> None:
        expired = [row for row in self.state["queue"] if self.now - row["created"] >= MAX_AGE]
        if expired:
            self.state["dropped"] += len(expired)
            self.state["queue"] = [row for row in self.state["queue"] if self.now - row["created"] < MAX_AGE]
        if self.state["dropped"]:
            self.health("queue:loss", True, f"Security delivery queue discarded {self.state['dropped']} expired or excess summaries; local loss counter retained.")
        try:
            token = token_path(self.config).read_text(encoding="ascii").strip()
            if not token or len(token) > 4096 or any(char.isspace() for char in token):
                raise ValueError("Invalid token")
        except (OSError, ValueError, UnicodeError):
            self.state["deliveryError"] = "token unavailable or invalid"
            return
        endpoint = self.config["endpoint"]
        parsed = urllib.parse.urlsplit(endpoint)
        if parsed.scheme != "http" or parsed.hostname not in {"127.0.0.1", "::1"} or parsed.path != "/api/v1/hooks/security" or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError("Collector endpoint must be loopback security ingest")
        # Disable environment proxy handling and redirects to prevent credential forwarding.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                return None
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        sent = 0
        for row in list(self.state["queue"]):
            if sent >= 30:
                break
            if row["retryAt"] > self.now:
                continue
            target = endpoint + "/traffic" if row.get("kind") == "traffic" else endpoint
            payload = row["traffic"] if row.get("kind") == "traffic" else row["event"]
            request = urllib.request.Request(target, json.dumps(payload).encode(),
                                             {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}, method="POST")
            try:
                with opener.open(request, timeout=5) as response:
                    if response.status != 202:
                        raise ValueError("Unexpected ingest response")
                    payload = json.loads(response.read(16385))
                    if payload.get("accepted") is not True:
                        raise ValueError("Ingest rejected event")
            except urllib.error.HTTPError as error:
                if error.code not in PERMANENT_REJECTIONS:
                    row["attempts"] += 1
                    row["retryAt"] = self.now + min(1800, 5 * 2 ** min(row["attempts"], 9))
                    self.state["deliveryError"] = "ingest unavailable or rejected sanitized event"
                    break
                # A refused item, such as traffic of a service LCM does not
                # know, would refuse every retry; it is counted as lost
                # instead of blocking the queue.
                self.state["queue"].remove(row)
                self.state["dropped"] += 1
                self.state["deliveryError"] = "ingest unavailable or rejected sanitized event"
                continue
            except (OSError, ValueError, urllib.error.URLError):
                row["attempts"] += 1
                row["retryAt"] = self.now + min(1800, 5 * 2 ** min(row["attempts"], 9))
                self.state["deliveryError"] = "ingest unavailable or rejected sanitized event"
                # A shared dependency failure must not consume one timeout per queued item.
                break
            self.state["queue"].remove(row)
            self.state["lastDelivery"] = stamp(self.now)
            self.state.pop("deliveryError", None)
            sent += 1

    def poll(self) -> None:
        self.nginx(self.lines("nginx", Path(self.config["nginxLog"])))
        self.audit(self.lines("audit", Path(self.config["auditLog"])))
        self.reports(Path(self.config["reportsDir"]))
        # Checkpoint queue and cursors before network I/O. Stable event IDs make retries safe.
        atomic_json(self.path, self.state)
        self.delivery()
        atomic_json(self.path, self.state)
        atomic_json(self.path.parent / "status.json", {"observedAt": stamp(), "queued": len(self.state["queue"]),
                    "dropped": self.state["dropped"], "lastDelivery": self.state["lastDelivery"],
                    "deliveryError": self.state.get("deliveryError"), "health": self.state["health"]}, 0o640)


def audit_status(reports: Path) -> None:
    trusted_root_path(reports)
    result = {"observedAt": stamp(), "enabled": 0, "lost": 0, "backlog": 0, "backlogLimit": 0,
              "rulesLoaded": False}
    try:
        output = subprocess.run(["/usr/sbin/auditctl", "-s"], capture_output=True, text=True, timeout=10, check=True).stdout
        values = dict(re.findall(r"^(\w+)\s+(\d+)\s*$", output, re.M))
        result.update(enabled=int(values.get("enabled", 0)), lost=int(values.get("lost", 0)),
                      backlog=int(values.get("backlog", 0)), backlogLimit=int(values.get("backlog_limit", 0)))
        rules = subprocess.run(["/usr/sbin/auditctl", "-l"], capture_output=True, text=True, timeout=10, check=True).stdout
        result["rulesLoaded"] = "wssec_accounts" in rules and "wssec_probe" in rules
    except (OSError, subprocess.SubprocessError, ValueError):
        result["error"] = "auditctl status unavailable"
    atomic_json(reports / "audit-status.json", result, 0o640)


def aide_check(config: Path, reports: Path, database: Path) -> None:
    trusted_root_path(reports)
    trusted_root_path(config)
    trusted_root_path(database.parent)
    if database.exists():
        trusted_root_path(database)
    lock = protected_root_file(reports / "aide.lock")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock.close()
        return
    result = {"observedAt": stamp(), "runId": str(time.time_ns()), "added": 0, "removed": 0,
              "changed": 0, "result": "baseline-missing"}
    if database.exists():
        try:
            with protected_root_file(reports / "aide-last-output.txt") as output:
                os.ftruncate(output.fileno(), 0)
                def bound_file_size() -> None:
                    resource.setrlimit(resource.RLIMIT_FSIZE, (16 * 1024 * 1024, 16 * 1024 * 1024))
                completed = subprocess.run(["/usr/bin/aide", "--config", str(config), "--check"],
                                           stdout=output, stderr=subprocess.STDOUT, timeout=3600,
                                           preexec_fn=bound_file_size, check=False)
            code = completed.returncode
            result.update(result="clean" if code == 0 else "changed" if 0 < code <= 7 else "error",
                          added=int(bool(code & 1)) if 0 <= code <= 7 else 0,
                          removed=int(bool(code & 2)) if 0 <= code <= 7 else 0,
                          changed=int(bool(code & 4)) if 0 <= code <= 7 else 0)
        except (OSError, subprocess.SubprocessError):
            result["result"] = "error"
    atomic_json(reports / "aide-status.json", result, 0o640)
    lock.close()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("collect", "collector-status", "audit-status", "aide-check"))
    parser.add_argument("--config", default="/etc/workstation-security/collector.json")
    parser.add_argument("--once", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    if args.mode == "audit-status":
        audit_status(Path("/var/lib/workstation-security/reports"))
        return
    if args.mode == "aide-check":
        aide_check(Path("/etc/workstation-security/aide.conf"), Path("/var/lib/workstation-security/reports"),
                   Path("/var/lib/workstation-security/aide/aide.db"))
        return
    config = read_json(Path(args.config), 65536)
    state = Path(config["stateDir"])
    if args.mode == "collector-status":
        print(json.dumps(collector_status(state), separators=(",", ":")))
        return
    with (state / "collector.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        while True:
            collector = Collector(config, state / "state.json")
            collector.poll()
            if args.once:
                break
            time.sleep(15)


if __name__ == "__main__":
    main()
