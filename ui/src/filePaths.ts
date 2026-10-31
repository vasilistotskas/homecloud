/** Relative, slash-separated paths inside a Files location ("" is its top). */

/** The segments of a path; "" has none. */
export function segments(path: string): string[] {
  return path === "" ? [] : path.split("/");
}

/** Joins a folder and a name. */
export function join(dir: string, name: string): string {
  return dir === "" ? name : `${dir}/${name}`;
}

/** The folder a path is in. */
export function parentOf(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? "" : path.slice(0, i);
}

/** The last segment of a path. */
export function baseName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}
