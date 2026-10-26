import { ipcRenderer } from "electron";
import type { DesktopDictationAPI } from "../shared/dictation";

export const dictationAPI: DesktopDictationAPI = {
  check: (locale) => ipcRenderer.invoke("dictation:check", locale),
  transcribe: (id, locale) => ipcRenderer.invoke("dictation:transcribe", id, locale),
  stop: (id) => ipcRenderer.invoke("dictation:stop", id),
  cancel: (id) => ipcRenderer.invoke("dictation:cancel", id),
  onRecording: (callback) => {
    const listener = (_event: Electron.IpcRendererEvent, id: string) => callback(id);
    ipcRenderer.on("dictation:recording", listener);
    return () => ipcRenderer.removeListener("dictation:recording", listener);
  },
};
