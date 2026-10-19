/** Toggle only the currently visible torrents, preserving selections in other filters. */
export function toggleVisibleSelection(selected: ReadonlySet<string>, visibleIds: string[]): Set<string> {
  const next = new Set(selected);
  const allSelected = visibleIds.every((id) => selected.has(id));
  for (const id of visibleIds) {
    if (allSelected) next.delete(id);
    else next.add(id);
  }
  return next;
}

/** Use the existing delete endpoint sequentially so a large selection cannot flood the client. */
export async function deleteTorrents(ids: string[], remove: (id: string) => Promise<void>) {
  const deleted: string[] = [];
  const failed: { id: string; message: string }[] = [];
  for (const id of ids) {
    try {
      await remove(id);
      deleted.push(id);
    } catch (err) {
      failed.push({ id, message: err instanceof Error ? err.message : String(err) });
    }
  }
  return { deleted, failed };
}
