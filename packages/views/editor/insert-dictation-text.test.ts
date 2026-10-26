import { afterEach, describe, expect, it, vi } from "vitest";
import { Editor } from "@tiptap/core";
import { createEditorExtensions } from "./extensions";
import { insertDictationText } from "./insert-dictation-text";

let editor: Editor | undefined;
afterEach(() => { editor?.destroy(); editor = undefined; });

function makeEditor() {
  editor = new Editor({ extensions: createEditorExtensions({}), content: "hello world", contentType: "markdown" });
  return editor;
}

describe("dictation insertion into a real composer editor", () => {
  it("inserts at the cursor, updates the draft, and can be undone", () => {
    const ed = makeEditor();
    const update = vi.fn();
    ed.on("update", update);
    ed.commands.setTextSelection(7);
    expect(insertDictationText(ed, "spoken ")).toBe(true);
    expect(ed.getText()).toBe("hello spoken world");
    expect(update).toHaveBeenCalledOnce();
    ed.commands.undo();
    expect(ed.getText()).toBe("hello world");
  });

  it("replaces selected text and treats markup and HTML as literal speech", () => {
    const ed = makeEditor();
    ed.commands.setTextSelection({ from: 7, to: 12 });
    insertDictationText(ed, "**spoken** <b>text</b>");
    expect(ed.getText()).toBe("hello **spoken** <b>text</b>");
    expect(ed.getJSON().content?.[0]?.content?.every((node) => !node.marks?.length)).toBe(true);
  });

  it("does not trigger the submit shortcut for a transcript containing line breaks", () => {
    const submit = vi.fn();
    editor = new Editor({ extensions: createEditorExtensions({ onSubmitRef: { current: submit } }) });
    insertDictationText(editor, "first\nsecond");
    expect(submit).not.toHaveBeenCalled();
    expect(editor.getText()).toContain("first");
  });

  it("ignores missing, readonly, destroyed editors and empty results", () => {
    expect(insertDictationText(null, "text")).toBe(false);
    const ed = makeEditor();
    expect(insertDictationText(ed, "")).toBe(false);
    ed.setEditable(false);
    expect(insertDictationText(ed, "text")).toBe(false);
    ed.destroy();
    expect(insertDictationText(ed, "text")).toBe(false);
  });
});
