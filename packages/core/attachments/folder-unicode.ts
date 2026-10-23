import data from "./unicode15-data.json";

function contains(ranges: number[][], code: number): boolean {
  let low = 0;
  let high = ranges.length;
  while (low < high) {
    const mid = (low + high) >>> 1;
    const range = ranges[mid]!;
    if (code < range[0]!) high = mid;
    else if (code > range[1]!) low = mid + 1;
    else return true;
  }
  return false;
}

// Unicode normalization is stable for the pinned assigned repertoire. Count
// decomposed marks one scalar at a time, before whole-path normalization, so
// Go's stream-safe normalization cannot insert a CGJ into an accepted name.
export function supportedFolderUnicode(path: string): boolean {
  let marks = 0;
  for (const character of path) {
    if (!contains(data.assigned, character.codePointAt(0)!)) return false;
    for (const decomposed of character.normalize("NFD")) {
      marks = contains(data.marks, decomposed.codePointAt(0)!) ? marks + 1 : 0;
      if (marks > 8) return false;
    }
  }
  return true;
}

// Called only after supportedFolderUnicode. Never use runtime case tables.
export function folderPathKey(path: string): string {
  const folds: Record<string, string> = data.folds;
  return [...path.normalize("NFC")].map((character) => folds[character.codePointAt(0)!] ?? character).join("").normalize("NFC");
}
