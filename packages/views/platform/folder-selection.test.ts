import { afterEach, describe, expect, it, vi } from "vitest";
import { filterFolderSymlinks } from "./folder-selection";

afterEach(() => { Reflect.deleteProperty(window, "desktopAPI"); });

describe("folder selection platform boundary", () => {
  it("returns browser files without claiming native symlink verification", async () => {
    expect(await filterFolderSymlinks([])).toEqual({ files: [], skipped: [], verified: false });
  });

  it("uses the browser path in an embedded view without the native filtering method", async () => {
    const files = [new File(["contents"], "linked-file.txt")];
    Object.defineProperty(window, "desktopAPI", { configurable: true, value: { pickDirectory: vi.fn() } });
    expect(await filterFolderSymlinks(files)).toEqual({ files, skipped: [], verified: false });
  });

  it("preserves relative paths and lists the files excluded by the desktop check", async () => {
    const files = ["root/a", "root/link"].map((path) => {
      const file = new File(["body"], path);
      Object.defineProperty(file, "webkitRelativePath", { value: path });
      return file;
    });
    const check = vi.fn().mockResolvedValue([1]);
    Object.defineProperty(window, "desktopAPI", { configurable: true, value: { folderSelectionSymlinks: check } });
    expect(await filterFolderSymlinks(files)).toEqual({ files: [files[0]], skipped: ["root/link"], verified: true });
    expect(check).toHaveBeenCalledWith(files);
  });
});
