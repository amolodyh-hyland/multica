// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { ProjectResourceSchema } from "./schemas";

const ref = {
  schema_version: 1, owner_id: "owner1", daemon_id: "machine1", resolver_id: "machine1",
  root_ref: "root1", root_generation: 1, authority_id: "mu_local_machine1", source_id: "source1", source_revision: 1,
  entry_id: "entry1", content_hash: `sha256:${"a".repeat(64)}`, format: "knowledge-text-v1", entry_point: "notes.md",
};
const resource = { id: "resource1", project_id: "project1", workspace_id: "workspace1", resource_type: "knowledge_file", resource_ref: ref, created_at: "2026-10-23T00:00:00Z" };
afterEach(() => vi.unstubAllGlobals());

it("keeps knowledge metadata typed separately and accepts existing resources", () => {
  expect(ProjectResourceSchema.parse(resource).resource_ref).toEqual(ref);
  expect(ProjectResourceSchema.parse({ ...resource, resource_type: "local_directory", resource_ref: { daemon_id: "machine1", local_path: "/work", execution_mode: "worktree" } }).resource_ref).toMatchObject({ execution_mode: "worktree" });
  expect(ProjectResourceSchema.parse({ ...resource, resource_type: "future_resource", resource_ref: {} }).resource_type).toBe("future_resource");
  for (const invalid of [{ ...ref, schema_version: 2 }, { ...ref, local_path: "/private" }, { ...ref, content_hash: "" }, { ...ref, resolver_id: "other" }]) {
    expect(ProjectResourceSchema.safeParse({ ...resource, resource_ref: invalid }).success).toBe(false);
  }
});

it("validates list, create and update payloads and handles malformed responses", async () => {
  const mock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ resources: [resource], total: 1 })));
  vi.stubGlobal("fetch", mock);
  const client = new ApiClient("https://server.test");
  await expect(client.listProjectResources("project1")).resolves.toMatchObject({ resources: [{ resource_type: "knowledge_file" }] });
  mock.mockImplementation(async () => new Response(JSON.stringify({ ...resource, resource_ref: { local_path: "/private" } })));
  await expect(client.createProjectResource("project1", { resource_type: "knowledge_file", resource_ref: ref })).resolves.toMatchObject({ id: "" });
  await expect(client.updateProjectResource("project1", "resource1", { label: "Notes" })).resolves.toMatchObject({ id: "" });
  mock.mockResolvedValueOnce(new Response(JSON.stringify({ resources: [{ ...resource, resource_ref: null }], total: 1 })));
  await expect(client.listProjectResources("project1")).resolves.toEqual({ resources: [], total: 0 });
});

it("checks status using resource IDs and parses only metadata", async () => {
 const status={resource_id:resource.id,reference_hash:ref.content_hash,status:"stale",reason:"changed",exists:true,type:"file",size_bytes:10,content_hash:`sha256:${"b".repeat(64)}`};
 const mock=vi.fn().mockImplementation(async()=>new Response(JSON.stringify({results:[status]})));
 vi.stubGlobal("fetch",mock);
 const client=new ApiClient("https://server.test");
 await expect(client.checkProjectKnowledgeStatus("project1",[resource.id])).resolves.toEqual({results:[status]});
 expect(JSON.parse(mock.mock.calls[0]?.[1]?.body)).toEqual({resource_ids:[resource.id]});
 for(const invalid of [{...status,content:"private text"},{...status,content_hash:"bad"},{...status,exists:"yes"}]){
  mock.mockResolvedValueOnce(new Response(JSON.stringify({results:[invalid]})));
  await expect(client.checkProjectKnowledgeStatus("project1",[resource.id])).resolves.toEqual({results:[]});
 }
 mock.mockResolvedValueOnce(new Response(JSON.stringify({results:[{...status,status:"future_state"}]})));
 await expect(client.checkProjectKnowledgeStatus("project1",[resource.id])).resolves.toMatchObject({results:[{status:"unverified"}]});
});
