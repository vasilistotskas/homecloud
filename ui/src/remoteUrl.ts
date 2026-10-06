/**
 * The address as typed ("homecloud.tail1234.ts.net", with or without
 * https:// or a trailing slash) in the form the desktop shell checks: a
 * lowercase https origin.
 */
export function remoteUrl(input: string): string {
  const host = input
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//, "")
    .replace(/\/+$/, "");
  return `https://${host}`;
}
