import { DictationError, type DictationEngine } from "@multica/core/dictation";
import type { DesktopDictationAPI } from "../../../shared/dictation";

export function createDesktopDictationEngine(api: DesktopDictationAPI): DictationEngine {
  let activeId: string | undefined;
  return {
    check: (locale) => api.check(locale),
    async transcribe({ locale, signal, onRecording }) {
      if (activeId) throw new DictationError("busy");
      if (signal.aborted) throw new DictationError("failed");
      const id = crypto.randomUUID();
      activeId = id;
      const unsubscribe = api.onRecording((recordingId) => {
        if (recordingId === id && !signal.aborted) onRecording();
      });
      const cancel = () => { void api.cancel(id).catch(() => {}); };
      signal.addEventListener("abort", cancel, { once: true });
      try {
        const result = await api.transcribe(id, locale);
        if (signal.aborted) throw new DictationError("failed");
        if (!result.ok) throw new DictationError(result.error);
        return result.text;
      } finally {
        signal.removeEventListener("abort", cancel);
        unsubscribe();
        if (activeId === id) activeId = undefined;
      }
    },
    async stop() {
      if (activeId) await api.stop(activeId);
    },
  };
}

export const desktopDictationEngineFactory = () => createDesktopDictationEngine(window.desktopAPI.dictation);
