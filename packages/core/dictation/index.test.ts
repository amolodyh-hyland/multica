// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { DictationController, DictationError, type DictationEngine } from "./index";

function fixture() {
  let resolve!: (text: string) => void;
  let reject!: (error: unknown) => void;
  let options!: Parameters<DictationEngine["transcribe"]>[0];
  const engine: DictationEngine = {
    check: vi.fn(async () => ({ available: true as const })),
    transcribe: vi.fn((input) => {
      options = input;
      return new Promise<string>((yes, no) => { resolve = yes; reject = no; });
    }),
    stop: vi.fn(async () => {}),
  };
  const change = vi.fn();
  const insert = vi.fn();
  const controller = new DictationController(engine, change);
  return { controller, engine, change, insert, options: () => options,
    resolve: (text: string) => resolve(text), reject: (error: unknown) => reject(error) };
}

describe("dictation with a fake engine", () => {
  it("records, stops, then returns an editable transcript exactly once", async () => {
    const f = fixture();
    const pending = f.controller.start("fr", f.insert);
    expect(f.change).toHaveBeenLastCalledWith("starting", undefined);
    expect(f.options().locale).toBe("fr");
    f.options().onRecording();
    expect(f.controller.isActive()).toBe(true);
    expect(f.change).toHaveBeenLastCalledWith("recording", undefined);
    await f.controller.stop();
    expect(f.engine.stop).toHaveBeenCalledOnce();
    expect(f.change).toHaveBeenLastCalledWith("transcribing", undefined);
    expect(f.insert).not.toHaveBeenCalled();
    f.resolve("editable text");
    await pending;
    expect(f.insert).toHaveBeenCalledExactlyOnceWith("editable text");
    expect(f.controller.isActive()).toBe(false);
  });

  it("guards repeated record and stop clicks synchronously", async () => {
    const f = fixture();
    const pending = f.controller.start("en", f.insert);
    await f.controller.start("en", f.insert);
    expect(f.engine.transcribe).toHaveBeenCalledOnce();
    await f.controller.stop();
    expect(f.engine.stop).not.toHaveBeenCalled();
    f.options().onRecording();
    await f.controller.stop();
    await f.controller.stop();
    expect(f.engine.stop).toHaveBeenCalledOnce();
    f.resolve("text");
    await pending;
  });

  it("keeps the recording state until native capture shutdown is acknowledged", async () => {
    const f = fixture();
    let acknowledge!: () => void;
    vi.mocked(f.engine.stop).mockImplementation(() => new Promise<void>((resolve) => { acknowledge = resolve; }));
    const result = f.controller.start("en", f.insert);
    f.options().onRecording();
    const stop = f.controller.stop();
    expect(f.change).toHaveBeenLastCalledWith("stopping", undefined);
    await f.controller.stop();
    expect(f.engine.stop).toHaveBeenCalledOnce();
    expect(f.change).not.toHaveBeenCalledWith("transcribing", undefined);
    acknowledge();
    await stop;
    expect(f.change).toHaveBeenLastCalledWith("transcribing", undefined);
    f.resolve("captured before stop");
    await result;
  });

  it("does not restore processing if completion races the shutdown acknowledgement", async () => {
    const f = fixture();
    let acknowledge!: () => void;
    vi.mocked(f.engine.stop).mockImplementation(() => new Promise<void>((resolve) => { acknowledge = resolve; }));
    const result = f.controller.start("en", f.insert);
    f.options().onRecording();
    const stop = f.controller.stop();
    f.resolve("finished");
    await result;
    acknowledge();
    await stop;
    expect(f.change).toHaveBeenLastCalledWith("idle", undefined);
  });

  it("ignores a late shutdown acknowledgement after cancel", async () => {
    const f = fixture();
    let acknowledge!: () => void;
    vi.mocked(f.engine.stop).mockImplementation(() => new Promise<void>((resolve) => { acknowledge = resolve; }));
    const result = f.controller.start("en", f.insert);
    f.options().onRecording();
    const stop = f.controller.stop();
    f.controller.cancel();
    acknowledge();
    await stop;
    f.resolve("discarded");
    await result;
    expect(f.change).toHaveBeenLastCalledWith("idle", undefined);
    expect(f.insert).not.toHaveBeenCalled();
  });

  it.each(["starting", "recording", "transcribing"])("cancel during %s aborts and ignores late results", async (phase) => {
    const f = fixture();
    const pending = f.controller.start("en", f.insert);
    if (phase !== "starting") f.options().onRecording();
    if (phase === "transcribing") await f.controller.stop();
    f.controller.cancel();
    expect(f.options().signal.aborted).toBe(true);
    f.options().onRecording();
    f.resolve("discard me");
    await pending;
    expect(f.insert).not.toHaveBeenCalled();
    expect(f.controller.isActive()).toBe(false);
    expect(f.change).toHaveBeenLastCalledWith("idle", undefined);
  });

  it.each(["permission_denied", "unavailable", "busy", "timeout", "failed"] as const)("surfaces %s without insertion", async (code) => {
    const f = fixture();
    const pending = f.controller.start("en", f.insert);
    f.reject(new DictationError(code));
    await pending;
    expect(f.change).toHaveBeenLastCalledWith("idle", code);
    expect(f.insert).not.toHaveBeenCalled();
  });

  it("reports silence and permits another recording", async () => {
    const f = fixture();
    const pending = f.controller.start("en", f.insert);
    f.resolve("  ");
    await pending;
    expect(f.change).toHaveBeenLastCalledWith("idle", "no_speech");
    expect(f.insert).not.toHaveBeenCalled();
    const retry = f.controller.start("en", f.insert);
    f.resolve("retry");
    await retry;
    expect(f.insert).toHaveBeenCalledExactlyOnceWith("retry");
  });

  it("aborts after stop fails and discards any eventual transcript", async () => {
    const f = fixture();
    vi.mocked(f.engine.stop).mockRejectedValueOnce(new Error("stop failed"));
    const pending = f.controller.start("en", f.insert);
    f.options().onRecording();
    await f.controller.stop();
    expect(f.options().signal.aborted).toBe(true);
    expect(f.change).toHaveBeenLastCalledWith("idle", "failed");
    f.resolve("late");
    await pending;
    expect(f.insert).not.toHaveBeenCalled();
  });
});
