import { Fragment, ReactNode, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api, Torrent } from "../api";
import { formatBytes, formatSpeed, availableUntil } from "../lib/format";
import { statusPresentation } from "../lib/status";
import { usePolling } from "../lib/usePolling";
import {
  countByStatus,
  DEFAULT_SORT,
  filterTorrents,
  initialSort,
  nextSort,
  SortKey,
  SortSpec,
  sortTorrents,
  totalDownloadSpeed,
} from "../lib/torrentList";
import { useToast } from "../components/Toast";
import { StatusBadge } from "../components/StatusBadge";
import { ProgressCell } from "../components/ProgressCell";
import { FreshnessIndicator } from "../components/FreshnessIndicator";
import { PageHeader } from "../components/PageHeader";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Skeleton } from "../components/Skeleton";
import { Icon } from "../components/Icon";

const SORT_OPTIONS: { key: SortKey; label: string }[] = [
  { key: "added_at", label: "Added" },
  { key: "name", label: "Name" },
  { key: "status", label: "Status" },
  { key: "progress", label: "Progress" },
  { key: "speed", label: "Speed" },
  { key: "size", label: "Size" },
  { key: "ratio", label: "Ratio" },
];

export function Torrents() {
  const toast = useToast();
  const { data, isStale, isOffline, lastUpdated, refresh } = usePolling(api.torrents, {
    baseIntervalMs: 3000,
  });

  // The status filter lives in the URL so dashboard shortcuts can deep-link.
  const [params, setParams] = useSearchParams();
  const statusFilter = params.get("status");
  const setStatusFilter = (s: string | null) =>
    setParams(s ? { status: s } : {}, { replace: true });

  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SortSpec>(DEFAULT_SORT);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [copiedUrl, setCopiedUrl] = useState("");
  const [removed, setRemoved] = useState<Set<string>>(new Set());
  const [deleteTarget, setDeleteTarget] = useState<Torrent | null>(null);
  const [deleting, setDeleting] = useState(false);

  async function copy(url: string) {
    try {
      await navigator.clipboard.writeText(url);
      setCopiedUrl(url);
      setTimeout(() => setCopiedUrl(""), 1500);
      toast.info("Stream URL copied");
    } catch {
      toast.error("Couldn't copy to clipboard");
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return;
    const id = deleteTarget.id;
    setDeleting(true);
    // Optimistic hide; restore on failure.
    setRemoved((s) => new Set(s).add(id));
    try {
      await api.deleteTorrent(id);
      toast.success("Torrent removed");
      setDeleteTarget(null);
    } catch (err) {
      setRemoved((s) => {
        const next = new Set(s);
        next.delete(id);
        return next;
      });
      toast.error(`Couldn't remove torrent — ${(err as Error).message}`);
    } finally {
      setDeleting(false);
    }
  }

  const all = useMemo(() => (data ?? []).filter((t) => !removed.has(t.id)), [data, removed]);
  const counts = useMemo(() => countByStatus(all), [all]);
  const visible = useMemo(
    () => sortTorrents(filterTorrents(all, query, statusFilter), sort),
    [all, query, statusFilter, sort],
  );

  if (!data) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Torrents" />
        <div className="surface p-4">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="mb-3 h-10 w-full" />
          ))}
        </div>
      </div>
    );
  }

  const active = counts.downloading ?? 0;
  const speed = totalDownloadSpeed(all);
  const filtering = query.trim() !== "" || statusFilter !== null;

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Torrents"
        subtitle={
          all.length > 0
            ? `${all.length} tracked, ${active} downloading${
                speed > 0 ? ` at ${formatSpeed(speed)}` : ""
              }`
            : undefined
        }
        actions={
          <>
            <FreshnessIndicator
              isStale={isStale}
              isOffline={isOffline}
              lastUpdated={lastUpdated}
            />
            <button
              className="btn btn-ghost btn-sm btn-square"
              aria-label="Refresh now"
              title="Refresh now"
              onClick={refresh}
            >
              <Icon name="refresh" size={16} />
            </button>
          </>
        }
      />

      {(isStale || isOffline) && (
        <div className="alert alert-warning py-2">
          <span className="loading loading-spinner loading-xs" />
          <span className="text-sm">
            Can&rsquo;t reach the server — showing the last known data. Retrying…
          </span>
        </div>
      )}

      {all.length === 0 ? (
        <EmptyState />
      ) : (
        <>
          <div className="flex flex-col gap-3 md:flex-row md:items-center">
            <label className="input input-bordered flex items-center gap-2 md:w-72">
              <Icon name="search" size={16} className="opacity-50" />
              <input
                type="search"
                className="grow"
                placeholder="Search name, hash, or indexer"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              {query && (
                <button
                  type="button"
                  className="opacity-50 hover:opacity-100"
                  aria-label="Clear search"
                  onClick={() => setQuery("")}
                >
                  <Icon name="close" size={14} />
                </button>
              )}
            </label>
            <StatusChips counts={counts} total={all.length} value={statusFilter} onChange={setStatusFilter} />
            <label className="flex items-center gap-2 text-sm md:ml-auto md:hidden">
              <span className="opacity-60">Sort</span>
              <select
                className="select select-bordered select-sm"
                value={sort.key}
                onChange={(e) => setSort(initialSort(e.target.value as SortKey))}
              >
                {SORT_OPTIONS.map((o) => (
                  <option key={o.key} value={o.key}>
                    {o.label}
                  </option>
                ))}
              </select>
            </label>
          </div>

          {visible.length === 0 ? (
            <div className="surface p-8 text-center text-sm opacity-70">
              No torrents match{filtering ? " this filter." : "."}{" "}
              {filtering && (
                <button
                  className="link link-primary"
                  onClick={() => {
                    setQuery("");
                    setStatusFilter(null);
                  }}
                >
                  Clear filters
                </button>
              )}
            </div>
          ) : (
            <>
              {/* Desktop table */}
              <div className="surface hidden overflow-x-auto md:block">
                <table className="table w-full">
                  <thead>
                    <tr>
                      <th className="w-8" />
                      <SortTh label="Name" k="name" sort={sort} onSort={setSort} />
                      <SortTh label="Status" k="status" sort={sort} onSort={setSort} />
                      <SortTh label="Progress" k="progress" sort={sort} onSort={setSort} className="w-40" />
                      <SortTh label="Speed" k="speed" sort={sort} onSort={setSort} />
                      <th>Seeds</th>
                      <SortTh label="Size" k="size" sort={sort} onSort={setSort} />
                      <SortTh label="Ratio" k="ratio" sort={sort} onSort={setSort} />
                      <th>Available until</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {visible.map((t) => {
                      const open = expanded === t.id;
                      return (
                        <Fragment key={t.id}>
                          <tr
                            className="cursor-pointer hover:bg-base-300/50"
                            onClick={() => setExpanded(open ? null : t.id)}
                            aria-expanded={open}
                          >
                            <td className="pr-0">
                              <Icon
                                name="chevron"
                                size={16}
                                className={`opacity-50 transition-transform ${open ? "rotate-180" : ""}`}
                              />
                            </td>
                            <td className="w-full max-w-0">
                              <div className="truncate font-medium">{t.name || t.hash}</div>
                              <div className="flex gap-2 truncate text-xs opacity-60">
                                {t.indexer && <span>{t.indexer}</span>}
                                {t.origin === "adopted" && <span>adopted from client</span>}
                              </div>
                            </td>
                            <td>
                              <StatusBadge status={t.status} />
                            </td>
                            <td>
                              <ProgressCell progress={t.progress} />
                            </td>
                            <td className="whitespace-nowrap tabular-nums">
                              {t.status === "downloading" ? formatSpeed(t.speed) : "—"}
                            </td>
                            <td className="tabular-nums">
                              {t.status === "downloading" ? t.seeders : "—"}
                            </td>
                            <td className="whitespace-nowrap tabular-nums">{formatBytes(t.size)}</td>
                            <td className="tabular-nums">
                              <span className={t.ratio >= 1 ? "text-success" : ""}>
                                {t.ratio.toFixed(2)}
                              </span>
                            </td>
                            <td className="whitespace-nowrap">{availableUntil(t)}</td>
                            <td>
                              <button
                                className="btn btn-ghost btn-xs btn-square text-error"
                                aria-label={`Remove ${t.name || t.hash}`}
                                title="Remove torrent"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  setDeleteTarget(t);
                                }}
                              >
                                <Icon name="trash" size={15} />
                              </button>
                            </td>
                          </tr>
                          {open && (
                            <tr>
                              <td colSpan={10} className="bg-base-200/60">
                                <TorrentDetail t={t} copiedUrl={copiedUrl} onCopy={copy} />
                              </td>
                            </tr>
                          )}
                        </Fragment>
                      );
                    })}
                  </tbody>
                </table>
              </div>

              {/* Mobile cards */}
              <div className="flex flex-col gap-3 md:hidden">
                {visible.map((t) => (
                  <div key={t.id} className="surface p-4">
                    <div className="flex items-start justify-between gap-2">
                      <span className="min-w-0 flex-1 truncate font-medium">
                        {t.name || t.hash}
                      </span>
                      <button
                        className="btn btn-ghost btn-xs btn-square text-error"
                        aria-label={`Remove ${t.name || t.hash}`}
                        onClick={() => setDeleteTarget(t)}
                      >
                        <Icon name="trash" size={15} />
                      </button>
                    </div>
                    <div className="mt-2 flex flex-wrap items-center gap-2">
                      <StatusBadge status={t.status} />
                      <span className="text-xs opacity-60 tabular-nums">
                        {formatBytes(t.size)}
                      </span>
                      {t.indexer && <span className="truncate text-xs opacity-60">{t.indexer}</span>}
                      {t.origin === "adopted" && (
                        <span className="truncate text-xs opacity-60">adopted</span>
                      )}
                    </div>
                    <div className="mt-3">
                      <ProgressCell progress={t.progress} />
                    </div>
                    <dl className="mt-3 grid grid-cols-2 gap-y-1 text-xs opacity-70">
                      <div>Speed: {t.status === "downloading" ? formatSpeed(t.speed) : "—"}</div>
                      <div>Seeds: {t.status === "downloading" ? t.seeders : "—"}</div>
                      <div>Ratio: {t.ratio.toFixed(2)}</div>
                      <div>Until: {availableUntil(t)}</div>
                    </dl>
                    <button
                      className="btn btn-ghost btn-xs mt-2"
                      onClick={() => setExpanded(expanded === t.id ? null : t.id)}
                    >
                      <Icon
                        name="chevron"
                        size={14}
                        className={expanded === t.id ? "rotate-180" : ""}
                      />
                      {expanded === t.id ? "Hide files" : "Show files"}
                    </button>
                    {expanded === t.id && (
                      <div className="mt-2 rounded-box bg-base-200 p-3">
                        <TorrentDetail t={t} copiedUrl={copiedUrl} onCopy={copy} />
                      </div>
                    )}
                  </div>
                ))}
              </div>
            </>
          )}
        </>
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        title="Remove torrent?"
        confirmLabel="Remove"
        danger
        busy={deleting}
        onCancel={() => !deleting && setDeleteTarget(null)}
        onConfirm={confirmDelete}
      >
        <span className="break-words">
          Remove <strong>{deleteTarget?.name || deleteTarget?.hash}</strong> from seedstrem?
          Downloaded files are deleted only if that option is enabled in Settings.
        </span>
      </ConfirmDialog>
    </div>
  );
}

