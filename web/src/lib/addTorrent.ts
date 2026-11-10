// Validation helpers for the Add torrent dialog. The server re-validates;
// these only give instant feedback before a round trip.

const MAGNET_RE = /^magnet:\?.*xt=urn:btih:[0-9a-zA-Z]{32,40}/i;

export function isMagnet(input: string): boolean {
  return MAGNET_RE.test(input.trim());
}

export function isTorrentFile(file: Pick<File, "name">): boolean {
  return file.name.toLowerCase().endsWith(".torrent");
}

// canSubmitAdd: a magnet link, or a .torrent file, is required — the file
// wins when both are supplied (it carries tracker passkeys a magnet may lack).
export function canSubmitAdd(magnet: string, file: Pick<File, "name"> | null): boolean {
  if (file) return isTorrentFile(file);
  return isMagnet(magnet);
}
