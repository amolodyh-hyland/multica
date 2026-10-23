import { beforeEach, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { AgentRuntime, ProjectResource } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ProjectKnowledgeSection } from "./project-knowledge-section";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "owner1" } }) }));
vi.mock("@multica/core/api", () => ({ api: { createProjectResource: vi.fn(), deleteProjectResource: vi.fn() } }));

const ref = {
  schema_version: 1 as const, owner_id: "owner1", daemon_id: "machine1", resolver_id: "machine1",
  root_ref: "root1", root_generation: 1, authority_id: "mu_local_machine1", source_id: "source1", source_revision: 1,
  entry_id: "entry1", content_hash: `sha256:${"a".repeat(64)}`, format: "knowledge-text-v1" as const, entry_point: "notes.md",
};
const resource: ProjectResource = { id: "resource1", project_id: "project1", workspace_id: "workspace1", resource_type: "knowledge_file", resource_ref: ref, label: null, position: 0, created_by: "owner1", created_at: "2026-10-23T00:00:00Z" };
function renderSection(resources: ProjectResource[] = [], runtimes: AgentRuntime[] = []) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return renderWithI18n(<QueryClientProvider client={qc}><ProjectKnowledgeSection projectId="project1" resources={resources} runtimes={runtimes} /></QueryClientProvider>);
}
beforeEach(() => { vi.clearAllMocks(); });

it("works without resources and keeps context separate from execution", () => {
  renderSection();
  expect(screen.getByText(/project works without knowledge or a repository/)).toBeInTheDocument();
  expect(screen.getByText(/do not choose an execution directory or install skills/)).toBeInTheDocument();
  expect(api.createProjectResource).not.toHaveBeenCalled();
});

it.each(["knowledge_file", "knowledge_folder", "knowledge_skill"] as const)("attaches %s only after reviewing owner metadata", async (kind) => {
  const user = userEvent.setup();
  const resource_ref = kind === "knowledge_skill" ? { ...ref, format: "native-skill-text-v1", entry_point: "SKILL.md" } : ref;
  const payload = { resource_type: kind, resource_ref };
  vi.mocked(api.createProjectResource).mockResolvedValue({ ...resource, resource_type: kind });
  renderSection();
  await user.click(screen.getByRole("button", { name: "Add knowledge reference" }));
  expect(screen.getByText(/Only reference metadata is saved/)).toBeInTheDocument();
  const submit = screen.getByRole("button", { name: "Attach reference" });
  expect(submit).toBeDisabled();
  await user.click(screen.getByRole("textbox", { name: "Registration JSON" }));
  await user.paste(JSON.stringify(payload));
  expect(screen.getByText("Owner machine: machine1")).toBeInTheDocument();
  expect(api.createProjectResource).not.toHaveBeenCalled();
  await user.click(submit);
  await waitFor(() => expect(api.createProjectResource).toHaveBeenCalledWith("project1", payload));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});

it("blocks another owner's reference", async () => {
  const user = userEvent.setup();
  renderSection();
  await user.click(screen.getByRole("button", { name: "Add knowledge reference" }));
  await user.click(screen.getByRole("textbox"));
  await user.paste(JSON.stringify({ resource_type: "knowledge_file", resource_ref: { ...ref, owner_id: "other" } }));
  expect(screen.getByRole("alert")).toHaveTextContent("Only the registered owner");
  expect(screen.getByRole("button", { name: "Attach reference" })).toBeDisabled();
});

it("preserves input and shows server rejection", async () => {
  const user = userEvent.setup();
  vi.mocked(api.createProjectResource).mockRejectedValue(new Error("Resolver no longer owned"));
  renderSection();
  await user.click(screen.getByRole("button", { name: "Add knowledge reference" }));
  const json = JSON.stringify({ resource_type: "knowledge_file", resource_ref: ref });
  await user.click(screen.getByRole("textbox"));
  await user.paste(json);
  await user.click(screen.getByRole("button", { name: "Attach reference" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Resolver no longer owned");
  expect(screen.getByRole("textbox")).toHaveValue(json);
});

it("shows metadata and reports a failed removal", async () => {
  const user = userEvent.setup();
  vi.mocked(api.deleteProjectResource).mockRejectedValue(new Error("Not permitted"));
  renderSection([resource]);
  expect(screen.getByText("Owner: owner1")).toBeInTheDocument();
  await user.click(screen.getByText("View reference metadata"));
  expect(screen.getByText(/sha256:/)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Remove reference" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Not permitted");
  expect(screen.getByText("notes.md")).toBeInTheDocument();
});


it("shows stale references with a removal action when a newer revision is attached", () => {
  const newer = { ...resource, id: "resource2", resource_ref: { ...ref, source_revision: 2 }, label: "Updated notes" };
  renderSection([resource, newer]);
  expect(screen.getByText("Stale reference")).toBeInTheDocument();
  expect(screen.getByText(/newer approved revision is already attached/)).toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: "Remove reference" })).toHaveLength(2);
});

it("offers an upgrade remedy for an incompatible owner daemon", () => {
  const runtime: AgentRuntime = {
    id: "runtime1", workspace_id: "workspace1", daemon_id: ref.daemon_id, owner_id: ref.owner_id,
    name: "Local runtime", custom_name: "Research computer", runtime_mode: "local", provider: "opencode", launch_header: "",
    status: "online", device_info: "Owner computer", metadata: { capabilities: [] }, visibility: "private",
    last_seen_at: "2026-10-23T00:00:00Z", created_at: "", updated_at: "",
  };
  renderSection([resource], [runtime]);
  expect(screen.getByText("Incompatible daemon")).toBeInTheDocument();
  expect(screen.getByText(/Upgrade Multica on the owner machine/)).toBeInTheDocument();
  expect(screen.getByText(/Research computer/)).toBeInTheDocument();
});
