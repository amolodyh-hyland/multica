import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "./queries";
import { agentTaskSnapshotKeys } from "../agents/queries";

export function issueRuntimeOptions(workspaceId: string, issueId: string, agentId: string) {
  return queryOptions({
    queryKey: [...issueKeys.all(workspaceId), "runtime", issueId, agentId],
    queryFn: () => api.getIssueRuntime(issueId),
    enabled: !!workspaceId && !!issueId && !!agentId,
  });
}

export function useSetIssueRuntime(workspaceId: string, issueId: string, agentId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (runtimeId: string | null) => api.setIssueRuntime(issueId, { agent_id: agentId, runtime_id: runtimeId }),
    onSettled: () => Promise.all([
      client.invalidateQueries({ queryKey: issueKeys.all(workspaceId) }),
      client.invalidateQueries({ queryKey: issueKeys.timeline(issueId) }),
      client.invalidateQueries({ queryKey: issueKeys.tasks(issueId) }),
      client.invalidateQueries({ queryKey: agentTaskSnapshotKeys.all(workspaceId) }),
    ]),
  });
}
