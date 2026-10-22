import { useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enChat from "../../locales/en/chat.json";
import enCommon from "../../locales/en/common.json";
import { ChatFolderAttachment } from "./chat-folder-attachment";
import { ChatAddMenu } from "./chat-add-menu";

const mocks = vi.hoisted(() => ({
  filter: vi.fn(),
  server: "https://multica.example",
  workspaceId: "ws-1",
}));
vi.mock("@multica/core/api", () => ({ api: { getBaseUrl: () => mocks.server } }));
vi.mock("@multica/core/paths", () => ({ useCurrentWorkspace: () => ({ id: mocks.workspaceId, name: "Design" }) }));
vi.mock("@multica/core/platform", () => ({ getCurrentWsId: () => mocks.workspaceId }));
vi.mock("../../platform/folder-selection", () => ({ filterFolderSymlinks: mocks.filter }));

function file(path = "project/src/readme.txt") {
  const selected = new File(["contents"], path.split("/").pop()!);
  Object.defineProperty(selected, "webkitRelativePath", { value: path });
  Object.defineProperty(selected, "arrayBuffer", { value: vi.fn(async () => new TextEncoder().encode("contents").buffer) });
  return selected;
}

function Wrapper({ children }: { children: React.ReactNode }) {
  return <I18nProvider locale="en" resources={{ en: { chat: enChat, common: enCommon } }}>{children}</I18nProvider>;
}

function setup(files = [file()]) {
  const upload = vi.fn();
  const close = vi.fn();
  function Harness() {
    const [open, setOpen] = useState(true);
    return open ? <ChatFolderAttachment files={files} onSelectFile={upload} onClose={() => { close(); setOpen(false); }} /> : null;
  }
  return { ...render(<Harness />, { wrapper: Wrapper }), upload, close, files };
}

beforeEach(() => {
  mocks.server = "https://multica.example";
  mocks.workspaceId = "ws-1";
  mocks.filter.mockReset().mockImplementation(async (files: File[]) => ({ files, skipped: [], verified: true }));
});

describe("folder consent", () => {
  it("shows the destination, name, count, size and full list before any read or upload", async () => {
    const { files, upload } = setup();
    expect(await screen.findByText("project")).toBeInTheDocument();
    expect(screen.getByText("https://multica.example")).toBeInTheDocument();
    expect(screen.getByText("Design (ws-1)")).toBeInTheDocument();
    expect(screen.getByText("1 files · 0.00 MiB (8 bytes)")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Files to send" })).toHaveTextContent("project/src/readme.txt");
    expect(files[0]!.arrayBuffer).not.toHaveBeenCalled();
    expect(upload).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Confirm upload" }));
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1));
    expect(upload.mock.calls[0]![0].name).toBe("project.multica-folder");
  });

  it("cancels without reading or sending bytes", async () => {
    const { files, upload, close } = setup();
    await screen.findByText("project");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(close).toHaveBeenCalledTimes(1);
    expect(files[0]!.arrayBuffer).not.toHaveBeenCalled();
    expect(upload).not.toHaveBeenCalled();
  });

  it("displays skipped junk and symlinks in the preview", async () => {
    mocks.filter.mockImplementation(async (files: File[]) => ({ files: files.slice(0, 2), skipped: ["project/link"], verified: true }));
    const { upload } = setup([file(), file("project/.git/config"), file("project/link")]);
    await screen.findByText("project");
    expect(screen.getByText("project/.git/config")).toBeInTheDocument();
    expect(screen.getByText("project/link")).toBeInTheDocument();
    expect(upload).not.toHaveBeenCalled();
  });

  it("refuses unverifiable browser selections", async () => {
    mocks.filter.mockResolvedValue({ files: [file()], skipped: [], verified: false });
    const { upload } = setup();
    expect(await screen.findByRole("alert")).toHaveTextContent("cannot verify symlinks");
    expect(screen.getByRole("button", { name: "Confirm upload" })).toBeDisabled();
    expect(upload).not.toHaveBeenCalled();
  });

  it("keeps a file read failure visible and never uploads a partial snapshot", async () => {
    const good = file("project/a");
    const bad = file("project/b");
    vi.mocked(bad.arrayBuffer).mockRejectedValue(new Error("unreadable"));
    const { upload } = setup([good, bad]);
    await screen.findByText("project");
    fireEvent.click(screen.getByRole("button", { name: "Confirm upload" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Nothing was uploaded");
    expect(upload).not.toHaveBeenCalled();
  });

  it("stops an in-progress local pack when cancelled", async () => {
    const selected = file();
    let finish!: (data: ArrayBuffer) => void;
    vi.mocked(selected.arrayBuffer).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    const { upload } = setup([selected]);
    await screen.findByText("project");
    fireEvent.click(screen.getByRole("button", { name: "Confirm upload" }));
    await waitFor(() => expect(selected.arrayBuffer).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await act(async () => finish(new TextEncoder().encode("contents").buffer));
    expect(upload).not.toHaveBeenCalled();
  });

  it("refuses to upload when the destination changes during preparation", async () => {
    const selected = file();
    vi.mocked(selected.arrayBuffer).mockImplementation(async () => {
      mocks.workspaceId = "ws-other";
      return new TextEncoder().encode("contents").buffer;
    });
    const { upload } = setup([selected]);
    await screen.findByText("project");
    fireEvent.click(screen.getByRole("button", { name: "Confirm upload" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("changed");
    expect(upload).not.toHaveBeenCalled();
  });
});

describe("chat add menu", () => {
  it("offers separate file and folder actions and preserves project context", async () => {
    const upload = vi.fn();
    const { container } = render(<ChatAddMenu onSelectFile={upload} onSelectProject={vi.fn()} />, { wrapper: Wrapper });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(await screen.findByRole("menuitem", { name: "Attach file" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Attach folder" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /Project context/ })).toBeInTheDocument();
    fireEvent.change(container.querySelector("input[webkitdirectory]")!, { target: { files: [file()] } });
    await screen.findByText("project");
    expect(upload).not.toHaveBeenCalled();
  });
});