function EmptyState() {
  return (
    <div className="surface p-10 text-center">
      <span className="mx-auto grid h-12 w-12 place-items-center rounded-full bg-primary/15 text-primary">
        <Icon name="play" size={22} />
      </span>
      <h2 className="mt-4 text-lg font-bold tracking-brand">No torrents yet</h2>
      <p className="mx-auto mt-1 max-w-md text-sm opacity-70">
        Play something through the seedstrem addon in Stremio and it appears here, found via
        Prowlarr and downloaded through your torrent client.
      </p>
    </div>
  );
}

interface ChipsProps {
  counts: Record<string, number>;
  total: number;
  value: string | null;
  onChange: (s: string | null) => void;
}

// Only statuses that currently have torrents get a chip, so the row stays short.
function StatusChips({ counts, total, value, onChange }: ChipsProps) {
  const present = Object.keys(counts);
  return (
    <div className="flex flex-wrap gap-1.5" role="group" aria-label="Filter by status">
      <Chip active={value === null} onClick={() => onChange(null)}>
        All <span className="tabular-nums opacity-60">{total}</span>
      </Chip>
      {present.map((s) => (
        <Chip key={s} active={value === s} onClick={() => onChange(value === s ? null : s)}>
          {statusPresentation(s).label}{" "}
          <span className="tabular-nums opacity-60">{counts[s]}</span>
        </Chip>
      ))}
      {value !== null && !present.includes(value) && (
        <Chip active onClick={() => onChange(null)}>
          {statusPresentation(value).label} <span className="tabular-nums opacity-60">0</span>
        </Chip>
      )}
    </div>
  );
}

