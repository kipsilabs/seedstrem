import { Link } from "react-router-dom";
import { api, Torrent } from "../api";
import { formatBytes, formatSpeed } from "../lib/format";
import { DASHBOARD_STATUSES } from "../lib/status";
import { usePolling } from "../lib/usePolling";
import { useToast } from "../components/Toast";
import { StatCard, StatRow } from "../components/StatCard";
import { Skeleton } from "../components/Skeleton";
import { FreshnessIndicator } from "../components/FreshnessIndicator";
import { PageHeader } from "../components/PageHeader";
import { ProgressCell } from "../components/ProgressCell";
import { Icon } from "../components/Icon";

const ACTIVE_LIMIT = 5;

export function Dashboard() {
  const toast = useToast();
  const { data: status, error, isStale, isOffline, lastUpdated } = usePolling(
    api.status,
    { baseIntervalMs: 5000 },
  );
  const { data: torrents } = usePolling(api.torrents, { baseIntervalMs: 5000 });

  if (!status) {
    if (error) {
      return (
        <div className="alert alert-error">
          <Icon name="alert" />
          <span>Couldn&rsquo;t load status. Retrying automatically…</span>
        </div>
      );
    }
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Dashboard" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  async function copyManifest() {
    try {
      await navigator.clipboard.writeText(status!.manifest_url);
      toast.info("Manifest URL copied");
    } catch {
      toast.error("Couldn't copy to clipboard");
    }
  }

  const externalHostMismatch =
    new URL(status.external_url).host !== window.location.host;
  // stremio:// deep link installs the addon directly in the Stremio app.
  const stremioDeepLink = status.manifest_url.replace(/^https?:\/\//, "stremio://");
  // Older backends only report the qbittorrent key.
  const downloaderStatus = status.downloader ?? status.qbittorrent;
  const downloaderName = status.downloader?.type === "deluge" ? "Deluge" : "qBittorrent";
  const trackedTorrents = Object.values(status.torrents ?? {}).reduce((a, b) => a + b, 0);
  const freeSpace = status.disk?.free;
  const freeSpaceHint =
    freeSpace === undefined
      ? "unavailable"
      : status.disk?.free_source === "local"
        ? "on host"
        : `on ${downloaderName}`;

  const active = (torrents ?? []).filter((t) => t.status === "downloading");
  const failing = (torrents ?? []).filter((t) => t.status === "error");

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Dashboard"
        subtitle={
          trackedTorrents === 0
            ? "Nothing tracked yet"
            : `${trackedTorrents} torrent${trackedTorrents === 1 ? "" : "s"} tracked`
        }
        actions={
          <FreshnessIndicator
            isStale={isStale}
            isOffline={isOffline}
            lastUpdated={lastUpdated}
          />
        }
      />

      {!downloaderStatus.connected && (
        <div className="alert alert-error items-start">
          <Icon name="alert" className="mt-0.5" />
          <div className="flex-1">
            <div className="font-semibold">{downloaderName} is unreachable</div>
            <div className="text-sm opacity-80">
              {downloaderStatus.error || "Streams cannot start until the client is back."}
            </div>
          </div>
          <Link to="/settings" className="btn btn-sm">
            Open settings
          </Link>
        </div>
      )}

      {externalHostMismatch && (
        <div className="alert alert-warning items-start">
          <Icon name="alert" className="mt-0.5" />
          <span className="text-sm">
            The external URL <code className="font-mono">{status.external_url}</code>{" "}
            doesn&rsquo;t match the address you&rsquo;re browsing from. Players may not reach
            the stream links it generates. Check the Server section in Settings.
          </span>
        </div>
      )}

      <Pipeline counts={status.torrents ?? {}} />

      {failing.length > 0 && <AttentionList torrents={failing} />}

      <ActiveList torrents={active} total={active.length} />

      <StatRow columns={3}>
        <StatCard
          label="Uploaded"
          value={formatBytes(status.total_uploaded ?? 0)}
          hint="total seeded"
          accent="success"
        />
        <StatCard
          label="Disk used by seedstrem"
          value={formatBytes(status.disk?.used ?? 0)}
          hint={`across ${trackedTorrents} torrent${trackedTorrents === 1 ? "" : "s"}`}
        />
        <StatCard
          label="Free space"
          value={freeSpace === undefined ? "—" : formatBytes(freeSpace)}
          hint={freeSpaceHint}
        />
      </StatRow>

      <div className="surface p-6">
        <div className="flex items-start gap-4">
          <span className="grid h-10 w-10 shrink-0 place-items-center rounded-field bg-primary/15 text-primary">
            <Icon name="play" />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="text-lg font-bold tracking-brand">Stremio addon</h2>
            <p className="mt-1 max-w-prose text-sm opacity-70">
              Install this addon in Stremio, then search for a movie or show. seedstrem finds
              torrents through Prowlarr and streams them via {downloaderName}.
            </p>
          </div>
        </div>
        <div className="mt-4 flex flex-col gap-2 sm:flex-row sm:items-center">
          <input
            readOnly
            aria-label="Manifest URL"
            className="input input-bordered flex-1 font-mono text-sm"
            value={status.manifest_url}
            onFocus={(e) => e.currentTarget.select()}
          />
          <div className="flex gap-2">
            <button className="btn flex-1 sm:flex-none" onClick={copyManifest}>
              <Icon name="copy" size={16} />
              Copy
            </button>
            <a className="btn btn-primary flex-1 sm:flex-none" href={stremioDeepLink}>
              Install in Stremio
            </a>
          </div>
        </div>
      </div>
    </div>
  );
}

