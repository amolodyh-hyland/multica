import { Suspense, act } from "react";
import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { useIssueViewUrlSync } from "../../../../../platform/use-issue-view-url-sync";
import ProjectDetailPage from "./page";

vi.mock("../../../../../platform/use-issue-view-url-sync", () => ({
  useIssueViewUrlSync: vi.fn(),
}));

vi.mock("@multica/views/projects/components", () => ({
  ProjectDetail: ({ projectId }: { projectId: string }) => <div data-testid="shared-project">{projectId}</div>,
}));

it("opens the shared Project editor for the route's Project", async () => {
  const params = Promise.resolve({ id: "project-with-context" });
  await act(async () => { render(<Suspense><ProjectDetailPage params={params} /></Suspense>); });
  expect(await screen.findByTestId("shared-project")).toHaveTextContent("project-with-context");
  expect(useIssueViewUrlSync).toHaveBeenCalledWith({ scope_type: "project", scope_id: "project-with-context" });
});
