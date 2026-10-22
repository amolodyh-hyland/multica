import { afterEach, describe, expect, it, vi } from "vitest";
import { filterFolderSymlinks } from "./folder-selection";

afterEach(() => { Reflect.deleteProperty(window, "desktopAPI"); });

describe("folder selection platform boundary", () => {
  it("fails closed without a native symlink check", async () => {
    expect(await filterFolderSymlinks([])).toEqual({ files: [], skipped: [], verified: false });
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
