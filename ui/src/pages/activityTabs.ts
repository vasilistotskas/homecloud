/** The Activity page's tabs by their `?tab=` value; the command palette lists the same. */
export const activityTabs = {
  runs: "Syncs",
  integrity: "Integrity checks",
  backups: "Records backups",
  iphone: "iPhone backups",
  drive: "iCloud Drive",
  events: "Events",
} as const;

export type ActivityTab = keyof typeof activityTabs;

export function isActivityTab(v: unknown): v is ActivityTab {
  return typeof v === "string" && Object.hasOwn(activityTabs, v);
}
