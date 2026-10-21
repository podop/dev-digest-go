/** The "Serializes as" preview: the heading a run uses for project context and
 *  one `- <path>` line per attached document, in order. */
export function serializesAs(paths: readonly string[]): string {
  return ["## Project context", ...paths.map((p) => `- ${p}`)].join("\n");
}
