// @vitest-environment node
import { EventEmitter } from "node:events";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DictationError, type DictationEngine } from "@multica/core/dictation";

const fake = vi.hoisted(() => ({
  handlers: new Map<string, (...args: unknown[]) => unknown>(),
  transcribe: vi.fn(), stop: vi.fn(async () => {}), check: vi.fn(async () => ({ available: true })),
  appOn: vi.fn(), microphoneStatus: vi.fn(() => "granted"),
}));
vi.mock("electron", () => ({
  app: { isPackaged: false, on: fake.appOn },
  systemPreferences: { getMediaAccessStatus: fake.microphoneStatus },
  BrowserWindow: { fromWebContents: () => ({}) },
  ipcMain: { handle: (channel: string, handler: (...args: unknown[]) => unknown) => fake.handlers.set(channel, handler) },
}));
vi.mock("./native-dictation", () => ({
  NativeDictationEngine: class {
    check = fake.check;
    stop = fake.stop;
    transcribe = fake.transcribe;
  },
}));
import { setupDictation } from "./dictation";

const ID = "12345678-1234-1234-1234-123456789abc";
function windowEvent(id: number, subframe = false) {
  const frame = {};
  const sender = Object.assign(new EventEmitter(), { id, mainFrame: frame, send: vi.fn(), isDestroyed: () => false });
  return { sender, senderFrame: subframe ? {} : frame };
}
function invoke(channel: string, ...args: unknown[]) { return fake.handlers.get(`dictation:${channel}`)!(...args); }

beforeEach(() => {
  vi.clearAllMocks();
  fake.handlers.clear();
  fake.microphoneStatus.mockReturnValue("granted");
  setupDictation();
});

describe("dictation IPC ownership", () => {
  it("refuses requests from iframes and malformed requests", async () => {
    await expect(invoke("transcribe", windowEvent(1, true), ID, "en")).resolves.toMatchObject({ ok: false });
    await expect(invoke("transcribe", windowEvent(1), "invalid", "en")).resolves.toMatchObject({ ok: false });
    expect(fake.transcribe).not.toHaveBeenCalled();
  });

  it("honors Windows microphone privacy denial before launching an engine", async () => {
    fake.microphoneStatus.mockReturnValue("denied");
    setupDictation("win32");
    expect(invoke("check", windowEvent(1), "en")).toEqual({ available: false, reason: "permission_denied" });
    await expect(invoke("transcribe", windowEvent(1), ID, "en")).resolves.toEqual({ ok: false, error: "permission_denied" });
    expect(fake.transcribe).not.toHaveBeenCalled();
  });

  it("only permits the owning window/session to stop or cancel, and rejects concurrent recordings", async () => {
    let finish!: (text: string) => void;
    let options!: Parameters<DictationEngine["transcribe"]>[0];
    fake.transcribe.mockImplementation((input) => {
      options = input;
      return new Promise<string>((resolve) => { finish = resolve; });
    });
    const owner = windowEvent(1);
    const other = windowEvent(2);
    const pending = invoke("transcribe", owner, ID, "en");
    options.onRecording();
    expect(owner.sender.send).toHaveBeenCalledWith("dictation:recording", ID);
    await expect(invoke("transcribe", other, ID, "en")).resolves.toEqual({ ok: false, error: "busy" });
    await invoke("stop", other, ID);
    await invoke("cancel", other, ID);
    await invoke("cancel", owner, "different id");
    expect(fake.stop).not.toHaveBeenCalled();
    expect(options.signal.aborted).toBe(false);
    await invoke("stop", owner, ID);
    expect(fake.stop).toHaveBeenCalledOnce();
    await invoke("cancel", owner, ID);
    expect(options.signal.aborted).toBe(true);
    finish("text");
    await pending;
    expect(owner.sender.listenerCount("destroyed")).toBe(0);
    expect(owner.sender.listenerCount("did-start-navigation")).toBe(0);
  });

  it.each(["destroyed", "render-process-gone", "navigation"])("aborts on %s", async (event) => {
    let options!: Parameters<DictationEngine["transcribe"]>[0];
    fake.transcribe.mockImplementation((input) => {
      options = input;
      return new Promise<string>((_resolve, reject) => input.signal.addEventListener("abort", () => reject(new DictationError("failed"))));
    });
    const owner = windowEvent(1);
    const pending = invoke("transcribe", owner, ID, "en");
    if (event === "navigation") owner.sender.emit("did-start-navigation", {}, "file:///new", false, true);
    else owner.sender.emit(event);
    expect(options.signal.aborted).toBe(true);
    await expect(pending).resolves.toEqual({ ok: false, error: "failed" });
  });
});
