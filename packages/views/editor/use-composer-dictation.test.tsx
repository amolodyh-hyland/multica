import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { DictationEngine } from "@multica/core/dictation";
import { TooltipProvider } from "@multica/ui/components/ui/tooltip";
import { DictationProvider } from "../platform/dictation-context";
import { useComposerDictation } from "./use-composer-dictation";
import { DictationControls } from "./dictation-controls";
import type { ContentEditorRef } from "./content-editor";
import copy from "../locales/en/editor.json";

vi.mock("../i18n", () => ({
  useLocale: () => "en",
  useT: () => ({ t: (selector: (resources: typeof copy) => string) => selector(copy) }),
}));

function fixture() {
  let complete!: (text: string) => void;
  let options!: Parameters<DictationEngine["transcribe"]>[0];
  const engine: DictationEngine = {
    check: vi.fn(async () => ({ available: true as const })),
    transcribe: vi.fn((input) => {
      options = input;
      return new Promise<string>((resolve) => { complete = resolve; });
    }),
    stop: vi.fn(async () => {}),
  };
  const factory = () => engine;
  const insertTextAtCursor = vi.fn(() => true);
  const editorRef = { current: { insertTextAtCursor } as unknown as ContentEditorRef };
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TooltipProvider><DictationProvider value={factory}>{children}</DictationProvider></TooltipProvider>
  );
  return { engine, editorRef, insertTextAtCursor, wrapper,
    complete: (text: string) => complete(text), options: () => options };
}

