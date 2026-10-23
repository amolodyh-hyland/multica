// @vitest-environment jsdom
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { expect, it, vi } from "vitest";
import { api } from "../api";
import { projectResourceKeys, useDeleteProjectResource } from "./resource-queries";

vi.mock("../api", () => ({ api: { deleteProjectResource: vi.fn() } }));

it("keeps a reference until removal succeeds and preserves it on failure", async () => {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  const key = projectResourceKeys.list("workspace1", "project1");
  const original = { resources: [{ id: "reference1" }], total: 1 };
  qc.setQueryData(key, original);
  let reject!: (error: Error) => void;
  vi.mocked(api.deleteProjectResource).mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail; }));
  const { result } = renderHook(() => useDeleteProjectResource("workspace1", "project1"), {
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>,
  });
  let pending!: Promise<unknown>;
  await act(async () => { pending = result.current.mutateAsync("reference1").catch(() => {}); });
  expect(qc.getQueryData(key)).toEqual(original);
  await act(async () => { reject(new Error("Not permitted")); await pending; });
  expect(qc.getQueryData(key)).toEqual(original);
  vi.mocked(api.deleteProjectResource).mockResolvedValueOnce(undefined);
  await act(async () => { await result.current.mutateAsync("reference1"); });
  expect(qc.getQueryData(key)).toEqual({ resources: [], total: 0 });
  qc.clear();
});
