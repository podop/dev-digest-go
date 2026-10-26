import { describe, it, expect, afterEach, vi } from "vitest";
import { cleanup } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { api, apiFetch } from "./api";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function sent(http: ReturnType<typeof mockFetch>, index = 0) {
  const init = http.fetch.mock.calls[index]?.[1] as RequestInit;
  return { init, headers: new Headers(init.headers) };
}

describe("api client", () => {
  it("del sends no body by default and a JSON body when given", async () => {
    const http = mockFetch({ "DELETE /a/1": { ok: true }, "DELETE /a/2": { ok: true } });
    await api.del("/a/1");
    await api.del("/a/2", { confirm: "x" });

    const first = sent(http, 0);
    expect(first.init.method).toBe("DELETE");
    expect(first.init.body).toBeUndefined();
    expect(first.headers.has("content-type")).toBe(false);
    const second = sent(http, 1);
    expect(second.init.method).toBe("DELETE");
    expect(second.headers.get("content-type")).toBe("application/json");
    expect(http.requests("DELETE", "/a/2")[0]?.body).toEqual({ confirm: "x" });
  });

  it("upload posts the raw body under the given content type only", async () => {
    const http = mockFetch({ "POST /up": { ok: true } });
    const file = new File(["PK"], "x.zip", { type: "application/zip" });
    await api.upload("/up?dry_run=true", file, "application/zip");

    const { init, headers } = sent(http);
    expect(init.method).toBe("POST");
    expect(init.body).toBe(file);
    expect(headers.get("content-type")).toBe("application/zip");
    expect(http.requests("POST", "/up")[0]?.search).toBe("?dry_run=true");
  });

  it("a caller's Content-Type in any letter case is not doubled with the JSON default", async () => {
    const http = mockFetch({ "POST /up": { ok: true } });
    await api.post("/up", { a: 1 });
    await apiFetch("/up", { method: "POST", body: "{}", headers: { "Content-Type": "text/x" } });

    expect(sent(http, 0).headers.get("content-type")).toBe("application/json");
    expect(sent(http, 1).headers.get("content-type")).toBe("text/x");
  });

  it("download passes the init (method, body) through and reads the file name", async () => {
    const http = mockFetch({
      "POST /dl": new Response("zip", { headers: { "content-disposition": 'attachment; filename="a-2026-10-26.zip"' } }),
    });
    const out = await api.download("/dl", { method: "POST", body: JSON.stringify({ name: "a" }) });

    expect(out.fileName).toBe("a-2026-10-26.zip");
    expect(await out.blob.text()).toBe("zip");
    expect(http.requests("POST", "/dl")[0]?.body).toEqual({ name: "a" });
    expect(sent(http).headers.get("content-type")).toBe("application/json");
  });
});
