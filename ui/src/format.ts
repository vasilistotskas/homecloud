import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

const binaryUnits = ["B", "KiB", "MiB", "GiB", "TiB"] as const;

/** Formats a byte count with binary units, e.g. 266 GiB. */
export function formatBytes(bytes: bigint | number): string {
  let value = Number(bytes);
  let unit = 0;
  while (value >= 1024 && unit < binaryUnits.length - 1) {
    value /= 1024;
    unit++;
  }
  const digits = unit === 0 || value >= 100 ? 0 : 1;
  return `${value.toLocaleString(undefined, { maximumFractionDigits: digits })} ${binaryUnits[unit] ?? ""}`;
}

export function formatCount(n: bigint | number): string {
  return Number(n).toLocaleString();
}

export function toDate(ts: Timestamp | undefined): Date | undefined {
  return ts === undefined ? undefined : timestampDate(ts);
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** "Today 14:00", "Yesterday 09:12" or "Oct 3, 14:00". */
export function formatWhen(d: Date, now: Date = new Date()): string {
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  if (sameDay(d, now)) {
    return `Today ${time}`;
  }
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(d, yesterday)) {
    return `Yesterday ${time}`;
  }
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: d.getFullYear() === now.getFullYear() ? undefined : "numeric" })}, ${time}`;
}

/** "38 s", "1 min 12 s", "2 h 5 min". */
export function formatDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) {
    return `${s} s`;
  }
  const m = Math.floor(s / 60);
  if (m < 60) {
    return s % 60 === 0 ? `${m} min` : `${m} min ${s % 60} s`;
  }
  const h = Math.floor(m / 60);
  return m % 60 === 0 ? `${h} h` : `${h} h ${m % 60} min`;
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

/** "in 42 minutes", "5 minutes ago". */
export function formatRelative(d: Date, now: Date = new Date()): string {
  const minutes = Math.round((d.getTime() - now.getTime()) / 60_000);
  if (Math.abs(minutes) < 60) {
    return relative.format(minutes, "minute");
  }
  return relative.format(Math.round(minutes / 60), "hour");
}

/** "every hour", "every 30 minutes", "every 2 hours". */
export function formatInterval(seconds: bigint): string {
  const s = Number(seconds);
  if (s % 3600 === 0) {
    const h = s / 3600;
    return h === 1 ? "every hour" : `every ${h} hours`;
  }
  return `every ${Math.round(s / 60)} minutes`;
}
