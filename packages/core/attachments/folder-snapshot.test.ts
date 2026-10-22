// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { FolderSnapshotError, MAX_FOLDER_BYTES, packFolderSnapshot, previewFolderSnapshot, uploadSizeLimit, validFolderPath } from "./folder-snapshot";

function selected(path: string, contents = "body", size?: number): File {
  const file = new File([contents], path.split("/").pop()!);
  Object.defineProperty(file, "webkitRelativePath", { value: path });
  if (size !== undefined) Object.defineProperty(file, "size", { value: size });
  return file;
}

describe("folder snapshots", () => {
  it("previews nested paths, count and exact size without reading bytes", () => {
    const a = selected("project/a.txt");
    const b = selected("project/src/a.txt", "nested");
    const read = vi.spyOn(a, "arrayBuffer");
    const preview = previewFolderSnapshot([a, b, selected("project/.git/config"), selected("project/node_modules/a/index.js")], ["project/link"]);
    expect(preview.name).toBe("project");
    expect(preview.totalBytes).toBe(10);
    expect(preview.files.map((file) => file.path)).toEqual(["project/a.txt", "project/src/a.txt"]);
    expect(preview.skipped).toEqual(["project/link", "project/.git/config", "project/node_modules/a/index.js"]);
    expect(read).not.toHaveBeenCalled();
  });

  it("accepts both exact limits and refuses the entire over-limit selection", () => {
    const files = Array.from({ length: 500 }, (_, i) => selected("project/" + i, "", 0));
    expect(previewFolderSnapshot(files).files).toHaveLength(500);
    expect(() => previewFolderSnapshot([...files, selected("project/extra")])).toThrow(new FolderSnapshotError("too_many_files"));
    expect(previewFolderSnapshot([selected("project/large", "", MAX_FOLDER_BYTES)]).totalBytes).toBe(MAX_FOLDER_BYTES);
    expect(() => previewFolderSnapshot([selected("project/large", "", MAX_FOLDER_BYTES + 1)])).toThrow(new FolderSnapshotError("too_large"));
  });

  it.each(["../outside", "/absolute", "C:/file", "root/../file", "root/./file", "root//file", "root/a\\b", "root/NUL.txt", "root/a."])("rejects unsafe path %s", (path) => {
    expect(validFolderPath(path)).toBe(false);
    expect(() => previewFolderSnapshot([selected(path)])).toThrow();
  });

  it("refuses empty selections, conflicting paths, duplicates and multiple roots", () => {
    for (const paths of [[], ["root/a", "root/a/b"], ["root/a", "root/A"], ["root/a", "other/b"]]) {
      expect(() => previewFolderSnapshot(paths.map((path) => selected(path)))).toThrow();
    }
  });

  it("creates one snapshot containing the relative names and bytes", async () => {
    const archive = await packFolderSnapshot(previewFolderSnapshot([selected("project/src/a.txt")]));
    expect(archive.name).toBe("project.multica-folder");
    expect(archive.type).toBe("application/vnd.multica.folder+zip");
    const data = new Uint8Array(await archive.arrayBuffer());
    expect(new DataView(data.buffer).getUint32(0, true)).toBe(0x04034b50);
    expect(new TextDecoder().decode(data)).toContain("project/src/a.txt");
    expect(new TextDecoder().decode(data)).toContain("body");
    expect(uploadSizeLimit(archive)).toBeGreaterThan(MAX_FOLDER_BYTES);
    expect(uploadSizeLimit({ name: "ordinary.zip" })).toBe(MAX_FOLDER_BYTES);
  });

  it("fails the entire snapshot if any file cannot be read or changed", async () => {
    const first = selected("root/a");
    const second = selected("root/b");
    vi.spyOn(second, "arrayBuffer").mockRejectedValue(new Error("read failed"));
    await expect(packFolderSnapshot(previewFolderSnapshot([first, second]))).rejects.toThrow("read failed");
    const changed = selected("root/c", "x", 2);
    await expect(packFolderSnapshot(previewFolderSnapshot([changed]))).rejects.toThrow(new FolderSnapshotError("changed"));
  });
});
