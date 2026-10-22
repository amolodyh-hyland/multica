import { packStoreZip } from "../skills/pack-archive";
import { caseFold } from "unicode-case-folding";

export const FOLDER_SNAPSHOT_EXTENSION = ".multica-folder";
export const FOLDER_SNAPSHOT_TYPE = "application/vnd.multica.folder+zip";
export const MAX_FOLDER_FILES = 500;
export const MAX_FOLDER_BYTES = 100 * 1024 * 1024;
export const MAX_FOLDER_ARCHIVE_BYTES = MAX_FOLDER_BYTES + 2 * 1024 * 1024;

export type FolderSnapshotErrorCode = "empty" | "too_many_files" | "too_large" | "unsafe_path" | "changed";

export class FolderSnapshotError extends Error {
  constructor(public readonly code: FolderSnapshotErrorCode) {
    super(code);
  }
}

export interface FolderSnapshot {
  name: string;
  files: { path: string; file: File }[];
  skipped: string[];
  totalBytes: number;
}

export function validFolderPath(path: string): boolean {
  if (!path || new TextEncoder().encode(path).length > 1024 || /[\\:<>"|?*]/.test(path)) return false;
  if ([...path].some((character) => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127)) return false;
  return path.split("/").every((part) =>
    !!part && part !== "." && part !== ".." && !/[. ]$/.test(part)
    && !/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(part),
  );
}

export function previewFolderSnapshot(files: File[], skipped: string[] = []): FolderSnapshot {
  const snapshot: FolderSnapshot = { name: "", files: [], skipped: [...skipped], totalBytes: 0 };
  const seen = new Set<string>();
  const directories = new Map<string, string>();
  for (const file of files) {
    const path = file.webkitRelativePath;
    if (!validFolderPath(path) || !path.includes("/")) throw new FolderSnapshotError("unsafe_path");
    const parts = path.split("/");
    if (!snapshot.name) snapshot.name = parts[0]!;
    if (snapshot.name !== parts[0]) throw new FolderSnapshotError("unsafe_path");
    if (parts.slice(1, -1).some((part) => part === ".git" || part === "node_modules")) {
      snapshot.skipped.push(path);
      continue;
    }
    const key = folderPathKey(path);
    if (seen.has(key)) throw new FolderSnapshotError("unsafe_path");
    seen.add(key);
    let parent = path;
    let parentKey = key;
    while (parent.includes("/")) {
      parent = parent.slice(0, parent.lastIndexOf("/"));
      parentKey = parentKey.slice(0, parentKey.lastIndexOf("/"));
      const original = directories.get(parentKey);
      if (original !== undefined && original !== parent) throw new FolderSnapshotError("unsafe_path");
      directories.set(parentKey, parent);
    }
    snapshot.files.push({ path, file });
    snapshot.totalBytes += file.size;
    if (snapshot.files.length > MAX_FOLDER_FILES) throw new FolderSnapshotError("too_many_files");
    if (snapshot.totalBytes > MAX_FOLDER_BYTES) throw new FolderSnapshotError("too_large");
  }
  if (!snapshot.files.length) throw new FolderSnapshotError("empty");
  for (const key of directories.keys()) {
    if (seen.has(key)) throw new FolderSnapshotError("unsafe_path");
  }
  snapshot.files.sort((a, b) => a.path.localeCompare(b.path));
  return snapshot;
}

function folderPathKey(path: string): string {
  return caseFold(path.normalize("NFC")).normalize("NFC");
}

export async function packFolderSnapshot(snapshot: FolderSnapshot): Promise<File> {
  const checked = previewFolderSnapshot(snapshot.files.map(({ file }) => file));
  if (checked.totalBytes !== snapshot.totalBytes || checked.name !== snapshot.name
    || checked.files.some((entry, index) => entry.path !== snapshot.files[index]?.path)) {
    throw new FolderSnapshotError("changed");
  }
  const entries = [];
  for (const { path, file } of checked.files) {
    const data = new Uint8Array(await file.arrayBuffer());
    if (data.byteLength !== file.size) throw new FolderSnapshotError("changed");
    entries.push({ path, data });
  }
  const zip = packStoreZip(entries);
  if (zip.byteLength > MAX_FOLDER_ARCHIVE_BYTES) throw new FolderSnapshotError("too_large");
  return new File([zip.buffer as ArrayBuffer], snapshot.name + FOLDER_SNAPSHOT_EXTENSION, { type: FOLDER_SNAPSHOT_TYPE });
}

export function uploadSizeLimit(file: Pick<File, "name">): number {
  return file.name.endsWith(FOLDER_SNAPSHOT_EXTENSION) ? MAX_FOLDER_ARCHIVE_BYTES : MAX_FOLDER_BYTES;
}
