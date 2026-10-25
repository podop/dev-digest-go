/* download.ts — browser file download helpers: read the server's file name from a
   `Content-Disposition` header and hand a Blob to the browser as a download. */

const FILE_NAME_MAX = 200;

/** Keeps the last path segment and drops control characters and quotes; null when nothing is left. */
function cleanFileName(raw: string): string | null {
  const base = raw.split(/[\\/]/).pop() ?? "";
  let out = "";
  for (const ch of base) {
    const code = ch.codePointAt(0) ?? 0;
    if (code > 0x1f && code !== 0x7f && ch !== '"') out += ch;
  }
  const name = out.trim().slice(0, FILE_NAME_MAX);
  return name === "" || name === "." || name === ".." ? null : name;
}

/** The file name of an `attachment` `Content-Disposition` (RFC 6266: `filename*` wins over `filename`). */
export function fileNameFromDisposition(header: string | null | undefined): string | null {
  if (!header) return null;
  const extended = /filename\*\s*=\s*([^']*)'[^']*'([^;]+)/i.exec(header);
  if (extended?.[2]) {
    try {
      const name = cleanFileName(decodeURIComponent(extended[2].trim()));
      if (name) return name;
    } catch {
      /* malformed percent-encoding: fall back to the plain parameter */
    }
  }
  const plain = /filename\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^;]+))/i.exec(header);
  const value = plain?.[1] !== undefined ? plain[1].replace(/\\(.)/g, "$1") : plain?.[2]?.trim();
  return value ? cleanFileName(value) : null;
}

/** Offers `blob` to the user as a file download named `fileName`. */
export function saveBlob(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = fileName;
  link.style.display = "none";
  document.body.appendChild(link);
  link.click();
  link.remove();
  // Revoke after the click was handled; the browser has already started reading the blob.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
