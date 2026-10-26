import { app, BrowserWindow, ipcMain, systemPreferences, type IpcMainInvokeEvent } from "electron";
import { join } from "node:path";
import { DictationError } from "@multica/core/dictation";
import type { DictationResult } from "../shared/dictation";
import { NativeDictationEngine } from "./native-dictation";

function isComposerWindow(event: IpcMainInvokeEvent): boolean {
  return event.senderFrame === event.sender.mainFrame &&
    BrowserWindow.fromWebContents(event.sender) !== null;
}

export function setupDictation(platform = process.platform): void {
  const resources = app.isPackaged ? process.resourcesPath : join(__dirname, "../../resources");
  let active: {
    id: string;
    senderId: number;
    abort: AbortController;
    engine: NativeDictationEngine;
  } | undefined;

  const microphoneDenied = () => platform === "win32" &&
    systemPreferences.getMediaAccessStatus("microphone") === "denied";

  ipcMain.handle("dictation:check", (event, locale: unknown) => {
    if (!isComposerWindow(event) || typeof locale !== "string") {
      return { available: false, reason: "unsupported" };
    }
    if (microphoneDenied()) return { available: false, reason: "permission_denied" };
    return new NativeDictationEngine(resources, platform).check(locale);
  });

  ipcMain.handle("dictation:transcribe", async (event, id: unknown, locale: unknown): Promise<DictationResult> => {
    if (!isComposerWindow(event) || typeof id !== "string" || !/^[a-f0-9-]{36}$/.test(id) || typeof locale !== "string") {
      return { ok: false, error: "failed" };
    }
    if (active) return { ok: false, error: "busy" };
    if (microphoneDenied()) return { ok: false, error: "permission_denied" };
    const session = {
      id, senderId: event.sender.id, abort: new AbortController(), engine: new NativeDictationEngine(resources, platform),
    };
    active = session;
    const cancel = () => session.abort.abort();
    const navigating = (_event: Electron.Event, _url: string, _inPlace: boolean, isMainFrame: boolean) => {
      if (isMainFrame) cancel();
    };
    event.sender.once("destroyed", cancel);
    event.sender.once("render-process-gone", cancel);
    event.sender.on("did-start-navigation", navigating);
    try {
      const text = await session.engine.transcribe({
        locale, signal: session.abort.signal,
        onRecording: () => {
          if (!event.sender.isDestroyed()) event.sender.send("dictation:recording", id);
        },
      });
      return { ok: true, text };
    } catch (error) {
      return { ok: false, error: error instanceof DictationError ? error.code : "failed" };
    } finally {
      event.sender.removeListener("destroyed", cancel);
      event.sender.removeListener("render-process-gone", cancel);
      event.sender.removeListener("did-start-navigation", navigating);
      if (active === session) active = undefined;
    }
  });

  ipcMain.handle("dictation:stop", async (event, id: unknown) => {
    if (isComposerWindow(event) && active && active.id === id && active.senderId === event.sender.id) {
      await active.engine.stop();
    }
  });
  ipcMain.handle("dictation:cancel", (event, id: unknown) => {
    if (isComposerWindow(event) && active && active.id === id && active.senderId === event.sender.id) {
      active.abort.abort();
    }
  });
  app.on("before-quit", () => active?.abort.abort());
}
