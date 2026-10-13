import type { LyricLine } from "../api";

// index of the line active at `seek` (last start <= seek), -1 before all.
export function lyricIndex(lines: LyricLine[], seek: number): number {
  let idx = -1;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].start <= seek) idx = i;
    else break;
  }
  return idx;
}
