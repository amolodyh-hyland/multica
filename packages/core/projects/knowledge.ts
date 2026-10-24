import { ProjectKnowledgeRefSchema } from "../api/schemas";
import type { CreateProjectResourceRequest, ProjectKnowledgeRef, ProjectKnowledgeType } from "../types";

export function isProjectKnowledgeType(value: string): value is ProjectKnowledgeType {
  return value === "knowledge_file" || value === "knowledge_folder" || value === "knowledge_skill";
}

export function parseProjectKnowledgeRegistration(value: string): CreateProjectResourceRequest | null {
  if (new TextEncoder().encode(value).length > 16384) return null;
  try {
    const input: unknown = JSON.parse(value);
    if (!input || typeof input !== "object" || !("resource_type" in input) || !("resource_ref" in input)) return null;
    const kind = input.resource_type;
    if (typeof kind !== "string" || !isProjectKnowledgeType(kind)) return null;
    if (Object.keys(input).some((key) => key !== "resource_type" && key !== "resource_ref")) return null;
    const parsed = ProjectKnowledgeRefSchema.safeParse(input.resource_ref);
    if (!parsed.success) return null;
    if (kind === "knowledge_skill" ? parsed.data.format !== "native-skill-text-v1" || parsed.data.entry_point !== "SKILL.md" : parsed.data.format !== "knowledge-text-v1") return null;
    return { resource_type: kind, resource_ref: parsed.data };
  } catch {
    return null;
  }
}

export function projectKnowledgeRef(value: unknown): ProjectKnowledgeRef | null {
  const parsed = ProjectKnowledgeRefSchema.safeParse(value);
  return parsed.success ? parsed.data : null;
}
