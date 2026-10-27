import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import type { Agent, AgentRuntime, Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const mocks = vi.hoisted(() => ({ get: vi.fn(), set: vi.fn(), toast: vi.fn(), canEdit: true }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (selector: (s: unknown) => unknown) => selector({ user: { id: "owner" } }) }));
vi.mock("@multica/core/permissions", () => ({ useAgentPermissions: () => ({ canEdit: { allowed: mocks.canEdit } }) }));
vi.mock("@multica/core/api", () => ({ api: {
  getIssueRuntime: (...args: unknown[]) => mocks.get(...args),
  setIssueRuntime: (...args: unknown[]) => mocks.set(...args),
} }));
vi.mock("sonner", () => ({ toast: { error: mocks.toast } }));
vi.mock("../../agents/components/inspector/runtime-picker", () => ({ RuntimePicker: ({ value, onChange, canEdit, runtimes }: { value: string; onChange: (id: string) => void; canEdit: boolean; runtimes: AgentRuntime[] }) => <>
  <span data-testid="selection">{value}</span>
  <span data-testid="choices">{runtimes.map((r) => r.id).join(",")}</span>
  <button disabled={!canEdit} onClick={() => onChange("codex")}>Switch to Codex</button>
</> }));

import { IssueRuntimeControl } from "./issue-runtime-control";
const issue = { id: "issue", assignee_type: "agent", assignee_id: "profile" } as Issue;
const state = { issue_id: "issue", agent_id: "profile", runtime_id: null as string | null, effective_runtime_id: "claude" };
let client: QueryClient;
function renderControl() {
  return renderWithI18n(<QueryClientProvider client={client}><IssueRuntimeControl issue={issue} /></QueryClientProvider>);
}
beforeEach(() => {
  vi.clearAllMocks(); mocks.canEdit = true;
  mocks.get.mockResolvedValue(state);
  mocks.set.mockImplementation(async (_id, input) => ({ ...state, ...input, effective_runtime_id: input.runtime_id ?? "claude" }));
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(["workspaces", "workspace", "agents"], [{ id: "profile", owner_id: "owner", runtime_id: "claude" } as Agent]);
  client.setQueryData(["workspaces", "workspace", "members"], []);
  client.setQueryData(["runtimes", "workspace", "list"], [
    { id: "claude", visibility: "private", owner_id: "owner" },
    { id: "codex", visibility: "public", owner_id: "other" },
    { id: "private-other", visibility: "private", owner_id: "other" },
  ] as AgentRuntime[]);
});
afterEach(() => { cleanup(); client.clear(); });

it("switches only the assigned profile's issue runs and can restore its default", async () => {
  renderControl();
  await waitFor(() => expect(screen.getByRole("button", { name: "Switch to Codex" })).toBeEnabled());
  expect(screen.getByTestId("choices").textContent).toBe("claude,codex");
  mocks.get.mockResolvedValue({ ...state, runtime_id: "codex", effective_runtime_id: "codex" });
  fireEvent.click(screen.getByRole("button", { name: "Switch to Codex" }));
  await waitFor(() => expect(mocks.set).toHaveBeenCalledWith("issue", { agent_id: "profile", runtime_id: "codex" }));
  fireEvent.click(await screen.findByRole("button", { name: "Use profile default" }));
  await waitFor(() => expect(mocks.set).toHaveBeenLastCalledWith("issue", { agent_id: "profile", runtime_id: null }));
  expect(client.getQueryData(["workspaces", "workspace", "agents"])).toMatchObject([{ runtime_id: "claude" }]);
});

it("shows a running-task conflict without changing the displayed selection", async () => {
  mocks.set.mockRejectedValue(new Error("finish or cancel active runs"));
  renderControl();
  await waitFor(() => expect(screen.getByRole("button", { name: "Switch to Codex" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Switch to Codex" }));
  await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("finish or cancel active runs"));
  expect(screen.getByTestId("selection").textContent).toBe("claude");
});

it("hides the control and skips the runtime read without profile management permission", () => {
  mocks.canEdit = false; renderControl();
  expect(screen.queryByRole("button", { name: "Switch to Codex" })).toBeNull();
  expect(mocks.get).not.toHaveBeenCalled();
});

it("does not enable editing when the saved state cannot be loaded", async () => {
  mocks.get.mockRejectedValue(new Error("unavailable")); renderControl();
  expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the runtime.");
  expect(mocks.set).not.toHaveBeenCalled();
});
