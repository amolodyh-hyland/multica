// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { ProjectSchema, SearchProjectsResponseSchema } from "./schemas";

const project = {
  id: "project-1", workspace_id: "workspace-1", title: "Research", description: "Summary",
  icon: null, status: "planned", priority: "none", lead_type: null, lead_id: null,
  created_at: "2026-10-22T00:00:00Z", updated_at: "2026-10-22T00:00:00Z",
};

afterEach(() => vi.unstubAllGlobals());

describe("Project instructions compatibility", () => {
  it("defaults older server responses and preserves separate instructions and description", () => {
    expect(ProjectSchema.parse(project).instructions).toBeNull();
    const current = { ...project, instructions: "Follow the review checklist" };
    expect(ProjectSchema.parse(current)).toMatchObject({ description: "Summary", instructions: current.instructions });
    expect(SearchProjectsResponseSchema.parse({ projects: [{ ...current, match_source: "title" }] }).projects[0]?.instructions).toBe(current.instructions);
    expect(ProjectSchema.safeParse({ ...project, instructions: 123 }).success).toBe(false);
  });

  it("validates every Project response and defaults missing instructions", async () => {
    const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify(project), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    await expect(client.getProject(project.id)).resolves.toMatchObject({ id: project.id, instructions: null });
    await expect(client.createProject({ title: project.title, instructions: "Review the evidence" })).resolves.toMatchObject({ instructions: null });
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body))).toEqual({ title: project.title, instructions: "Review the evidence" });
    await client.updateProject(project.id, { instructions: null });
    expect(JSON.parse(String(fetchMock.mock.calls[2]?.[1]?.body))).toEqual({ instructions: null });
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ projects: [project], total: 1 }), { status: 200 }));
    await expect(client.listProjects()).resolves.toMatchObject({ projects: [{ id: project.id, instructions: null }], total: 1 });
  });

  it("falls back on malformed create, read, update and list responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async () => new Response(JSON.stringify({ ...project, instructions: false, projects: [{ ...project, instructions: false }] }), { status: 200 })));
    const client = new ApiClient("https://api.example.test");
    for (const result of [client.getProject(project.id), client.createProject({ title: "Research" }), client.updateProject(project.id, { instructions: "Review" })]) {
      await expect(result).resolves.toMatchObject({ id: "", instructions: null });
    }
    await expect(client.listProjects()).resolves.toEqual({ projects: [], total: 0 });
  });
});
