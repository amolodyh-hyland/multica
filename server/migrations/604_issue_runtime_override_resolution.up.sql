CREATE OR REPLACE FUNCTION issue_runtime_for_run(p_agent_id UUID, p_issue_id UUID)
RETURNS UUID LANGUAGE plpgsql VOLATILE AS $$
DECLARE selected_runtime UUID;
BEGIN
    -- Match the ownership fence: workspace before profile. The profile share
    -- lock serializes enqueue with default and issue runtime changes.
    PERFORM 1 FROM workspace w JOIN agent a ON a.workspace_id = w.id
    WHERE a.id = p_agent_id FOR KEY SHARE OF w;
    PERFORM 1 FROM agent WHERE id = p_agent_id FOR SHARE;
    -- Freeze assignment for the snapshot, including retries whose lateral
    -- resolution runs before the outer ownership fence.
    PERFORM 1 FROM issue i JOIN agent a ON a.workspace_id = i.workspace_id
    WHERE i.id = p_issue_id AND a.id = p_agent_id FOR KEY SHARE OF i;
    SELECT o.runtime_id INTO selected_runtime
    FROM issue_runtime_override o
    JOIN issue i ON i.id = o.issue_id AND i.workspace_id = o.workspace_id
    JOIN agent a ON a.id = o.agent_id AND a.workspace_id = o.workspace_id
    JOIN agent_runtime r ON r.id = o.runtime_id AND r.workspace_id = o.workspace_id
    WHERE o.issue_id = p_issue_id AND o.agent_id = p_agent_id
      AND i.assignee_type = 'agent' AND i.assignee_id = p_agent_id;
    RETURN selected_runtime;
END;
$$;