// The torrent lifecycle as one strip. Statuses really are a sequence, so the
// stages sit in order under a single seam; each stage is a filter shortcut.
function Pipeline({ counts }: { counts: Record<string, number> }) {
  return (
    <div className="surface overflow-hidden">
      <div className="brand-seam h-0.5" aria-hidden />
      <div className="grid grid-cols-3 divide-x divide-base-content/8 sm:grid-cols-6">
        {DASHBOARD_STATUSES.map(({ key, label }, i) => {
          const n = counts[key] ?? 0;
          const tone =
            key === "error" && n > 0
              ? "text-error"
              : key === "downloading" && n > 0
                ? "text-primary"
                : n === 0
                  ? "opacity-40"
                  : "";
          return (
            <Link
              key={key}
              to={`/torrents?status=${key}`}
              className={`flex flex-col gap-1 px-4 py-3 transition-colors hover:bg-base-300/50 ${
                i >= 3 ? "border-t border-base-content/8 sm:border-t-0" : ""
              }`}
            >
              <span className="text-[13px] opacity-60">{label}</span>
              <span className={`text-2xl font-semibold tabular-nums ${tone}`}>{n}</span>
            </Link>
          );
        })}
      </div>
    </div>
  );
}

function ActiveList({ torrents, total }: { torrents: Torrent[]; total: number }) {
  const shown = torrents.slice(0, ACTIVE_LIMIT);
  return (
    <section className="surface">
      <header className="flex items-center justify-between gap-3 px-5 py-3">
        <h2 className="font-semibold">Downloading now</h2>
        <Link to="/torrents" className="link link-hover flex items-center gap-1 text-sm opacity-70">
          All torrents
          <Icon name="arrow-right" size={14} />
        </Link>
      </header>
      {shown.length === 0 ? (
        <p className="border-t border-base-content/8 px-5 py-6 text-sm opacity-60">
          Nothing is downloading. Press play in Stremio and it shows up here.
        </p>
      ) : (
        <ul className="divide-y divide-base-content/8 border-t border-base-content/8">
          {shown.map((t) => (
            <li key={t.id} className="grid gap-x-4 gap-y-2 px-5 py-3 sm:grid-cols-[1fr_10rem_6rem_4rem]">
              <div className="min-w-0">
                <div className="truncate text-sm font-medium">{t.name || t.hash}</div>
                {t.indexer && <div className="truncate text-xs opacity-50">{t.indexer}</div>}
              </div>
              <ProgressCell progress={t.progress} />
              <span className="text-sm tabular-nums opacity-80">{formatSpeed(t.speed)}</span>
              <span className="text-sm tabular-nums opacity-60">{t.seeders} seeds</span>
            </li>
          ))}
        </ul>
      )}
      {total > shown.length && (
        <Link
          to="/torrents?status=downloading"
          className="block border-t border-base-content/8 px-5 py-2.5 text-center text-sm opacity-70 hover:opacity-100"
        >
          {total - shown.length} more downloading
        </Link>
      )}
    </section>
  );
}

function AttentionList({ torrents }: { torrents: Torrent[] }) {
  return (
    <section className="surface border-error/30">
      <header className="flex items-center gap-2 px-5 py-3 text-error">
        <Icon name="alert" size={16} />
        <h2 className="font-semibold">
          {torrents.length === 1
            ? "1 torrent needs attention"
            : `${torrents.length} torrents need attention`}
        </h2>
      </header>
      <ul className="divide-y divide-base-content/8 border-t border-base-content/8">
        {torrents.slice(0, ACTIVE_LIMIT).map((t) => (
          <li key={t.id} className="px-5 py-3">
            <div className="truncate text-sm font-medium">{t.name || t.hash}</div>
            {t.error && <div className="mt-0.5 text-xs opacity-70">{t.error}</div>}
          </li>
        ))}
      </ul>
      <Link
        to="/torrents?status=error"
        className="block border-t border-base-content/8 px-5 py-2.5 text-center text-sm opacity-70 hover:opacity-100"
      >
        Review in Torrents
      </Link>
    </section>
  );
}
