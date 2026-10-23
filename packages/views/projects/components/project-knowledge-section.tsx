"use client";

import { useState } from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { parseProjectKnowledgeRegistration, projectKnowledgeRef, projectKnowledgeMachine, projectKnowledgeHasNewerReference, useCreateProjectResource, useDeleteProjectResource } from "@multica/core/projects";
import type { AgentRuntime, ProjectResource } from "@multica/core/types";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";

export function ProjectKnowledgeSection({ projectId, resources, runtimes = [], onRefresh, refreshing }: { projectId: string; resources: ProjectResource[]; runtimes?: AgentRuntime[]; onRefresh?: () => void; refreshing?: boolean }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const userId = useAuthStore((state) => state.user?.id);
  const create = useCreateProjectResource(wsId, projectId);
  const remove = useDeleteProjectResource(wsId, projectId);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const registration = parseProjectKnowledgeRegistration(draft);
  const ref = registration && projectKnowledgeRef(registration.resource_ref);
  const registerCommand = `multica --workspace-id ${wsId} knowledge register file /absolute/notes.md --owner-id ${userId ?? "<owner-id>"} --allow-read --approve-context`;

  async function attach() {
    if (!registration || ref?.owner_id !== userId) return;
    setError(null);
    try {
      const created = await create.mutateAsync(registration);
      if (!created.id) throw new Error(t(($) => $.knowledge.save_failed));
      setOpen(false);
      setDraft("");
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.knowledge.save_failed));
    }
  }

  async function detach(id: string) {
    setRemoving(id);
    setError(null);
    try {
      await remove.mutateAsync(id);
    } catch (err) {
      setError(err instanceof Error ? err.message : t(($) => $.knowledge.remove_failed));
    } finally {
      setRemoving(null);
    }
  }

  return (
    <section className="space-y-2" aria-label={t(($) => $.knowledge.title)}>
      <h2 className="px-2 text-caption font-medium">{t(($) => $.knowledge.title)}</h2>
      <p className="pl-2 text-caption text-muted-foreground">{t(($) => $.knowledge.context_hint)}</p>
      {resources.length === 0 && <p className="pl-2 text-caption text-muted-foreground">{t(($) => $.knowledge.empty)}</p>}
      {resources.length > 0 && <>
        <p className="pl-2 text-caption text-muted-foreground">{t(($) => $.knowledge.verify_at_run)}</p>
        {onRefresh && <Button variant="ghost" size="xs" disabled={refreshing} aria-busy={refreshing} onClick={onRefresh}>{t(($) => $.knowledge.refresh)}</Button>}
      </>}
      <div className="max-h-96 space-y-2 overflow-y-auto pl-2">
        {resources.map((resource) => (
          <KnowledgeRow key={resource.id} resource={resource} resources={resources} runtimes={runtimes} removing={removing !== null} onRemove={() => void detach(resource.id)} />
        ))}
      </div>
      {error && !open && <p role="alert" className="text-caption text-destructive">{error}</p>}
      <Dialog open={open} onOpenChange={(value) => { if (!create.isPending) { setOpen(value); setError(null); } }}>
        <DialogTrigger render={<Button variant="ghost" size="sm" />}>{t(($) => $.knowledge.add)}</DialogTrigger>
        <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t(($) => $.knowledge.add)}</DialogTitle>
            <DialogDescription>{t(($) => $.knowledge.registration_hint)}</DialogDescription>
          </DialogHeader>
          <pre className="whitespace-pre-wrap break-all rounded-md bg-muted p-2 text-micro">{registerCommand}</pre>
          <label className="space-y-2 text-caption">
            {t(($) => $.knowledge.registration)}
            <Textarea value={draft} onChange={(event) => { setDraft(event.target.value); setError(null); }} className="min-h-36 font-mono" aria-invalid={!!draft && !registration} />
          </label>
          {draft && !registration && <p role="alert" className="text-caption text-destructive">{t(($) => $.knowledge.invalid)}</p>}
          {ref && <div className="space-y-1 break-all text-caption">
            <p>{t(($) => $.knowledge.owner, { owner: ref.owner_id })}</p>
            <p>{t(($) => $.knowledge.machine, { machine: ref.daemon_id })}</p>
            <p>{ref.entry_point}</p>
            {ref.owner_id !== userId && <p role="alert" className="text-destructive">{t(($) => $.knowledge.owner_only)}</p>}
          </div>}
          {error && <p role="alert" className="text-caption text-destructive">{error}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={create.isPending} onClick={() => setOpen(false)}>{t(($) => $.knowledge.cancel)}</Button>
            <Button disabled={!registration || ref?.owner_id !== userId || create.isPending} aria-busy={create.isPending} onClick={() => void attach()}>{t(($) => $.knowledge.attach)}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}

function KnowledgeRow({ resource, resources, runtimes, removing, onRemove }: { resource: ProjectResource; resources: ProjectResource[]; runtimes: AgentRuntime[]; removing: boolean; onRemove: () => void }) {
  const { t } = useT("projects");
  const ref = projectKnowledgeRef(resource.resource_ref);
  const machine = ref && projectKnowledgeMachine(ref, runtimes);
  const stale = ref && projectKnowledgeHasNewerReference(ref, resources);
  return (
    <article className="rounded-md border p-2 space-y-2 text-caption break-all">
      <p className="font-medium">{resource.label || ref?.entry_point || resource.resource_type}</p>
      <p>{resource.resource_type === "knowledge_file" ? t(($) => $.knowledge.file) : resource.resource_type === "knowledge_folder" ? t(($) => $.knowledge.folder) : t(($) => $.knowledge.skill)}</p>
      {ref && <>
        <p>{t(($) => $.knowledge.owner, { owner: ref.owner_id })}</p>
        <p>{t(($) => $.knowledge.machine, { machine: ref.daemon_id })}</p>
        {machine?.runtime && <p>{runtimeDisplayLabel(machine.runtime)}</p>}
      </>}
      <p>{stale ? t(($) => $.knowledge.stale) : machine?.status === "available" ? t(($) => $.knowledge.available) : machine?.status === "incompatible" ? t(($) => $.knowledge.incompatible) : machine?.status === "unverified" ? t(($) => $.knowledge.unverified) : t(($) => $.knowledge.unavailable)}</p>
      {(stale || machine?.status === "incompatible" || machine?.status === "unavailable") && <p className="text-muted-foreground">{stale ? t(($) => $.knowledge.stale_hint) : machine?.status === "incompatible" ? t(($) => $.knowledge.upgrade) : t(($) => $.knowledge.reconnect)}</p>}
      <details>
        <summary className="cursor-pointer">{t(($) => $.knowledge.changed_source)}</summary>
        <p className="mt-2 text-muted-foreground">{t(($) => $.knowledge.replace_hint)}</p>
      </details>
      <details>
        <summary className="cursor-pointer">{t(($) => $.knowledge.details)}</summary>
        <pre className="whitespace-pre-wrap text-micro">{JSON.stringify(resource.resource_ref, null, 2)}</pre>
      </details>
      <Button variant="ghost" size="xs" disabled={removing} onClick={onRemove}>{t(($) => $.knowledge.remove)}</Button>
    </article>
  );
}
