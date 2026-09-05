import { ThemeName } from "../lib/theme";
import type { Status } from "../api";
import { NavItem } from "./NavItem";
import { Icon } from "./Icon";
import mark from "../assets/mark.png";

interface SidebarProps {
  theme: ThemeName;
  status: Status | null;
  statusStale: boolean;
  onToggleTheme: () => void;
  onLogout: () => void;
  onNavigate?: () => void;
}

// The persistent left navigation: brand, primary nav, a live health line for
// the download client, and a footer with the theme toggle and logout. Rendered
// both in the desktop rail and inside the mobile drawer.
export function Sidebar({
  theme,
  status,
  statusStale,
  onToggleTheme,
  onLogout,
  onNavigate,
}: SidebarProps) {
  const dark = theme === "seedstrem-dark";
  const downloader = status?.downloader ?? status?.qbittorrent;
  const clientName = status?.downloader?.type === "deluge" ? "Deluge" : "qBittorrent";
  const errors = status?.torrents?.error ?? 0;

  return (
    <div className="flex h-full w-60 flex-col gap-1 bg-base-100 p-3">
      <div className="mb-3 flex items-center gap-3 px-2 py-2">
        <span className="grid h-10 w-10 place-items-center rounded-field bg-[#0b1220] shadow-sm ring-1 ring-white/10">
          <img src={mark} alt="" className="h-8 w-8" />
        </span>
        <span className="text-[17px] font-bold tracking-brand">
          seed<span className="text-accent">strem</span>
        </span>
      </div>

      <nav className="flex flex-col gap-0.5">
        <NavItem to="/" icon="dashboard" label="Dashboard" end onNavigate={onNavigate} />
        <NavItem
          to="/torrents"
          icon="torrents"
          label="Torrents"
          badge={errors}
          onNavigate={onNavigate}
        />
        <NavItem to="/history" icon="history" label="History" onNavigate={onNavigate} />
        <NavItem to="/settings" icon="settings" label="Settings" onNavigate={onNavigate} />
      </nav>

      <div className="mt-auto flex flex-col gap-1 border-t border-base-content/10 pt-3">
        <HealthLine
          name={clientName}
          connected={downloader?.connected ?? null}
          stale={statusStale}
          version={downloader?.version}
        />
        <button
          className="flex items-center gap-3 rounded-field px-3 py-2 text-sm font-medium text-base-content/60 transition-colors hover:bg-base-300/60 hover:text-base-content"
          onClick={onToggleTheme}
        >
          <Icon name={dark ? "moon" : "sun"} className="opacity-80" />
          {dark ? "Dark theme" : "Light theme"}
        </button>
        <button
          className="flex items-center gap-3 rounded-field px-3 py-2 text-sm font-medium text-base-content/60 transition-colors hover:bg-error/10 hover:text-error"
          onClick={onLogout}
        >
          <Icon name="logout" className="opacity-80" />
          Log out
        </button>
        {status?.version && (
          <span className="px-3 pt-1 text-xs opacity-40">seedstrem {status.version}</span>
        )}
      </div>
    </div>
  );
}

interface HealthLineProps {
  name: string;
  connected: boolean | null;
  stale: boolean;
  version?: string;
}

// One line that answers "is my client reachable?" from any page.
function HealthLine({ name, connected, stale, version }: HealthLineProps) {
  const tone =
    connected === null || stale ? "bg-base-content/30" : connected ? "bg-success" : "bg-error";
  const text =
    connected === null
      ? "Checking…"
      : stale
        ? "Server unreachable"
        : connected
          ? `${name} connected`
          : `${name} unreachable`;
  return (
    <div className="flex items-center gap-3 px-3 py-2 text-sm">
      <span className="grid w-[18px] place-items-center">
        <span className={`h-2 w-2 rounded-full ${tone}`} aria-hidden />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-base-content/80">{text}</span>
        {connected && version && !stale && (
          <span className="block truncate text-xs opacity-50">v{version}</span>
        )}
      </span>
    </div>
  );
}
