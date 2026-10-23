// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseProjectKnowledgeRegistration } from "./knowledge";

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
