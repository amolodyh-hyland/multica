import type { Editor } from "@tiptap/core";

export function insertDictationText(editor: Editor | null, text: string): boolean {
  if (!editor || editor.isDestroyed || !editor.isEditable || !text) return false;
  return editor.chain().focus().insertContent({ type: "text", text }).run();
}
