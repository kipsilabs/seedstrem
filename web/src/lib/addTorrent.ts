export function isTorrentFile(file: Pick<File, "name">): boolean {
  return file.name.toLowerCase().endsWith(".torrent");
}

export function canSubmitAdd(files: readonly Pick<File, "name">[]): boolean {
  return files.length > 0 && files.every(isTorrentFile);
}

export interface TorrentUploadFailure {
  file: File;
  error: string;
}

export async function addTorrentFiles(files: readonly File[], addFile: (file: File) => Promise<unknown>) {
  let added = 0;
  const failures: TorrentUploadFailure[] = [];
  for (const file of files) {
    try {
      await addFile(file);
      added++;
    } catch (err) {
      failures.push({ file, error: err instanceof Error ? err.message : String(err) });
    }
  }
  return { added, failures };
}
