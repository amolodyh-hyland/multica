import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { screen, waitFor, act } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { AgentRuntime, ProjectResource } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { ProjectKnowledgeSection } from "./project-knowledge-section";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "owner1" } }) }));
vi.mock("@multica/core/api", () => ({ api: { createProjectResource: vi.fn(), deleteProjectResource: vi.fn(), checkProjectKnowledgeStatus: vi.fn() } }));

const ref = {
  schema_version: 1 as const, owner_id: "owner1", daemon_id: "machine1", resolver_id: "machine1",
  root_ref: "root1", root_generation: 1, authority_id: "mu_local_machine1", source_id: "source1", source_revision: 1,
  entry_id: "entry1", content_hash: `sha256:${"a".repeat(64)}`, format: "knowledge-text-v1" as const, entry_point: "notes.md",
};
const resource: ProjectResource = { id: "resource1", project_id: "project1", workspace_id: "workspace1", resource_type: "knowledge_file", resource_ref: ref, label: null, position: 0, created_by: "owner1", created_at: "2026-10-23T00:00:00Z" };
function renderSection(resources: ProjectResource[] = [], runtimes: AgentRuntime[] = []) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = (items: ProjectResource[]) => <QueryClientProvider client={qc}><ProjectKnowledgeSection projectId="project1" resources={items} runtimes={runtimes} /></QueryClientProvider>;
  const rendered = renderWithI18n(view(resources));
  return { ...rendered, rerenderSection: (items: ProjectResource[]) => rendered.rerender(view(items)) };
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



it("checks current source status only after an explicit human action", async () => {
 const user=userEvent.setup();
 vi.mocked(api.checkProjectKnowledgeStatus).mockResolvedValue({results:[{resource_id:resource.id,reference_hash:ref.content_hash,status:"stale",reason:"changed",exists:true,type:"file",size_bytes:42,content_hash:`sha256:${"b".repeat(64)}`}]});
 renderSection([resource]);
 expect(api.checkProjectKnowledgeStatus).not.toHaveBeenCalled();
 expect(screen.getByText("Unverified")).toBeInTheDocument();
 await user.click(screen.getByRole("button",{name:"Check status"}));
 expect(await screen.findByText("Changed since pinned")).toBeInTheDocument();
 expect(api.checkProjectKnowledgeStatus).toHaveBeenCalledExactlyOnceWith("project1",[resource.id]);
 expect(screen.getByText(/register and approve the current content again/)).toBeInTheDocument();
});

it.each([
 ["unverified","owner_offline","Unverified","Start the owner daemon"],
 ["unverified","busy","Unverified","Wait, then check again"],
 ["unverified","timeout","Unverified","Wait, then check again"],
 ["unverified","unreadable","Unverified","Check the source permissions and registration"],
 ["incompatible","needs_newer_runner","Needs newer runner","Update Multica"],
 ["missing","missing","Missing","register and approve"],
 ["available","checked","Available",null],
] as const)("shows %s with actionable recovery",async (status,reason,label,remedy)=>{
 const user=userEvent.setup();
 vi.mocked(api.checkProjectKnowledgeStatus).mockResolvedValue({results:[{resource_id:resource.id,reference_hash:ref.content_hash,status,reason}]});
 renderSection([resource]);
 await user.click(screen.getByRole("button",{name:"Check status"}));
 await waitFor(()=>expect(api.checkProjectKnowledgeStatus).toHaveBeenCalledTimes(1));
 await waitFor(()=>expect(screen.getByRole("button",{name:"Check status"})).not.toBeDisabled());
 expect(screen.getByText(label)).toBeInTheDocument();
 if(remedy)expect(screen.getByText(new RegExp(remedy))).toBeInTheDocument();
 if(reason!=="owner_offline")expect(screen.queryByText(/Start the owner daemon/)).not.toBeInTheDocument();
 expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

afterEach(()=>vi.useRealTimers());
it("does not poll status on mount or in the background",async()=>{
 vi.useFakeTimers();renderSection([resource]);
 await act(async()=>{await vi.advanceTimersByTimeAsync(60000)});
 expect(api.checkProjectKnowledgeStatus).not.toHaveBeenCalled();
});


it("discards an old check when a binding moves to another machine with the same content hash", async () => {
 const user=userEvent.setup();
 vi.mocked(api.checkProjectKnowledgeStatus).mockResolvedValue({results:[{resource_id:resource.id,reference_hash:ref.content_hash,status:"available",reason:"checked"}]});
 const {rerenderSection}=renderSection([resource]);
 await user.click(screen.getByRole("button",{name:"Check status"}));
 expect(await screen.findByText("Available")).toBeInTheDocument();
 rerenderSection([{...resource,resource_ref:{...ref,daemon_id:"other-machine",resolver_id:"other-machine"}}]);
 expect(screen.getByText("Unverified")).toBeInTheDocument();
 expect(screen.queryByText("Available")).not.toBeInTheDocument();
 expect(api.checkProjectKnowledgeStatus).toHaveBeenCalledTimes(1);
});
