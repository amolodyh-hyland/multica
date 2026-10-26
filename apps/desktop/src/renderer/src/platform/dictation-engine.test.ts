import { describe, expect, it, vi } from "vitest";
import { DictationError } from "@multica/core/dictation";
import type { DesktopDictationAPI, DictationResult } from "../../../shared/dictation";
import { createDesktopDictationEngine } from "./dictation-engine";

function fixture() {
  let recording!: (id: string) => void;
  let complete!: (result: DictationResult) => void;
  const unsubscribe = vi.fn();
  const api: DesktopDictationAPI = {
    check: vi.fn(async () => ({ available: true as const })),
    transcribe: vi.fn(() => new Promise<DictationResult>((resolve) => { complete = resolve; })),
    stop: vi.fn(async () => {}), cancel: vi.fn(async () => {}),
    onRecording: vi.fn((callback) => { recording = callback; return unsubscribe; }),
  };
  return { api, engine: createDesktopDictationEngine(api), unsubscribe,
    recording: (id: string) => recording(id), complete: (result: DictationResult) => complete(result) };
}

describe("desktop dictation engine bridge", () => {
  it("only handles recording events for its session, stops and returns text", async () => {
    const f = fixture();
    const onRecording = vi.fn();
    const promise = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording });
    const id = vi.mocked(f.api.transcribe).mock.calls[0]![0];
    f.recording("another-session");
    expect(onRecording).not.toHaveBeenCalled();
    f.recording(id);
    expect(onRecording).toHaveBeenCalledOnce();
    await f.engine.stop();
    expect(f.api.stop).toHaveBeenCalledExactlyOnceWith(id);
    f.complete({ ok: true, text: "draft text" });
    await expect(promise).resolves.toBe("draft text");
    expect(f.unsubscribe).toHaveBeenCalledOnce();
  });

  it("cancels the owning session and discards text after abort", async () => {
    const f = fixture();
    const abort = new AbortController();
    const onRecording = vi.fn();
    const promise = f.engine.transcribe({ locale: "en", signal: abort.signal, onRecording });
    const id = vi.mocked(f.api.transcribe).mock.calls[0]![0];
    abort.abort();
    expect(f.api.cancel).toHaveBeenCalledExactlyOnceWith(id);
    f.recording(id);
    expect(onRecording).not.toHaveBeenCalled();
    f.complete({ ok: true, text: "discard" });
    await expect(promise).rejects.toBeInstanceOf(DictationError);
    expect(f.unsubscribe).toHaveBeenCalledOnce();
  });

  it("propagates permission denial without retaining event listeners", async () => {
    const f = fixture();
    const promise = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    f.complete({ ok: false, error: "permission_denied" });
    await expect(promise).rejects.toMatchObject({ code: "permission_denied" });
    expect(f.unsubscribe).toHaveBeenCalledOnce();
  });
});
