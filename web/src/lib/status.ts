// Single source of truth for how a torrent status is presented. Previously the
// human labels lived in Dashboard.tsx while Torrents.tsx leaked the raw enum
// string; both now read from here.

export type Tone = "info" | "warning" | "neutral" | "primary" | "success" | "error";

export interface StatusPresentation {
  label: string;
  icon: string;
  badgeClass: string; // daisyUI badge modifier
  tone: Tone;
}

const PRESENTATION: Record<string, StatusPresentation> = {
  magnet_conversion: { label: "Resolving", icon: "◌", badgeClass: "badge-info", tone: "info" },
  waiting_files_selection: { label: "Waiting selection", icon: "◔", badgeClass: "badge-warning", tone: "warning" },
  queued: { label: "Queued", icon: "▪", badgeClass: "badge-ghost", tone: "neutral" },
  downloading: { label: "Downloading", icon: "⇩", badgeClass: "badge-primary", tone: "primary" },
  downloaded: { label: "Downloaded", icon: "✓", badgeClass: "badge-success", tone: "success" },
  error: { label: "Error", icon: "✕", badgeClass: "badge-error", tone: "error" },
};

const FALLBACK: StatusPresentation = {
  label: "Unknown",
  icon: "?",
  badgeClass: "badge-ghost",
  tone: "neutral",
};

export function statusPresentation(status: string): StatusPresentation {
  return PRESENTATION[status] ?? { ...FALLBACK, label: status || "Unknown" };
}

// Order + labels for the dashboard stat tiles.
export const DASHBOARD_STATUSES: { key: string; label: string }[] = Object.entries(
  PRESENTATION,
).map(([key, p]) => ({ key, label: p.label }));
