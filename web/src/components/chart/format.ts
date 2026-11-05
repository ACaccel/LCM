import { formatBytes, formatDuration } from "../../lib/format";

/** Axis and tooltip formatters for the units charts show. */
const trim = (v: number) => String(Number(v.toFixed(1)));

export const fmtPercent = (v: number) => `${trim(v)}%`;
export const fmtMs = (v: number) => formatDuration(v);
export const fmtBytes = (v: number) => formatBytes(v);
export const fmtRate = (v: number) => `${formatBytes(v)}/s`;
export const fmtCount = (v: number) => (v >= 10_000 ? `${trim(v / 1000)}k` : trim(v));
export const fmtCelsius = (v: number) => `${trim(v)} °C`;
