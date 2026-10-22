import { lstat } from "node:fs/promises";
import { dirname, isAbsolute, join, normalize } from "node:path";

export interface FolderSelectionEntry {
  path: string;
  relativePath: string;
}

export async function folderSelectionSymlinks(entries: FolderSelectionEntry[]): Promise<number[]> {
  const skipped: number[] = [];
  const checked = new Map<string, boolean>();
  for (const [index, entry] of entries.entries()) {
    const parts = entry.relativePath.split("/");
    if (!isAbsolute(entry.path) || parts.length < 2 || parts.some((part) => !part || part === "." || part === ".." || /[\\:]/.test(part))) {
      throw new Error("Invalid folder selection");
    }
    let root = entry.path;
    for (let i = 1; i < parts.length; i++) root = dirname(root);
    if (normalize(join(dirname(root), ...parts)) !== normalize(entry.path)) throw new Error("Folder selection path changed");
    let current = root;
    let symlink = false;
    for (let i = 0; i < parts.length; i++) {
      if (i) current = join(current, parts[i]!);
      let link = checked.get(current);
      if (link === undefined) {
        link = (await lstat(current)).isSymbolicLink();
        checked.set(current, link);
      }
      if (link) { symlink = true; break; }
    }
    if (symlink) skipped.push(index);
  }
  return skipped;
}
