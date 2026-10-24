import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { AddRepoForm } from "./AddRepoForm";

afterEach(cleanup);

const URL_PLACEHOLDER = "https://github.com/owner/repo";

describe("AddRepoForm", () => {
  it("posts the trimmed URL on submit (button or Enter), calls onAdded with the repo and clears the field", async () => {
    const api = mockFetch({ "POST /repos": { id: "r9", full_name: "acme/api" } });
    const onAdded = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={onAdded} />);
    const input = screen.getByRole("textbox", { name: "Repository URL" });
    const submit = screen.getByRole("button", { name: "Add repository" });
    expect(submit).toBeDisabled();

    await user.type(input, "  https://github.com/acme/api {Enter}");
    await waitFor(() => expect(onAdded).toHaveBeenCalledTimes(1));
    expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: "r9", full_name: "acme/api" }));
    expect(api.requests("POST", "/repos")[0]!.body).toEqual({ url: "https://github.com/acme/api" });
    expect(input).toHaveValue("");
    expect(screen.getByRole("button", { name: "Add repository" })).toBeDisabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows the API message, keeps the URL, refocuses the field and does not call onAdded (EC15)", async () => {
    mockFetch({ "POST /repos": jsonResponse({ error: { code: "conflict", message: "Repository already added" } }, 409) });
    const onAdded = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={onAdded} />);
    const input = screen.getByRole("textbox", { name: "Repository URL" });
    expect(input).toHaveFocus(); // focused on open

    await user.type(input, "https://github.com/acme/api");
    await user.click(screen.getByRole("button", { name: "Add repository" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Repository already added");
    expect(input).toHaveValue("https://github.com/acme/api");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveFocus(); // back on the field so the URL can be fixed
    expect(onAdded).not.toHaveBeenCalled();
  });

  it("shows the engine-unreachable message when the request cannot be sent", async () => {
    vi.stubGlobal("fetch", () => Promise.reject(new TypeError("network down")));
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    await user.type(screen.getByPlaceholderText(URL_PLACEHOLDER), "https://github.com/x/y");
    await user.click(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Cannot reach the DevDigest engine");
  });

  it("shows Cancel only when onCancel is given, and Cancel neither submits nor adds", async () => {
    const api = mockFetch({ "POST /repos": { id: "r1" } });
    const first = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
    first.unmount();

    const onCancel = vi.fn();
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} onCancel={onCancel} />);
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/a/b");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(api.requests("POST", "/repos")).toHaveLength(0);
  });

  it("disables the button while the add runs, so a double click sends one request", async () => {
    let release: (r: Response) => void = () => {};
    const api = mockFetch({ "POST /repos": () => new Promise<Response>((res) => (release = res)) });
    const { user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} />);
    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/a/b");
    await user.dblClick(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByRole("button", { name: "Cloning…" })).toBeDisabled();
    expect(api.requests("POST", "/repos")).toHaveLength(1);
    release(jsonResponse({ id: "r1" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Add repository" })).toBeInTheDocument());
  });

  it("takes every visible string from the message files (pseudo bundle, AC22)", async () => {
    mockFetch({ "POST /repos": jsonResponse({ error: { code: "bad", message: "Repo not reachable" } }, 400) });
    const { container, user } = renderWithProviders(<AddRepoForm onAdded={vi.fn()} onCancel={vi.fn()} />, {
      messages: pseudoMessages(),
    });
    expect(strayText(container)).toEqual([]);
    expect(screen.getByRole("button", { name: "⟦addRepo.cancel⟧" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "⟦addRepo.urlLabel⟧" })).toBeInTheDocument();

    await user.type(screen.getByPlaceholderText("⟦addRepo.urlPlaceholder⟧"), "x");
    await user.click(screen.getByRole("button", { name: "⟦addRepo.submit⟧" }));
    // The API message is data, everything else is a marker.
    expect(await screen.findByText("Repo not reachable")).toBeInTheDocument();
    expect(strayText(container, ["Repo not reachable"])).toEqual([]);
  });
});
