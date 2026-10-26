import type { DictationAvailability, DictationErrorCode } from "@multica/core/dictation";

export type DictationResult =
  | { ok: true; text: string }
  | { ok: false; error: DictationErrorCode };

export interface DesktopDictationAPI {
  check(locale: string): Promise<DictationAvailability>;
  transcribe(id: string, locale: string): Promise<DictationResult>;
  stop(id: string): Promise<void>;
  cancel(id: string): Promise<void>;
  onRecording(callback: (id: string) => void): () => void;
}

export function isDictationErrorCode(value: unknown): value is DictationErrorCode {
  return typeof value === "string" && [
    "unsupported", "unavailable", "permission_denied", "busy", "no_speech", "timeout", "failed",
  ].includes(value);
}
