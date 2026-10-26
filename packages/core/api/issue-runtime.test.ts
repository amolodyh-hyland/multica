// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

const client = new ApiClient("https://api.example.test");
const state = { issue_id: "issue", agent_id: "profile", runtime_id: "codex", effective_runtime_id: "codex" };
afterEach(() => vi.unstubAllGlobals());

it("saves a fenced issue-only selection and supports resetting to the profile default", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(state)));
  vi.stubGlobal("fetch", fetch);
  await expect(client.setIssueRuntime("issue/id", { agent_id: "profile", runtime_id: "codex" })).resolves.toEqual(state);
  expect(fetch.mock.calls[0]![0]).toBe("https://api.example.test/api/issues/issue%2Fid/runtime");
  expect(fetch.mock.calls[0]![1]).toMatchObject({ method: "PUT", body: JSON.stringify({ agent_id: "profile", runtime_id: "codex" }) });
  fetch.mockResolvedValue(new Response(JSON.stringify({ ...state, runtime_id: null, effective_runtime_id: "claude" })));
  await expect(client.setIssueRuntime("issue", { agent_id: "profile", runtime_id: null })).resolves.toMatchObject({ runtime_id: null });
});

it("rejects malformed selection state instead of treating it as the profile default", async () => {
  vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ ...state, runtime_id: false })))));
  await expect(client.getIssueRuntime("issue")).rejects.toThrow("Could not load issue runtime");
  await expect(client.setIssueRuntime("issue", { agent_id: "profile", runtime_id: "codex" })).rejects.toThrow("Could not save issue runtime");
});

it("surfaces active-run conflicts", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response('{"error":"finish or cancel active runs"}', { status: 409 })));
  await expect(client.setIssueRuntime("issue", { agent_id: "profile", runtime_id: "codex" })).rejects.toThrow("finish or cancel active runs");
});
