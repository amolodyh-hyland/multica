"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { getCurrentWsId } from "@multica/core/platform";
import { FolderSnapshotError, packFolderSnapshot, previewFolderSnapshot, type FolderSnapshot } from "@multica/core/attachments";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@multica/ui/components/ui/dialog";
import { filterFolderSymlinks } from "../../platform/folder-selection";
import { useT } from "../../i18n";
import { formatBytes } from "../../common/format-bytes";

export function ChatFolderAttachment({ files, onSelectFile, onClose }: {
  files: File[];
  onSelectFile: (file: File) => void;
  onClose: () => void;
}) {
  const { t } = useT("chat");
  const workspace = useCurrentWorkspace();
  const server = new URL(api.getBaseUrl() || "/", window.location.href).href.replace(/\/$/, "");
  const [target] = useState({ server, workspaceId: workspace?.id, workspaceName: workspace?.name });
  const [snapshot, setSnapshot] = useState<FolderSnapshot | null>(null);
  const [error, setError] = useState("");
  const [packing, setPacking] = useState(false);
  const alive = useRef(true);
  const busy = useRef(false);
  const targetCurrent = server === target.server && workspace?.id === target.workspaceId;
  const current = useRef(targetCurrent);
  current.current = targetCurrent;
  const visibleError = !targetCurrent || !target.workspaceId ? t(($) => $.folder.errors.changed) : error;

  const errorText = useCallback((err: unknown): string => {
    if (err instanceof FolderSnapshotError) return t(($) => $.folder.errors[err.code]);
    return t(($) => $.folder.errors.failed);
  }, [t]);

  useEffect(() => {
    alive.current = true;
    setSnapshot(null);
    setError("");
    let cancelled = false;
    void filterFolderSymlinks(files).then((result) => {
      if (cancelled) return;
      if (!result.verified) {
        setError(t(($) => $.folder.errors.unverified));
        return;
      }
      setSnapshot(previewFolderSnapshot(result.files, result.skipped));
    }).catch((err: unknown) => { if (!cancelled) setError(errorText(err)); });
    return () => { cancelled = true; alive.current = false; };
  }, [files, errorText, t]);

  async function confirm() {
    if (!snapshot || !current.current || !target.workspaceId || busy.current) return;
    busy.current = true;
    setPacking(true);
    setError("");
    try {
      const verified = await filterFolderSymlinks(snapshot.files.map(({ file }) => file));
      if (!verified.verified || verified.skipped.length) throw new FolderSnapshotError("changed");
      const archive = await packFolderSnapshot(snapshot);
      const currentServer = new URL(api.getBaseUrl() || "/", window.location.href).href.replace(/\/$/, "");
      if (!alive.current || !current.current) return;
      if (currentServer !== target.server || getCurrentWsId() !== target.workspaceId) throw new FolderSnapshotError("changed");
      onSelectFile(archive);
      onClose();
    } catch (err) {
      if (alive.current) setError(errorText(err));
    } finally {
      busy.current = false;
      if (alive.current) setPacking(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[85vh]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.folder.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.folder.description)}</DialogDescription>
        </DialogHeader>
        <div className="min-h-0 space-y-3 overflow-y-auto text-body-sm">
          <dl className="space-y-1 break-all">
            <div><dt className="font-medium">{t(($) => $.folder.server)}</dt><dd>{target.server}</dd></div>
            <div><dt className="font-medium">{t(($) => $.folder.workspace)}</dt><dd>{target.workspaceName} ({target.workspaceId})</dd></div>
          </dl>
          {snapshot && (
            <>
              <p className="break-all font-medium">{snapshot.name}</p>
              <p>{t(($) => $.folder.summary, { count: snapshot.files.length, bytes: snapshot.totalBytes, size: (snapshot.totalBytes / (1024 * 1024)).toFixed(2) })}</p>
              <ul className="max-h-56 overflow-y-auto break-all" aria-label={t(($) => $.folder.files)}>
                {snapshot.files.map(({ path, file }) => <li key={path}>{path} ({formatBytes(file.size)})</li>)}
              </ul>
              <p>{t(($) => $.folder.skip_policy)}</p>
              {snapshot.skipped.length > 0 && (
                <details>
                  <summary>{t(($) => $.folder.skipped, { count: snapshot.skipped.length })}</summary>
                  <ul className="max-h-32 overflow-y-auto break-all">{snapshot.skipped.map((path) => <li key={path}>{path}</li>)}</ul>
                </details>
              )}
            </>
          )}
          {visibleError && <p role="alert">{visibleError}</p>}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>{t(($) => $.folder.cancel)}</Button>
          <Button type="button" disabled={!snapshot || !targetCurrent || !target.workspaceId || packing} aria-busy={packing} onClick={() => void confirm()}>
            {packing ? t(($) => $.folder.preparing) : t(($) => $.folder.confirm)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