describe("composer dictation wiring", () => {
  it("keeps the microphone hidden without an injected desktop engine", () => {
    function WebComposer() {
      const dictation = useComposerDictation({ editorRef: { current: null }, resetKey: "web" });
      return <DictationControls dictation={dictation} />;
    }
    const { container } = render(<WebComposer />);
    expect(container).toBeEmptyDOMElement();
  });

  it("records on click, stops on second click and inserts the result without sending", async () => {
    const f = fixture();
    const activate = vi.fn();
    function Composer() {
      const dictation = useComposerDictation({ editorRef: f.editorRef, resetKey: "draft", activate });
      return <DictationControls dictation={dictation} />;
    }
    render(<Composer />, { wrapper: f.wrapper });
    const record = await screen.findByRole("button", { name: copy.dictation.start });
    await waitFor(() => expect(record).toBeEnabled());
    fireEvent.click(record);
    expect(activate).toHaveBeenCalledOnce();
    expect(f.engine.transcribe).toHaveBeenCalledOnce();
    act(() => f.options().onRecording());
    expect(screen.getByRole("status")).toHaveTextContent(copy.dictation.recording);
    fireEvent.click(screen.getByRole("button", { name: copy.dictation.stop }));
    expect(f.engine.stop).toHaveBeenCalledOnce();
    expect(f.insertTextAtCursor).not.toHaveBeenCalled();
    await act(async () => f.complete("edit this before sending"));
    expect(f.insertTextAtCursor).toHaveBeenCalledExactlyOnceWith("edit this before sending");
  });

  it("keeps the recording indicator until the stop acknowledgement arrives", async () => {
    const f = fixture();
    let acknowledge!: () => void;
    vi.mocked(f.engine.stop).mockImplementation(() => new Promise<void>((resolve) => { acknowledge = resolve; }));
    function Composer() {
      const dictation = useComposerDictation({ editorRef: f.editorRef, resetKey: "draft" });
      return <DictationControls dictation={dictation} />;
    }
    render(<Composer />, { wrapper: f.wrapper });
    const record = await screen.findByRole("button", { name: copy.dictation.start });
    await waitFor(() => expect(record).toBeEnabled());
    fireEvent.click(record);
    act(() => f.options().onRecording());
    fireEvent.click(screen.getByRole("button", { name: copy.dictation.stop }));
    const stopping = screen.getByRole("button", { name: copy.dictation.stopping });
    expect(stopping).toBeDisabled();
    expect(stopping).toHaveAttribute("aria-pressed", "true");
    expect(stopping.querySelector(".animate-pulse")).not.toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent(copy.dictation.stopping);
    await act(async () => acknowledge());
    expect(screen.getByRole("button", { name: copy.dictation.transcribing })).toHaveAttribute("aria-pressed", "false");
    await act(async () => f.complete("before stop"));
  });

  it("discards a result when the user switches drafts", async () => {
    const f = fixture();
    const { result, rerender } = renderHook(({ resetKey }) => useComposerDictation({ editorRef: f.editorRef, resetKey }), {
      initialProps: { resetKey: "draft-a" }, wrapper: f.wrapper,
    });
    await waitFor(() => expect(result.current.availability?.available).toBe(true));
    act(() => result.current.start());
    rerender({ resetKey: "draft-b" });
    expect(f.options().signal.aborted).toBe(true);
    await act(async () => f.complete("belongs to draft a"));
    expect(f.insertTextAtCursor).not.toHaveBeenCalled();
  });

  it("waits for a lazy editor to mount before requesting microphone access", async () => {
    const f = fixture();
    const activate = vi.fn();
    const { result, rerender } = renderHook(({ ready }) => useComposerDictation({ editorRef: f.editorRef, resetKey: "draft", ready, activate }), {
      initialProps: { ready: false }, wrapper: f.wrapper,
    });
    await waitFor(() => expect(result.current.availability?.available).toBe(true));
    act(() => result.current.start());
    expect(activate).toHaveBeenCalledOnce();
    expect(result.current.isActive()).toBe(true);
    expect(f.engine.transcribe).not.toHaveBeenCalled();
    rerender({ ready: true });
    expect(f.engine.transcribe).toHaveBeenCalledOnce();
    await act(async () => f.complete("text"));
    expect(f.insertTextAtCursor).toHaveBeenCalledExactlyOnceWith("text");
  });

  it("cancels capture when a persistent composer is hidden or disabled", async () => {
    const f = fixture();
    const { result, rerender } = renderHook(({ disabled }) => useComposerDictation({ editorRef: f.editorRef, resetKey: "draft", disabled }), {
      initialProps: { disabled: false }, wrapper: f.wrapper,
    });
    await waitFor(() => expect(result.current.availability?.available).toBe(true));
    act(() => result.current.start());
    rerender({ disabled: true });
    expect(f.options().signal.aborted).toBe(true);
    await act(async () => f.complete("hidden draft"));
    expect(f.insertTextAtCursor).not.toHaveBeenCalled();
  });

  it("aborts on unmount and ignores late completion", async () => {
    const f = fixture();
    const { result, unmount } = renderHook(() => useComposerDictation({ editorRef: f.editorRef, resetKey: "draft" }), { wrapper: f.wrapper });
    await waitFor(() => expect(result.current.availability?.available).toBe(true));
    act(() => result.current.start());
    unmount();
    expect(f.options().signal.aborted).toBe(true);
    await act(async () => f.complete("late text"));
    expect(f.insertTextAtCursor).not.toHaveBeenCalled();
  });

  it.each(["permission_denied", "unavailable"] as const)("announces preflight %s and exposes a keyboard-reachable explanation", async (reason) => {
    const f = fixture();
    vi.mocked(f.engine.check).mockResolvedValue({ available: false, reason });
    const { result } = renderHook(() => useComposerDictation({ editorRef: f.editorRef, resetKey: "draft" }), { wrapper: f.wrapper });
    await waitFor(() => expect(result.current.availability).toEqual({ available: false, reason }));
    render(<DictationControls dictation={result.current} />, { wrapper: f.wrapper });
    expect(screen.getByRole("button", { name: copy.dictation.start })).toBeDisabled();
    const status = screen.getByRole("alert");
    expect(status).toHaveTextContent(copy.dictation.errors[reason]);
    expect(status).toBeVisible();
    expect(status).not.toHaveClass("sr-only");
    const explanation = screen.getByRole("group", { name: copy.dictation.start });
    expect(explanation).toHaveAttribute("tabindex", "0");
    explanation.focus();
    expect(explanation).toHaveFocus();
    expect(explanation).toHaveAccessibleDescription(copy.dictation.errors[reason]);
    expect(screen.getByRole("button", { name: copy.dictation.start })).toHaveAccessibleDescription(copy.dictation.errors[reason]);
    act(() => result.current.start());
    expect(f.engine.transcribe).not.toHaveBeenCalled();
  });
});
