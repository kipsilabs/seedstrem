import { ComponentType, FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "../../components/Icon";
import { api, Config } from "../../api";
import { useToast } from "../../components/Toast";
import { useNavigationGuard } from "../../components/NavigationGuard";
import { PageHeader } from "../../components/PageHeader";
import { Skeleton } from "../../components/Skeleton";
import { SectionDef, SectionProps } from "./types";
import { DownloadClient } from "./sections/DownloadClient";
import { Prowlarr } from "./sections/Prowlarr";
import { ContentTypes } from "./sections/ContentTypes";
import { Filters } from "./sections/Filters";
import { Metadata } from "./sections/Metadata";
import { Seeding } from "./sections/Seeding";
import { Rss } from "./sections/Rss";
import { PathMappings } from "./sections/PathMappings";
import { Server } from "./sections/Server";
import { Storage } from "./sections/Storage";
import { Streaming } from "./sections/Streaming";

const SECTIONS: SectionDef[] = [
  { id: "download-client", label: "Download client", icon: "download", group: "Connections" },
  { id: "prowlarr", label: "Prowlarr", icon: "search", group: "Connections" },
  { id: "content-types", label: "Content types", icon: "play", group: "Addon" },
  { id: "filters", label: "Result filters", icon: "filter", group: "Addon" },
  { id: "metadata", label: "Metadata", icon: "film", group: "Addon", restart: true },
  { id: "seeding", label: "Seeding & cleanup", icon: "repeat", group: "System" },
  { id: "rss", label: "RSS auto-grab", icon: "rss", group: "System" },
  { id: "storage", label: "Storage & disk", icon: "database", group: "System" },
  { id: "paths", label: "Path mappings", icon: "route", group: "System" },
  { id: "server", label: "Server", icon: "server", group: "System", restart: true },
  { id: "streaming", label: "Streaming", icon: "activity", group: "System" },
];

const COMPONENTS: Record<string, ComponentType<SectionProps>> = {
  "download-client": DownloadClient,
  prowlarr: Prowlarr,
  "content-types": ContentTypes,
  filters: Filters,
  metadata: Metadata,
  seeding: Seeding,
  rss: Rss,
  storage: Storage,
  paths: PathMappings,
  server: Server,
  streaming: Streaming,
};

const GROUP_ORDER = ["Connections", "Addon", "System"];

// Secrets arrive masked; blank them so the "empty = keep existing" convention
// holds and the mask is never resubmitted.
function blankSecrets(c: Config): Config {
  c.qbittorrent.password = "";
  c.deluge.password = "";
  c.server.admin_password = "";
  c.prowlarr.api_key = "";
  c.meta.tmdb_api_key = "";
  // Configs predating indexer_ids arrive without it (JSON null).
  c.prowlarr.indexer_ids = c.prowlarr.indexer_ids ?? [];
  // Configs predating the rss.filters block arrive without it.
  c.rss.filters = c.rss.filters ?? {
    min_size_mb: 0,
    max_size_mb: 0,
    categories: [],
    include_keywords: [],
    exclude_keywords: [],
  };
  c.rss.filters.categories = c.rss.filters.categories ?? [];
  c.rss.filters.include_keywords = c.rss.filters.include_keywords ?? [];
  c.rss.filters.exclude_keywords = c.rss.filters.exclude_keywords ?? [];
  return c;
}

export function Settings() {
  const toast = useToast();
  const { setGuard } = useNavigationGuard();

  const [config, setConfig] = useState<Config | null>(null);
  const [saved, setSaved] = useState<string>(""); // JSON snapshot of last-saved state
  const [active, setActive] = useState(SECTIONS[0].id);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api
      .getConfig()
      .then((c) => {
        const blanked = blankSecrets(c);
        setConfig(blanked);
        setSaved(JSON.stringify(blanked));
      })
      .catch(() => toast.error("Couldn't load configuration"));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const dirty = config !== null && JSON.stringify(config) !== saved;

  const discard = () => {
    if (saved) setConfig(JSON.parse(saved) as Config);
  };

  // Register the unsaved-changes guard for in-app navigation, plus a native
  // beforeunload prompt for reload/close.
  const dirtyRef = useRef(false);
  dirtyRef.current = dirty;

  // Cmd/Ctrl+S saves, the way every editor does.
  const formRef = useRef<HTMLFormElement>(null);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        if (dirtyRef.current) formRef.current?.requestSubmit();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  useEffect(() => {
    setGuard(() => dirtyRef.current);
    return () => setGuard(null);
  }, [setGuard]);
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      if (dirtyRef.current) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, []);

  const update = (fn: (c: Config) => void) =>
    setConfig((prev) => {
      if (!prev) return prev;
      const next = structuredClone(prev);
      fn(next);
      return next;
    });

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!config) return;
    setSaving(true);
    try {
      const res = await api.putConfig(config);
      const blanked = blankSecrets(res.config);
      setConfig(blanked);
      setSaved(JSON.stringify(blanked));
      if (res.restart_required) {
        toast.info("Saved — some changes apply only after restarting seedstrem");
      } else {
        toast.success("Settings saved");
      }
    } catch (err) {
      toast.error(`Couldn't save — ${(err as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const grouped = useMemo(() => {
    return GROUP_ORDER.map((group) => ({
      group,
      items: SECTIONS.filter((s) => s.group === group),
    }));
  }, []);

  if (!config) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title="Settings" />
        <Skeleton className="h-96 w-full" />
      </div>
    );
  }

  const ActiveSection = COMPONENTS[active];
  const activeDef = SECTIONS.find((s) => s.id === active)!;

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Settings" />

      {/* Mobile section selector */}
      <select
        className="select select-bordered md:hidden"
        value={active}
        onChange={(e) => setActive(e.target.value)}
      >
        {SECTIONS.map((s) => (
          <option key={s.id} value={s.id}>
            {s.label}
            {s.restart ? " (restart required)" : ""}
          </option>
        ))}
      </select>

      <form ref={formRef} onSubmit={save} className="flex gap-6">
        {/* Desktop sub-nav */}
        <nav className="surface hidden w-56 shrink-0 self-start p-2 md:block">
          {grouped.map(({ group, items }) => (
            <div key={group} className="mb-2">
              <div className="px-3 py-1.5 text-xs font-medium opacity-50">
                {group}
              </div>
              {items.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  onClick={() => setActive(s.id)}
                  className={[
                    "flex w-full items-center gap-2 rounded-field px-3 py-2 text-left text-sm transition-colors",
                    active === s.id
                      ? "bg-base-300 font-medium text-base-content shadow-[inset_2px_0_0_0_var(--color-primary)]"
                      : "text-base-content/70 hover:bg-base-300/60",
                  ].join(" ")}
                >
                  <Icon name={s.icon} size={16} className={active === s.id ? "text-primary" : "opacity-70"} />
                  <span className="flex-1 truncate">{s.label}</span>
                  {s.restart && (
                    <span className="text-[11px] text-warning" title="Restart required">
                      restart
                    </span>
                  )}
                </button>
              ))}
            </div>
          ))}
        </nav>

        {/* Active section + sticky save bar */}
        <div className="min-w-0 flex-1">
          {activeDef.restart && (
            <div className="alert alert-warning mb-4 py-2 text-sm">
              <Icon name="info" size={16} />
              <span>Changes in this section apply only after restarting seedstrem.</span>
            </div>
          )}
          <ActiveSection config={config} update={update} />

          <div className="sticky bottom-0 z-10 mt-4 flex items-center justify-between gap-3 rounded-box border border-base-content/10 bg-base-100/95 p-3 backdrop-blur">
            <span className="text-sm">
              {dirty ? (
                <span className="flex items-center gap-2 text-warning">
                  <span className="inline-block h-2 w-2 rounded-full bg-warning" />
                  Unsaved changes
                </span>
              ) : (
                <span className="opacity-50">All changes saved</span>
              )}
            </span>
            <div className="flex items-center gap-2">
              {dirty && !saving && (
                <button type="button" className="btn btn-ghost" onClick={discard}>
                  Discard
                </button>
              )}
              <button className="btn btn-primary" disabled={saving || !dirty}>
                {saving ? <span className="loading loading-spinner loading-sm" /> : "Save settings"}
              </button>
            </div>
          </div>
        </div>
      </form>
    </div>
  );
}