function Chip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`btn btn-sm rounded-full ${active ? "btn-primary" : "btn-ghost bg-base-100 ring-1 ring-base-content/10"}`}
    >
      {children}
    </button>
  );
}

interface SortThProps {
  label: string;
  k: SortKey;
  sort: SortSpec;
  onSort: (s: SortSpec) => void;
  className?: string;
}

function SortTh({ label, k, sort, onSort, className = "" }: SortThProps) {
  const active = sort.key === k;
  return (
    <th className={className} aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : undefined}>
      <button type="button" className="th-sort" onClick={() => onSort(nextSort(sort, k))}>
        {label}
        <Icon
          name="chevron"
          size={12}
          className={`transition-opacity ${active ? "opacity-100" : "opacity-0"} ${
            active && sort.dir === "asc" ? "rotate-180" : ""
          }`}
        />
      </button>
    </th>
  );
}

interface DetailProps {
  t: Torrent;
  copiedUrl: string;
  onCopy: (url: string) => void;
}

function TorrentDetail({ t, copiedUrl, onCopy }: DetailProps) {
  return (
    <div className="flex flex-col gap-2">
      {t.error && (
        <div className="alert alert-error py-2 text-sm">
          <Icon name="alert" size={16} />
          <span className="break-words">{t.error}</span>
        </div>
      )}
      {t.links.length === 0 ? (
        <span className="text-sm opacity-70">No files selected yet.</span>
      ) : (
        <ul className="flex flex-col gap-1">
          {t.links.map((l) => (
            <li key={l.url} className="flex items-center gap-2 text-sm">
              <span className="min-w-0 flex-1 truncate font-mono text-xs">{l.path}</span>
              <span className="whitespace-nowrap text-xs opacity-60">
                {formatBytes(l.bytes)}
              </span>
              <button
                className="btn btn-outline btn-xs"
                onClick={(e) => {
                  e.stopPropagation();
                  onCopy(l.url);
                }}
              >
                <Icon name={copiedUrl === l.url ? "check" : "copy"} size={13} />
                {copiedUrl === l.url ? "Copied" : "Copy stream URL"}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
