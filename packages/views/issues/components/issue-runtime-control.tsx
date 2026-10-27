"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueRuntimeOptions, useSetIssueRuntime } from "@multica/core/issues";
import { useAgentPermissions } from "@multica/core/permissions";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import type { Issue } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import { RuntimePicker } from "../../agents/components/inspector/runtime-picker";
import { PropRow } from "../../common/prop-row";
import { useT } from "../../i18n";

export function IssueRuntimeControl({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const agent = issue.assignee_type === "agent"
    ? agents.find((a) => a.id === issue.assignee_id) ?? null
    : null;
  const { canEdit } = useAgentPermissions(agent, wsId);
  const runtime = useQuery({
    ...issueRuntimeOptions(wsId, issue.id, agent?.id ?? ""),
    enabled: !!agent && canEdit.allowed,
  });
  const mutation = useSetIssueRuntime(wsId, issue.id, agent?.id ?? "");

  if (!agent || !canEdit.allowed) return null;
  const state = runtime.data?.agent_id === agent.id ? runtime.data : null;
  const change = async (id: string | null) => {
    try {
      await mutation.mutateAsync(id);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.runtime.save_failed));
    }
  };
  // Private destinations must permit the profile's owner as well as the
  // member editing the issue. The server repeats this check under locks.
  const choices = runtimes.filter(
    (r) => r.visibility !== "private" || r.owner_id === agent.owner_id,
  );

  return (
    <>
      <PropRow label={t(($) => $.runtime.label)} interactive={!!state && !mutation.isPending}>
        <div className="flex min-w-0 flex-col items-start py-1">
          {runtime.isError ? (
            <span role="alert">{t(($) => $.runtime.load_failed)}</span>
          ) : (
            <RuntimePicker
              value={state?.effective_runtime_id ?? agent.runtime_id}
              runtimes={choices}
              members={members}
              currentUserId={userId}
              canEdit={!!state && !mutation.isPending}
              onChange={change}
            />
          )}
          {state?.runtime_id && (
            <Button
              variant="ghost"
              size="xs"
              disabled={mutation.isPending}
              aria-busy={mutation.isPending}
              onClick={() => void change(null)}
            >
              {t(($) => $.runtime.use_default)}
            </Button>
          )}
        </div>
      </PropRow>
      <p className="col-span-2 py-1 text-caption text-muted-foreground whitespace-normal">
        {t(($) => $.runtime.hint)}
      </p>
    </>
  );
}
