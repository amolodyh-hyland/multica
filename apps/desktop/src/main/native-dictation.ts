import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { existsSync } from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline";
import {
  DictationError,
  type DictationAvailability,
  type DictationEngine,
} from "@multica/core/dictation";
import { isDictationErrorCode } from "../shared/dictation";

const LOCALES: Record<string, string> = {
  en: "en-US", fr: "fr-FR", ja: "ja-JP", ko: "ko-KR", "zh-Hans": "zh-CN",
};
const MAX_OUTPUT_BYTES = 256 * 1024;
const RECORDING_LIMIT_MS = 120_000;
const FINALIZE_LIMIT_MS = 15_000;

export function nativeDictationCommand(
  resources: string,
  platform: NodeJS.Platform,
  locale: string,
  check: boolean,
): { file: string; args: string[] } | undefined {
  if (platform === "darwin") {
    return {
      file: join(resources, "dictation", "multica-dictation"),
      args: [LOCALES[locale] ?? locale, ...(check ? ["--check"] : [])],
    };
  }
  if (platform === "win32") {
    return {
      file: join(process.env.SystemRoot ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
      args: ["-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File",
        join(resources, "dictation", "windows-speech.ps1"),
        "-Locale", LOCALES[locale] ?? locale, ...(check ? ["-Check"] : [])],
    };
  }
  return undefined;
}

export class NativeDictationEngine implements DictationEngine {
  private child: ChildProcessWithoutNullStreams | undefined;
  private stopping: (() => Promise<void>) | undefined;

  constructor(
    private readonly resources: string,
    private readonly platform = process.platform,
    private readonly spawnProcess: typeof spawn = spawn,
  ) {}

  async check(locale: string): Promise<DictationAvailability> {
    try {
      await this.run(locale, true);
      return { available: true };
    } catch (error) {
      return { available: false, reason: error instanceof DictationError ? error.code : "unavailable" };
    }
  }

  transcribe(options: Parameters<DictationEngine["transcribe"]>[0]): Promise<string> {
    return this.run(options.locale, false, options.signal, options.onRecording);
  }

  async stop(): Promise<void> {
    await this.stopping?.();
  }

  private run(locale: string, check: boolean, signal?: AbortSignal, onRecording?: () => void): Promise<string> {
    if (this.child) return Promise.reject(new DictationError("busy"));
    if (!/^[a-zA-Z]{2,8}(?:-[a-zA-Z0-9]{2,8}){0,3}$/.test(locale)) {
      return Promise.reject(new DictationError("unavailable"));
    }
    const command = nativeDictationCommand(this.resources, this.platform, locale, check);
    if (!command) return Promise.reject(new DictationError("unsupported"));
    const helper = this.platform === "win32"
      ? join(this.resources, "dictation", "windows-speech.ps1") : command.file;
    if (!existsSync(helper)) return Promise.reject(new DictationError("unavailable"));
    if (signal?.aborted) return Promise.reject(new DictationError("failed"));

    return new Promise((resolve, reject) => {
      let settled = false;
      let bytes = 0;
      let recording = false;
      let started = false;
      let captureStopped = false;
      let stopPromise: Promise<void> | undefined;
      let resolveStop: (() => void) | undefined;
      let rejectStop: ((error: DictationError) => void) | undefined;
      const child = this.spawnProcess(command.file, command.args, {
        stdio: ["pipe", "pipe", "pipe"], windowsHide: true, shell: false,
      }) as ChildProcessWithoutNullStreams;
      this.child = child;
      const lines = createInterface({ input: child.stdout });
      let timer: ReturnType<typeof setTimeout>;
      const finish = (error?: DictationError, text = "") => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        signal?.removeEventListener("abort", abort);
        lines.close();
        child.kill();
        this.child = undefined;
        this.stopping = undefined;
        if (error) { rejectStop?.(error); reject(error); }
        else resolve(text);
      };
      const abort = () => finish(new DictationError("failed"));
      const timeout = () => finish(new DictationError("timeout"));
      timer = setTimeout(timeout, check ? 10_000 : RECORDING_LIMIT_MS);
      this.stopping = () => {
        if (stopPromise) return stopPromise;
        if (!recording || settled) return Promise.resolve();
        stopPromise = new Promise<void>((yes, no) => { resolveStop = yes; rejectStop = no; });
        clearTimeout(timer);
        timer = setTimeout(timeout, FINALIZE_LIMIT_MS);
        child.stdin.write("stop\n");
        return stopPromise;
      };
      child.stdin.on("error", () => finish(new DictationError("failed")));
      child.stderr.resume();
      child.stdout.on("data", (data: Buffer) => {
        bytes += data.length;
        if (bytes > MAX_OUTPUT_BYTES) finish(new DictationError("failed"));
      });
      lines.on("line", (line) => {
        if (settled) return;
        try {
          if (Buffer.byteLength(line, "utf8") > MAX_OUTPUT_BYTES) throw new Error();
          const message: unknown = JSON.parse(line);
          if (!message || typeof message !== "object" || !("type" in message)) throw new Error();
          if (message.type === "available" && check) finish();
          else if (message.type === "recording" && !check && !started) {
            recording = true;
            started = true;
            onRecording?.();
          } else if (message.type === "capture_stopped" && recording && !captureStopped) {
            captureStopped = true;
            recording = false;
            resolveStop?.();
          } else if (message.type === "result" && !check && "text" in message && typeof message.text === "string") {
            if (!started || !captureStopped) throw new Error();
            finish(undefined, message.text);
          } else if (message.type === "error" && "code" in message && isDictationErrorCode(message.code)) {
            finish(new DictationError(message.code));
          } else throw new Error();
        } catch {
          finish(new DictationError("failed"));
        }
      });
      child.on("error", () => finish(new DictationError("unavailable")));
      child.on("close", () => finish(new DictationError("failed")));
      signal?.addEventListener("abort", abort, { once: true });
      if (signal?.aborted) abort();
    });
  }
}
