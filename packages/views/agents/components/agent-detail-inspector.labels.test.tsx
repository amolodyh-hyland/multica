import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, fireEvent } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentDetailInspector } from "./agent-detail-inspector";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: undefined, isSuccess: false }),
}));

// The picker is the only way agent labels ever reached the agent settings
// form. Mocking it means the assertion below fails loudly if the row is
// re-added, instead of passing because a real picker rendered nothing.
vi.mock("../../labels/resource-label-picker", () => ({
  ResourceLabelPicker: () => <div data-testid="resource-label-picker" />,
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/model-picker", () => ({
  ModelPicker: () => <div data-testid="model-picker" />,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: ({ onChange, canEdit }: { onChange: (id: string) => void; canEdit: boolean }) => <button disabled={!canEdit} onClick={() => onChange("codex-runtime")}>Choose Codex</button>,
}));

vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: () => <div data-testid="thinking-field" />,
}));

vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: () => <div data-testid="service-tier-field" />,
}));

const agent = {
  id: "agent-1",
  workspace_id: "workspace-1",
  name: "Lambda",
  description: "Test agent",
  runtime_id: "runtime-1",
} as Agent;

describe("AgentDetailInspector labels", () => {
  afterEach(() => {
    cleanup();
  });

  it("switches the shared profile with fresh provider-native defaults", () => {
    const onUpdate = vi.fn(async () => {});
    renderWithI18n(<AgentDetailInspector agent={agent} runtime={null} runtimes={[]} members={[]} currentUserId="user-1" canEdit onUpdate={onUpdate} />);
    fireEvent.click(screen.getByRole("button", { name: "Choose Codex" }));
    expect(onUpdate).toHaveBeenCalledWith("agent-1", { runtime_id: "codex-runtime", model: "", thinking_level: "", service_tier: "", custom_args: [] });
    expect(screen.getByText(/Changes the shared profile/)).toBeInTheDocument();
  });

  // Agent labels were removed from the product (MUL-5600). Label Settings no
  // longer manages an agent catalog, so an attach-only picker here would be a
  // dead end pointing at a catalog the user cannot populate.
  it("does not offer a label picker", () => {
    renderWithI18n(
      <AgentDetailInspector
        agent={agent}
        runtime={null}
        runtimes={[]}
        members={[]}
        currentUserId="user-1"
        canEdit
        onUpdate={vi.fn(async () => {})}
      />,
    );

    // Sanity check: the profile card actually rendered.
    expect(screen.getByLabelText("Name")).toBeInTheDocument();

    expect(screen.queryByTestId("resource-label-picker")).toBeNull();
    expect(screen.queryByText("Labels")).toBeNull();
  });
});
