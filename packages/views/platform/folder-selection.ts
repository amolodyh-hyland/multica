export async function filterFolderSymlinks(files: File[]): Promise<{ files: File[]; skipped: string[]; verified: boolean }> {
  const desktop = (window as unknown as {
    desktopAPI?: { folderSelectionSymlinks?: (files: File[]) => Promise<number[]> };
  }).desktopAPI;
  if (!desktop?.folderSelectionSymlinks) return { files, skipped: [], verified: false };
  const indexes = new Set(await desktop.folderSelectionSymlinks(files));
  return {
    files: files.filter((_, index) => !indexes.has(index)),
    skipped: files.filter((_, index) => indexes.has(index)).map((file) => file.webkitRelativePath),
    verified: true,
  };
}
