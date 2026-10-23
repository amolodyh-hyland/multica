// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseProjectKnowledgeRegistration, projectKnowledgeMachine, projectKnowledgeHasNewerReference } from "./knowledge";

const ref = {
  schema_version: 1 as const, owner_id: "owner1", daemon_id: "machine1", resolver_id: "machine1",
  root_ref: "root1", root_generation: 1, authority_id: "mu_local_machine1", source_id: "source1", source_revision: 1,
  entry_id: "entry1", content_hash: `sha256:${"a".repeat(64)}`, format: "knowledge-text-v1" as const, entry_point: "notes.md",
};

describe("knowledge registration metadata", () => {
  it.each(["knowledge_file", "knowledge_folder", "knowledge_skill"])("accepts CLI %s output", (kind) => {
    const resource_ref = kind === "knowledge_skill" ? { ...ref, format: "native-skill-text-v1", entry_point: "SKILL.md" } : ref;
    const registration = { resource_type: kind, resource_ref };
    expect(parseProjectKnowledgeRegistration(JSON.stringify(registration))).toEqual(registration);
  });

  it.each([
    null, [], {}, { resource_type: "local_directory", resource_ref: ref },
    { resource_type: "knowledge_file", resource_ref: { ...ref, local_path: "/private/notes.md" } },
    { resource_type: "knowledge_file", resource_ref: ref, files: ["private text"] },
    { resource_type: "knowledge_file", resource_ref: { ...ref, resolver_id: "other" } },
    { resource_type: "knowledge_skill", resource_ref: ref },
    { resource_type: "knowledge_file", resource_ref: { ...ref, format: "native-skill-text-v1" } },
    { resource_type: "knowledge_file", resource_ref: { ...ref, schema_version: 2 } },
  ])("rejects invalid or non-metadata registration %j", (input) => {
    expect(parseProjectKnowledgeRegistration(JSON.stringify(input))).toBeNull();
  });

  it("rejects malformed and oversized input", () => {
    expect(parseProjectKnowledgeRegistration("{bad")).toBeNull();
    expect(parseProjectKnowledgeRegistration(" ".repeat(16385))).toBeNull();
  });
});

const runtime: import("../types").AgentRuntime = {
  id: "runtime1", workspace_id: "workspace1", daemon_id: ref.daemon_id, owner_id: ref.owner_id,
  name: "Local runtime", custom_name: "Research machine", runtime_mode: "local", provider: "opencode", launch_header: "",
  status: "online", device_info: "Owner computer", metadata: { capabilities: ["knowledge-context-v1"] },
  visibility: "private", last_seen_at: "2026-10-23T00:00:00Z", created_at: "", updated_at: "",
};

describe("knowledge owner-machine status", () => {
  it("requires an online runtime on the exact owner machine", () => {
    expect(projectKnowledgeMachine(ref, [runtime]).status).toBe("available");
    for (const rows of [[], [{ ...runtime, daemon_id: "remote" }], [{ ...runtime, owner_id: "other" }], [{ ...runtime, status: "offline" as const }]]) {
      expect(projectKnowledgeMachine(ref, rows).status).toBe("unavailable");
    }
  });

  it("distinguishes absent capability reports from an incompatible daemon", () => {
    expect(projectKnowledgeMachine(ref, [{ ...runtime, metadata: {} }]).status).toBe("unverified");
    expect(projectKnowledgeMachine(ref, [{ ...runtime, metadata: { capabilities: [] } }]).status).toBe("incompatible");
  });

  it("uses the newest runtime report after downgrade, independent of input order", () => {
    const older = { ...runtime, id: "old", last_seen_at: "2026-10-22T00:00:00Z" };
    const newer = { ...runtime, metadata: { capabilities: [] } };
    expect(projectKnowledgeMachine(ref, [older, newer]).status).toBe("incompatible");
    expect(projectKnowledgeMachine(ref, [newer, older]).status).toBe("incompatible");
  });
});

it("marks a superseded approved revision stale without inferring live file freshness", () => {
  const resource = { resource_type: "knowledge_file", resource_ref: { ...ref, source_revision: 2 } } as import("../types").ProjectResource;
  expect(projectKnowledgeHasNewerReference(ref, [resource])).toBe(true);
  expect(projectKnowledgeHasNewerReference(ref, [{ ...resource, resource_ref: ref }])).toBe(false);
  expect(projectKnowledgeHasNewerReference(ref, [{ ...resource, resource_ref: { ...ref, source_revision: 2, owner_id: "other" } }])).toBe(false);
  expect(projectKnowledgeHasNewerReference(ref, [{ ...resource, resource_ref: { ...ref, source_revision: 2, daemon_id: "other", resolver_id: "other" } }])).toBe(false);
});
