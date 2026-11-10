import { useState } from "react";
import { api } from "../api";
import { canSubmitAdd, isMagnet } from "../lib/addTorrent";
import { useToast } from "./Toast";

interface AddTorrentDialogProps {
  open: boolean;
  onClose: () => void;
  onAdded: () => void;
}

// Add a torrent by pasting a magnet link or uploading a .torrent file. A
// .torrent is preferred for private trackers: it carries the passkey.
export function AddTorrentDialog({ open, onClose, onAdded }: AddTorrentDialogProps) {
  const toast = useToast();
  const [magnet, setMagnet] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);

  if (!open) return null;

  function close() {
    if (busy) return;
    setMagnet("");
    setFile(null);
    onClose();
  }

  async function submit() {
    setBusy(true);
    try {
      const added = file ? await api.addTorrentFile(file) : await api.addMagnet(magnet.trim());
      toast.success(`Added ${added.name || "torrent"}`);
      setMagnet("");
      setFile(null);
      onAdded();
      onClose();
    } catch (err) {
      toast.error(`Couldn't add torrent — ${(err as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  const magnetInvalid = magnet.trim() !== "" && !isMagnet(magnet) && !file;

  return (
    <div className="modal modal-open" role="dialog" aria-modal="true" aria-labelledby="add-torrent-title">
      <div className="modal-box surface">
        <h3 id="add-torrent-title" className="text-lg font-bold tracking-brand">
          Add torrent
        </h3>
        <div className="flex flex-col gap-4 py-3">
          <label className="form-control">
            <span className="mb-1 text-sm opacity-70">Magnet link</span>
            <textarea
              className={`textarea textarea-bordered h-24 w-full ${magnetInvalid ? "textarea-error" : ""}`}
              placeholder="magnet:?xt=urn:btih:…"
              value={magnet}
              disabled={busy || file !== null}
              onChange={(e) => setMagnet(e.target.value)}
            />
            {magnetInvalid && (
              <span className="mt-1 text-xs text-error">That doesn&rsquo;t look like a magnet link.</span>
            )}
          </label>
          <div className="divider my-0 text-xs opacity-60">or</div>
          <label className="form-control">
            <span className="mb-1 text-sm opacity-70">.torrent file</span>
            <input
              type="file"
              accept=".torrent,application/x-bittorrent"
              className="file-input file-input-bordered w-full"
              disabled={busy}
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
            <span className="mt-1 text-xs opacity-60">
              Recommended for private trackers — the file carries your passkey.
            </span>
          </label>
        </div>
        <div className="modal-action">
          <button className="btn btn-ghost" onClick={close} disabled={busy}>
            Cancel
          </button>
          <button className="btn btn-primary" onClick={submit} disabled={busy || !canSubmitAdd(magnet, file)}>
            {busy ? <span className="loading loading-spinner loading-sm" /> : "Add"}
          </button>
        </div>
      </div>
      <button className="modal-backdrop" aria-label="Close dialog" onClick={close} disabled={busy} />
    </div>
  );
}
