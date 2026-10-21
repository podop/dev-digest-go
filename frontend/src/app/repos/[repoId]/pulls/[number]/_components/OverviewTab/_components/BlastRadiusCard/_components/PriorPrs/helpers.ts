/** Last path segment of a repo-relative file path ("a/b/c.ts" -> "c.ts"). */
export function fileBasename(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}
