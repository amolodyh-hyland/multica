export type DictationErrorCode =
  | "unsupported"
  | "unavailable"
  | "permission_denied"
  | "busy"
  | "no_speech"
  | "timeout"
  | "failed";

export class DictationError extends Error {
  constructor(public readonly code: DictationErrorCode) {
    super(code);
  }
}

export type DictationAvailability =
  | { available: true }
  | { available: false; reason: DictationErrorCode };

export interface DictationEngine {
  check(locale: string): Promise<DictationAvailability>;
  transcribe(options: {
    locale: string;
    signal: AbortSignal;
    onRecording: () => void;
  }): Promise<string>;
  /** Resolves only after the engine has released live audio input. */
  stop(): Promise<void>;
}

export type DictationPhase = "idle" | "starting" | "recording" | "stopping" | "transcribing";

export class DictationController {
  private generation = 0;
  private abort: AbortController | undefined;
  private phase: DictationPhase = "idle";

  constructor(
    private readonly engine: DictationEngine,
    private readonly onChange: (phase: DictationPhase, error?: DictationErrorCode) => void,
  ) {}

  isActive(): boolean {
    return this.phase !== "idle";
  }

  private update(phase: DictationPhase, error?: DictationErrorCode): void {
    this.phase = phase;
    this.onChange(phase, error);
  }

  async start(locale: string, insert: (text: string) => void): Promise<void> {
    if (this.isActive()) return;
    const generation = ++this.generation;
    const abort = new AbortController();
    this.abort = abort;
    this.update("starting");
    try {
      const text = await this.engine.transcribe({
        locale,
        signal: abort.signal,
        onRecording: () => {
          if (generation === this.generation && this.phase === "starting") {
            this.update("recording");
          }
        },
      });
      if (generation !== this.generation) return;
      if (!text.trim()) throw new DictationError("no_speech");
      insert(text);
      this.update("idle");
    } catch (error) {
      if (generation !== this.generation) return;
      this.update("idle", error instanceof DictationError ? error.code : "failed");
    } finally {
      if (generation === this.generation) this.abort = undefined;
    }
  }

  async stop(): Promise<void> {
    if (this.phase !== "recording") return;
    const generation = this.generation;
    this.update("stopping");
    try {
      await this.engine.stop();
      if (generation === this.generation && this.isActive()) this.update("transcribing");
    } catch (error) {
      if (generation !== this.generation) return;
      this.cancel();
      this.update("idle", error instanceof DictationError ? error.code : "failed");
    }
  }

  cancel(): void {
    ++this.generation;
    this.abort?.abort();
    this.abort = undefined;
    this.update("idle");
  }
}
