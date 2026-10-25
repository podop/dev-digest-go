import { describe, it, expect } from "vitest";
import { fileNameFromDisposition } from "./download";

describe("fileNameFromDisposition", () => {
  it("reads the quoted and the bare filename, preferring filename*", () => {
    expect(fileNameFromDisposition('attachment; filename="devdigest-workspace-acme-2026-10-25.json"')).toBe(
      "devdigest-workspace-acme-2026-10-25.json",
    );
    expect(fileNameFromDisposition("attachment; filename=plain.json")).toBe("plain.json");
    expect(fileNameFromDisposition("attachment; filename=\"a.json\"; filename*=UTF-8''b%20c.json")).toBe("b c.json");
  });

  it("returns null for a missing, empty or unusable header", () => {
    expect(fileNameFromDisposition(null)).toBeNull();
    expect(fileNameFromDisposition("attachment")).toBeNull();
    expect(fileNameFromDisposition('attachment; filename=""')).toBeNull();
    expect(fileNameFromDisposition('attachment; filename=".."')).toBeNull();
  });

  it("keeps only the last path segment and drops control characters", () => {
    expect(fileNameFromDisposition('attachment; filename="../../etc/passwd"')).toBe("passwd");
    expect(fileNameFromDisposition("attachment; filename*=UTF-8''a%0Ab.json")).toBe("ab.json");
  });
});
