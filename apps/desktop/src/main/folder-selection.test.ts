// @vitest-environment node
import { mkdtemp, mkdir, writeFile, symlink, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { describe, expect, it } from "vitest";
import { folderSelectionSymlinks } from "./folder-selection";

describe("folder selection symlinks", () => {
  it("skips linked files, linked directories and a linked selection root", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-folder-"));
    try {
      const root = join(dir, "root");
      await mkdir(root);
      await mkdir(join(root, "nested"));
      await writeFile(join(root, "nested", "a"), "content");
      await symlink(join(root, "nested", "a"), join(root, "link"));
      await symlink(join(root, "nested"), join(root, "linked-dir"));
      await symlink(root, join(dir, "linked-root"));
      const paths = ["root/nested/a", "root/link", "root/linked-dir/a", "linked-root/nested/a"];
      expect(await folderSelectionSymlinks(paths.map((relativePath) => ({ path: join(dir, relativePath), relativePath })))).toEqual([1, 2, 3]);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });

  it("refuses invalid picker paths", async () => {
    await expect(folderSelectionSymlinks([{ path: "/tmp/root/a", relativePath: "root/../a" }])).rejects.toThrow();
    await expect(folderSelectionSymlinks([{ path: "/tmp/root/a", relativePath: "other/a" }])).rejects.toThrow();
  });
});
