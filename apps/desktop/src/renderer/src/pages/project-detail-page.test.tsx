import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { ProjectDetailPage } from "./project-detail-page";

vi.mock("react-router-dom", () => ({ useParams: () => ({ id: "project-with-context" }) }));
vi.mock("@tanstack/react-query", () => ({ useQuery: () => ({ data: { title: "Research" } }) }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace1" }));
vi.mock("@multica/core/projects/queries", () => ({ projectDetailOptions: vi.fn() }));
vi.mock("@/hooks/use-document-title", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@multica/views/projects/components", () => ({
  ProjectDetail: ({ projectId }: { projectId: string }) => <div data-testid="shared-project">{projectId}</div>,
}));

it("opens the shared Project editor in the desktop route", () => {
  render(<ProjectDetailPage />);
  expect(screen.getByTestId("shared-project")).toHaveTextContent("project-with-context");
});
