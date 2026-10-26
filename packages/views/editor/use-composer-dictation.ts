"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import {
  DictationController,
  DictationError,
  type DictationAvailability,
  type DictationErrorCode,
  type DictationPhase,
} from "@multica/core/dictation";
import { useLocale } from "../i18n";
import { useDictationEngineFactory } from "../platform/dictation-context";
import type { ContentEditorRef } from "./content-editor";

export function useComposerDictation({
  editorRef, resetKey, disabled = false, activate, ready = true,
}: {
  editorRef: RefObject<ContentEditorRef | null>;
  resetKey: string;
  disabled?: boolean;
  activate?: () => void;
  ready?: boolean;
}) {
  const factory = useDictationEngineFactory();
  const locale = useLocale();
  const [availability, setAvailability] = useState<DictationAvailability>();
  const [state, setState] = useState<{ phase: DictationPhase; error?: DictationErrorCode }>({ phase: "idle" });
  const [waitingForEditor, setWaitingForEditor] = useState(false);
  const pendingStart = useRef<string | null>(null);
  const binding = `${resetKey}:${locale}`;
  const current = useRef({ binding, disabled });
  current.current = { binding, disabled };
  const engine = useMemo(() => factory?.(), [factory]);
  const controller = useMemo(() => engine && new DictationController(engine, (phase, error) => {
    setState({ phase, error });
  }), [engine]);

  useEffect(() => {
    let disposed = false;
    setAvailability(undefined);
    if (!engine || disabled) return;
    const check = () => {
      if (controller?.isActive()) return;
      void engine?.check(locale).then((result) => {
        if (!disposed) setAvailability(result);
      }).catch(() => {
        if (!disposed) setAvailability({ available: false, reason: "unavailable" });
      });
    };
    check();
    window.addEventListener("focus", check);
    return () => { disposed = true; window.removeEventListener("focus", check); };
  }, [engine, controller, locale, disabled]);

  const cancel = useCallback(() => {
    pendingStart.current = null;
    setWaitingForEditor(false);
    controller?.cancel();
  }, [controller]);

  const begin = useCallback(() => {
    void controller?.start(locale, (text) => {
      if (current.current.binding === binding && !current.current.disabled) {
        if (!editorRef.current?.insertTextAtCursor(text)) throw new DictationError("failed");
      }
    });
  }, [controller, locale, binding, editorRef]);

  useLayoutEffect(() => {
    if (!controller) return;
    if (disabled) cancel();
    return cancel;
  }, [cancel, controller, binding, disabled]);

  useEffect(() => {
    if (ready && waitingForEditor && pendingStart.current === binding && !disabled) {
      pendingStart.current = null;
      setWaitingForEditor(false);
      begin();
    }
  }, [ready, waitingForEditor, binding, disabled, begin]);

  useEffect(() => {
    if (!controller) return;
    const onVisibility = () => { if (document.visibilityState === "hidden") cancel(); };
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener("pagehide", cancel);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      window.removeEventListener("pagehide", cancel);
    };
  }, [cancel, controller]);

  return {
    visible: !!factory,
    availability,
    ...state,
    phase: waitingForEditor ? "starting" as const : state.phase,
    busy: waitingForEditor || state.phase !== "idle",
    isActive: () => pendingStart.current !== null || (controller?.isActive() ?? false),
    start: () => {
      if (disabled || !availability?.available || pendingStart.current || controller?.isActive()) return;
      activate?.();
      if (!ready) {
        pendingStart.current = binding;
        setWaitingForEditor(true);
      } else begin();
    },
    stop: () => { void controller?.stop(); },
    cancel,
  };
}

export type ComposerDictation = ReturnType<typeof useComposerDictation>;
