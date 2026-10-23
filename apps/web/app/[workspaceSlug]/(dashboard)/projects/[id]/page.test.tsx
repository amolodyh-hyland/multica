import { Suspense, act } from "react";
import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import ProjectDetailPage from "./page";

vi.mock("@multica/views/projects/components", () => ({
  ProjectDetail: ({ projectId }: { projectId: string }) => <div data-testid="shared-project">{projectId}</div>,
}));

it("opens the shared Project editor for the route's Project", async () => {
  const params = Promise.resolve({ id: "project-with-context" });
  await act(async () => { render(<Suspense><ProjectDetailPage params={params} /></Suspense>); });
  expect(await screen.findByTestId("shared-project")).toHaveTextContent("project-with-context");
});
