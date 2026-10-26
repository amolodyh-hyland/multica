// @vitest-environment node
import { EventEmitter } from "node:events";
import { PassThrough } from "node:stream";
import { join } from "node:path";
import { spawn } from "node:child_process";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NativeDictationEngine, nativeDictationCommand } from "./native-dictation";

const exists = vi.hoisted(() => vi.fn(() => true));
vi.mock("node:fs", () => ({ existsSync: exists }));

function fixture() {
  const child = Object.assign(new EventEmitter(), {
    stdin: new PassThrough(), stdout: new PassThrough(), stderr: new PassThrough(), kill: vi.fn(),
  });
  const spawnProcess = vi.fn((_file: string, _args: string[], _options: unknown) => child);
  const engine = new NativeDictationEngine("/resources", "darwin", spawnProcess as unknown as typeof spawn);
  const message = (value: unknown) => child.stdout.write(JSON.stringify(value) + "\n");
  return { child, engine, spawnProcess, message };
}

afterEach(() => { vi.useRealTimers(); exists.mockReturnValue(true); });

describe("native on-device dictation process", () => {
  it("probes availability without starting capture", async () => {
    const f = fixture();
    const pending = f.engine.check("en");
    expect(f.spawnProcess.mock.calls[0]?.[1]).toEqual(["en-US", "--check"]);
    f.message({ type: "available" });
    await expect(pending).resolves.toEqual({ available: true });
    expect(f.child.kill).toHaveBeenCalledOnce();
  });

  it("streams only protocol text, sends stop and terminates after the final result", async () => {
    const f = fixture();
    const recording = vi.fn();
    const input: string[] = [];
    f.child.stdin.on("data", (value) => input.push(value.toString()));
    const pending = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: recording });
    f.message({ type: "recording" });
    expect(recording).toHaveBeenCalledOnce();
    const stopped = f.engine.stop();
    expect(input).toEqual(["stop\n"]);
    f.message({ type: "capture_stopped" });
    await stopped;
    f.message({ type: "result", text: "editable transcript" });
    await expect(pending).resolves.toBe("editable transcript");
    expect(f.child.kill).toHaveBeenCalledOnce();
  });

  it("keeps Stop pending until the helper acknowledges capture shutdown", async () => {
    const f = fixture();
    const pending = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    f.message({ type: "recording" });
    const acknowledged = vi.fn();
    const stopped = f.engine.stop().then(acknowledged);
    await Promise.resolve();
    expect(acknowledged).not.toHaveBeenCalled();
    f.message({ type: "capture_stopped" });
    await stopped;
    expect(acknowledged).toHaveBeenCalledOnce();
    f.message({ type: "result", text: "editable transcript" });
    await expect(pending).resolves.toBe("editable transcript");
  });

  it("rejects a transcript received before capture shutdown acknowledgement", async () => {
    const f = fixture();
    const pending = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    f.message({ type: "recording" });
    const stopped = f.engine.stop();
    const stopRejection = expect(stopped).rejects.toMatchObject({ code: "failed" });
    const resultRejection = expect(pending).rejects.toMatchObject({ code: "failed" });
    f.message({ type: "result", text: "untrusted boundary" });
    await stopRejection;
    await resultRejection;
    expect(f.child.kill).toHaveBeenCalledOnce();
  });

  it("accepts automatic capture shutdown before a late Stop request", async () => {
    const f = fixture();
    const input = vi.fn();
    f.child.stdin.on("data", input);
    const result = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    f.message({ type: "recording" });
    f.message({ type: "capture_stopped" });
    await f.engine.stop();
    expect(input).not.toHaveBeenCalled();
    f.message({ type: "result", text: "finished" });
    await expect(result).resolves.toBe("finished");
  });

  it("kills the native process immediately on cancel", async () => {
    const f = fixture();
    const abort = new AbortController();
    const pending = f.engine.transcribe({ locale: "en", signal: abort.signal, onRecording: vi.fn() });
    abort.abort();
    await expect(pending).rejects.toMatchObject({ code: "failed" });
    expect(f.child.kill).toHaveBeenCalledOnce();
  });

  it("enforces a recording timeout and a shorter stop timeout", async () => {
    vi.useFakeTimers();
    const f = fixture();
    const pending = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    const rejection = expect(pending).rejects.toMatchObject({ code: "timeout" });
    vi.advanceTimersByTime(120_000);
    await rejection;
    expect(f.child.kill).toHaveBeenCalledOnce();

    const g = fixture();
    const finalizing = g.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    const finalRejection = expect(finalizing).rejects.toMatchObject({ code: "timeout" });
    g.message({ type: "recording" });
    const stopped = g.engine.stop();
    const stopRejection = expect(stopped).rejects.toMatchObject({ code: "timeout" });
    vi.advanceTimersByTime(15_000);
    await stopRejection;
    await finalRejection;
    expect(g.child.kill).toHaveBeenCalledOnce();
  });

  it("returns typed permission errors and releases the process", async () => {
    const f = fixture();
    const pending = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    f.message({ type: "error", code: "permission_denied" });
    await expect(pending).rejects.toMatchObject({ code: "permission_denied" });
    expect(f.child.kill).toHaveBeenCalledOnce();
  });

  it.each([["invalid JSON", "not json\n"], ["invalid transcript", JSON.stringify({ type: "result", text: 42 }) + "\n"], ["oversized output", "x".repeat(256 * 1024 + 1)]])("rejects %s", async (_name, output) => {
    const f = fixture();
    const pending = f.engine.transcribe({ locale: "en", signal: new AbortController().signal, onRecording: vi.fn() });
    f.child.stdout.write(output);
    await expect(pending).rejects.toMatchObject({ code: "failed" });
    expect(f.child.kill).toHaveBeenCalledOnce();
  });

  it("does not launch when the helper is missing or the platform is unsupported", async () => {
    const f = fixture();
    exists.mockReturnValue(false);
    await expect(f.engine.check("en")).resolves.toEqual({ available: false, reason: "unavailable" });
    expect(f.spawnProcess).not.toHaveBeenCalled();
    expect(nativeDictationCommand("/resources", "linux", "en", false)).toBeUndefined();
  });

  it("passes a fixed Windows script and separate locale arguments without shell interpolation", () => {
    const command = nativeDictationCommand("/resources", "win32", "zh-Hans", true)!;
    expect(command.file).toMatch(/WindowsPowerShell/);
    expect(command.args).toEqual([
      "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File",
      join("/resources", "dictation", "windows-speech.ps1"), "-Locale", "zh-CN", "-Check",
    ]);
  });
});
