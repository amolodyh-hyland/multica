"use client";

import { createContext, useContext } from "react";
import type { DictationEngine } from "@multica/core/dictation";

const DictationContext = createContext<(() => DictationEngine) | null>(null);

export const DictationProvider = DictationContext.Provider;
export const useDictationEngineFactory = () => useContext(DictationContext);
