import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { NavigationProvider } from "@multica/views/navigation";
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
  render(
    <NavigationProvider value={{
      pathname: "/acme/projects/project-with-context",
      searchParams: new URLSearchParams(),
      hash: "",
      push: vi.fn(), replace: vi.fn(), back: vi.fn(),
      getShareableUrl: (path) => `https://app.example${path}`,
    }}>
      <ProjectDetailPage />
    </NavigationProvider>,
  );
  expect(screen.getByTestId("shared-project")).toHaveTextContent("project-with-context");
});
