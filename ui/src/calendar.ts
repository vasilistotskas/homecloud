/**
 * The viewer's calendar. The agent counts days and months in the viewer's IANA
 * time zone, which is also the zone the app shows dates in (Intl and Date use
 * it by default), so both agree on which day and month a photo was taken.
 */
export const viewerTimeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

/** A day of the Gregorian calendar, as the API's CalendarDate; month is 1 to 12. */
export interface Day {
  year: number;
  month: number;
  day: number;
}

/** The viewer's date at now. */
export function today(now: Date = new Date()): Day {
  return { year: now.getFullYear(), month: now.getMonth() + 1, day: now.getDate() };
}

/** The first instant after month (1 to 12) of year in the viewer's time zone. */
export function endOfMonth(year: number, month: number): Date {
  return new Date(year, month, 1);
}

/** A month as the `month` search parameter writes it: "2023-06". */
export function monthParam(year: number, month: number): string {
  return `${String(year).padStart(4, "0")}-${String(month).padStart(2, "0")}`;
}

/** Reads a `month` search parameter; undefined unless it names a month. */
export function parseMonthParam(value: string | null): { year: number; month: number } | undefined {
  const m = /^(\d{4})-(\d{2})$/.exec(value ?? "");
  if (m === null) {
    return undefined;
  }
  const year = Number(m[1]);
  const month = Number(m[2]);
  return year >= 1 && month >= 1 && month <= 12 ? { year, month } : undefined;
}
