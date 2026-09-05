import { ReactNode, useMemo, useState } from "react";
import { api } from "../api";
import { usePolling } from "../lib/usePolling";
import { deletionEvidence, deletionReasonClass, deletionReasonLabel } from "../lib/deletions";
import { FreshnessIndicator } from "../components/FreshnessIndicator";
import { PageHeader } from "../components/PageHeader";
import { Skeleton } from "../components/Skeleton";
import { Icon } from "../components/Icon";

function when(unixSeconds: number): string {
  return new Date(unixSeconds * 1000).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function History() {
  const { data, isStale, isOffline, lastUpdated, refresh } = usePolling(api.deletions, {
    baseIntervalMs: 10000,
  });
  const [reason, setReason] = useState<string | null>(null);

  const counts = useMemo(
    () =>
      (data ?? []).reduce<Record<string, number>>((acc, d) => {
        acc[d.reason] = (acc[d.reason] ?? 0) + 1;
        return acc;
      }, {}),
    [data],
  );
  const visible = useMemo(
    () => (data ?? []).filter((d) => reason === null || d.reason === reason),
    [data, reason],
  );

  if (!data) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="History" />
        <div className="surface p-4">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="mb-3 h-10 w-full" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="History"
        subtitle={
          data.length > 0
            ? `${data.length} removal${data.length === 1 ? "" : "s"} in the last 48 hours`
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

      {data.length === 0 ? (
        <div className="surface p-10 text-center">
          <span className="mx-auto grid h-12 w-12 place-items-center rounded-full bg-base-300 opacity-80">
            <Icon name="history" size={22} />
          </span>
          <h2 className="mt-4 text-lg font-bold tracking-brand">Nothing removed recently</h2>
          <p className="mx-auto mt-1 max-w-md text-sm opacity-70">
            Torrents removed in the last 48 hours appear here, with the reason and the
            measurement behind each decision.
          </p>
        </div>
      ) : (
        <>
          {Object.keys(counts).length > 1 && (
            <div className="flex flex-wrap gap-1.5" role="group" aria-label="Filter by reason">
              <ReasonChip active={reason === null} onClick={() => setReason(null)}>
                All <span className="tabular-nums opacity-60">{data.length}</span>
              </ReasonChip>
              {Object.entries(counts).map(([r, n]) => (
                <ReasonChip
                  key={r}
                  active={reason === r}
                  onClick={() => setReason(reason === r ? null : r)}
                >
                  {deletionReasonLabel(r)} <span className="tabular-nums opacity-60">{n}</span>
                </ReasonChip>
              ))}
            </div>
          )}

          {/* Desktop table */}
          <div className="surface hidden overflow-x-auto md:block">
            <table className="table w-full">
              <thead>
                <tr>
                  <th>Removed</th>
                  <th>Name</th>
                  <th>Reason</th>
                  <th>Evidence</th>
                  <th>Indexer</th>
                  <th>Files</th>
                </tr>
              </thead>
              <tbody>
                {visible.map((d) => (
                  <tr key={d.id}>
                    <td className="whitespace-nowrap text-sm opacity-70">
                      {when(d.deleted_at)}
                    </td>
                    <td className="w-full max-w-0">
                      <div className="truncate font-medium">{d.name || d.hash}</div>
                    </td>
                    <td>
                      <span className={`badge ${reasonBadge(d.reason)} badge-sm`}>
                        {deletionReasonLabel(d.reason)}
                      </span>
                    </td>
                    <td className="text-sm opacity-80">{deletionEvidence(d)}</td>
                    <td className="text-sm opacity-70">{d.indexer || "—"}</td>
                    <td className="text-sm opacity-70">
                      {d.files_deleted ? "Deleted" : "Kept"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* Mobile cards */}
          <div className="flex flex-col gap-3 md:hidden">
            {visible.map((d) => (
              <div key={d.id} className="surface p-4">
                <div className="flex items-start justify-between gap-2">
                  <div className="truncate font-medium">{d.name || d.hash}</div>
                  <span
                    className={`badge ${reasonBadge(d.reason)} badge-sm shrink-0`}
                  >
                    {deletionReasonLabel(d.reason)}
                  </span>
                </div>
                <div className="mt-1 text-sm opacity-80">{deletionEvidence(d)}</div>
                <div className="mt-2 flex justify-between text-xs opacity-60">
                  <span>{when(d.deleted_at)}</span>
                  <span>{d.files_deleted ? "Files deleted" : "Files kept"}</span>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

// Soft badges tint their colour; ghost has none to tint.
function reasonBadge(reason: string): string {
  const cls = deletionReasonClass(reason);
  return cls === "badge-ghost" ? cls : `${cls} badge-soft`;
}

function ReasonChip({
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
