/* ImportUrlModal — the subtitle names both GitHub and GitLab blob links (GitLab B FR33, AC29). */
import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/render";
import { ImportUrlModal } from "./ImportUrlModal";

describe("ImportUrlModal", () => {
  it("names GitHub and GitLab blob links in the subtitle", () => {
    renderWithProviders(<ImportUrlModal onClose={vi.fn()} onPreview={vi.fn()} />);

    expect(screen.getByText(/GitHub and GitLab blob links work too\./)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Fetch preview" })).toBeDisabled();
  });
});
