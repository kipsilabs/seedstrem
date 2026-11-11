import { useRef, useState } from "react";
import { api } from "../api";
import { addTorrentFiles, canSubmitAdd, TorrentUploadFailure } from "../lib/addTorrent";
import { useToast } from "./Toast";

interface AddTorrentDialogProps {
  open: boolean;
  onClose: () => void;
  onAdded: () => void;
}

export function AddTorrentDialog({ open, onClose, onAdded }: AddTorrentDialogProps) {
  const toast = useToast();
  const [files, setFiles] = useState<File[]>([]);
  const [failures, setFailures] = useState<TorrentUploadFailure[]>([]);
  const [busy, setBusy] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  if (!open) return null;

  function close() {
    if (busy) return;
    setFiles([]);
    setFailures([]);
    onClose();
  }

  async function submit() {
    if (busy || !canSubmitAdd(files)) return;
    setBusy(true);
    setFailures([]);
    try {
      const result = await addTorrentFiles(files, api.addTorrentFile);
      setFiles(result.failures.map(({ file }) => file));
      setFailures(result.failures);
      if (fileInput.current) fileInput.current.value = "";
      if (result.added > 0) {
        toast.success(`Added ${result.added} torrent${result.added === 1 ? "" : "s"}`);
        onAdded();
      }
      if (result.failures.length === 0) onClose();
    } finally {
      setBusy(false);
    }
  }

  const invalidSelection = files.length > 0 && !canSubmitAdd(files);

  return (
    <div className="modal modal-open" role="dialog" aria-modal="true" aria-labelledby="add-torrent-title">
      <div className="modal-box surface">
        <h3 id="add-torrent-title" className="text-lg font-bold tracking-brand">
          Add torrents
        </h3>
        <div className="flex flex-col gap-4 py-3">
          <label className="form-control">
            <span className="mb-1 text-sm opacity-70">.torrent files</span>
            <input
              ref={fileInput}
              type="file"
              multiple
              accept=".torrent,application/x-bittorrent"
              className="file-input file-input-bordered w-full"
              disabled={busy}
              onChange={(e) => {
                setFiles(Array.from(e.target.files ?? []));
                setFailures([]);
              }}
            />
            <span className="mt-1 text-xs opacity-60">
              Select one or more .torrent files. Private tracker files carry your passkey.
            </span>
          </label>
          {invalidSelection && (
            <p className="text-sm text-error" role="alert">Every selected file must have a .torrent extension.</p>
          )}
          {files.length > 0 && (
            <div className="text-sm">
              <p className="mb-1 opacity-70">{files.length} file{files.length === 1 ? "" : "s"} selected</p>
              <ul className="max-h-40 overflow-y-auto space-y-1">
                {files.map((file, index) => <li key={index} className="break-all">{file.name}</li>)}
              </ul>
            </div>
          )}
          {failures.length > 0 && (
            <div className="text-sm text-error" role="alert">
              <p>These files couldn&rsquo;t be added. Retry them or select other files.</p>
              <ul className="mt-1 max-h-40 overflow-y-auto space-y-1">
                {failures.map(({ file, error }, index) => (
                  <li key={index} className="break-all">{file.name}: {error}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
        <div className="modal-action">
          <button className="btn btn-ghost" onClick={close} disabled={busy}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={submit} disabled={busy || !canSubmitAdd(files)}>
            {busy ? <><span className="loading loading-spinner loading-sm" />Adding…</> : failures.length > 0 ? "Retry failed" : "Add"}
          </button>
        </div>
      </div>
      <button className="modal-backdrop" aria-label="Close dialog" onClick={close} disabled={busy} />
    </div>
  );
}
