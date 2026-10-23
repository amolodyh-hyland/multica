import { ProjectKnowledgeRefSchema } from "../api/schemas";
import type { AgentRuntime, CreateProjectResourceRequest, ProjectKnowledgeRef, ProjectKnowledgeType, ProjectResource } from "../types";

export function projectKnowledgeHasNewerReference(ref: ProjectKnowledgeRef, resources: ProjectResource[]): boolean {
  return resources.some((resource) => {
    if (!isProjectKnowledgeType(resource.resource_type)) return false;
    const candidate = projectKnowledgeRef(resource.resource_ref);
    return candidate !== null && candidate.owner_id === ref.owner_id && candidate.daemon_id === ref.daemon_id &&
      candidate.authority_id === ref.authority_id && candidate.source_id === ref.source_id &&
      (candidate.source_revision > ref.source_revision || candidate.root_generation > ref.root_generation);
  });
}

export function projectKnowledgeMachine(ref: ProjectKnowledgeRef, runtimes: AgentRuntime[]) {
  const owned = runtimes.filter((runtime) => runtime.daemon_id === ref.daemon_id && runtime.owner_id === ref.owner_id);
  owned.sort((a, b) => (b.last_seen_at ?? "").localeCompare(a.last_seen_at ?? "") || Number(b.status === "online") - Number(a.status === "online") || a.id.localeCompare(b.id));
  const runtime = owned[0];
  if (!runtime || runtime.status !== "online") return { runtime, status: "unavailable" as const };
  const capabilities = runtime.metadata?.capabilities;
  if (!Array.isArray(capabilities)) return { runtime, status: "unverified" as const };
  return { runtime, status: capabilities.includes("knowledge-context-v1") ? "available" as const : "incompatible" as const };
}

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
