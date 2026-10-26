"use client";

import { useId } from "react";
import { Loader2, Mic, Square, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { useT } from "../i18n";
import type { ComposerDictation } from "./use-composer-dictation";

export function DictationControls({ dictation, disabled }: { dictation: ComposerDictation; disabled?: boolean }) {
  const { t } = useT("editor");
  const statusId = useId();
  if (!dictation.visible) return null;
  const reason = dictation.error ?? (dictation.availability?.available === false ? dictation.availability.reason : undefined);
  const message = reason ? t(($) => $.dictation.errors[reason]) : undefined;
  const unavailable = dictation.availability?.available === false;
  const capturing = dictation.phase === "recording" || dictation.phase === "stopping";
  const label = dictation.phase === "recording" ? t(($) => $.dictation.stop)
    : dictation.phase === "stopping" ? t(($) => $.dictation.stopping)
    : dictation.phase === "starting" ? t(($) => $.dictation.starting)
    : dictation.phase === "transcribing" ? t(($) => $.dictation.transcribing)
    : !dictation.availability ? t(($) => $.dictation.checking)
    : t(($) => $.dictation.start);
  const waiting = dictation.phase === "starting" || dictation.phase === "stopping" || dictation.phase === "transcribing";

  return (
    <span className="relative inline-flex items-center gap-1">
      <Tooltip>
        <TooltipTrigger render={<span className="inline-flex" role={unavailable ? "group" : undefined}
          tabIndex={unavailable ? 0 : undefined} aria-label={unavailable ? label : undefined}
          aria-describedby={message ? statusId : undefined} />}>
          <Button
            type="button"
            variant={capturing ? "destructive" : "ghost"}
            size="icon-sm"
            aria-label={label}
            aria-pressed={capturing}
            aria-describedby={message ? statusId : undefined}
            aria-busy={waiting || undefined}
            disabled={waiting || !!disabled || !dictation.availability?.available}
            onPointerDown={(event) => event.preventDefault()}
            onMouseDown={(event) => event.preventDefault()}
            onClick={dictation.phase === "recording" ? dictation.stop : dictation.start}
          >
            {capturing ? <Square className="fill-current animate-pulse" aria-hidden="true" />
              : waiting ? <Loader2 className="animate-spin" aria-hidden="true" />
              : <Mic aria-hidden="true" />}
          </Button>
        </TooltipTrigger>
        <TooltipContent>{message ?? label}</TooltipContent>
      </Tooltip>
      {dictation.busy && (
        <Button type="button" variant="ghost" size="icon-sm" aria-label={t(($) => $.dictation.cancel)}
          onMouseDown={(event) => event.preventDefault()} onClick={dictation.cancel}>
          <X aria-hidden="true" />
        </Button>
      )}
      <span id={statusId} role={reason ? "alert" : "status"} className={reason ? "absolute bottom-full right-0 mb-2 w-64 rounded-lg border border-border bg-popover p-2 text-caption text-popover-foreground shadow-sm" : "sr-only"}>
        {message ?? (dictation.phase === "recording" ? t(($) => $.dictation.recording) : dictation.busy ? label : "")}
      </span>
    </span>
  );
}
