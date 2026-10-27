const dtf = new Intl.DateTimeFormat("zh-TW", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});
const tf = new Intl.DateTimeFormat("zh-TW", { hour: "2-digit", minute: "2-digit", hour12: false });
const full = new Intl.DateTimeFormat("zh-TW", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** HH:mm for today, MM/DD HH:mm for other days. */
export function formatTime(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  return sameDay(d, now) ? tf.format(d) : dtf.format(d).replace(/\s+/g, " ");
}

/** Full local date and time, for tooltips and detail views. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "-";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "-" : full.format(d);
}

/** Compact duration such as 850ms, 22s, 1m16s or 2h05m. */
export function formatDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return "-";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const total = Math.round(ms / 1000);
  if (total < 60) return `${total}s`;
  const m = Math.floor(total / 60);
  const sec = total % 60;
  if (m < 60) return sec ? `${m}m${String(sec).padStart(2, "0")}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

/** Elapsed time between two instants, or until `now` when still open. */
export function spanMs(from: string | null | undefined, to: string | null | undefined, now: number = Date.now()): number | undefined {
  if (!from) return undefined;
  const a = Date.parse(from);
  const b = to ? Date.parse(to) : now;
  return Number.isNaN(a) || Number.isNaN(b) ? undefined : Math.max(0, b - a);
}

/** Uptime ratio 0..1 as a percentage; "-" without data. */
export function formatUptime(ratio: number | null | undefined, count: number): string {
  if (!count || ratio == null) return "-";
  const pct = ratio * 100;
  return `${pct >= 99.995 ? "100" : pct.toFixed(pct >= 99 ? 2 : 1)}%`;
}

/** Commits are shortened to 7 characters; other versions are shown as they are. */
export function shortVersion(v: string | null | undefined): string {
  if (!v) return "-";
  return /^[0-9a-f]{12,64}$/i.test(v) ? v.slice(0, 7) : v;
}

/** Local midnight of the day containing `now`. */
export function startOfDay(now: Date = new Date()): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate());
}
