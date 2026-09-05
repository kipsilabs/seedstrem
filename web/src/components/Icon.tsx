import { SVGProps } from "react";

// One stroke-based icon set so every glyph shares weight and optical size,
// instead of emoji and box-drawing characters that render differently per OS.
export type IconName =
  | "dashboard"
  | "torrents"
  | "history"
  | "settings"
  | "sun"
  | "moon"
  | "logout"
  | "menu"
  | "close"
  | "trash"
  | "search"
  | "refresh"
  | "chevron"
  | "copy"
  | "check"
  | "alert"
  | "info"
  | "play"
  | "link"
  | "filter"
  | "film"
  | "repeat"
  | "rss"
  | "database"
  | "route"
  | "server"
  | "activity"
  | "download"
  | "arrow-right";

const PATHS: Record<IconName, string> = {
  dashboard: "M4 4h6v7H4zM14 4h6v4h-6zM14 12h6v8h-6zM4 15h6v5H4z",
  torrents: "M12 4v11m-5-5 5 5 5-5M5 20h14",
  history: "M12 21a9 9 0 1 0-9-9m9-4v5l3 2M3 4v4h4",
  settings: "M4 7h9M17 7h3M4 17h3M11 17h9M13 5v4M7 15v4",
  sun: "M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4",
  moon: "M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z",
  logout: "M9 21H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3M16 17l5-5-5-5M21 12H9",
  menu: "M4 7h16M4 12h16M4 17h16",
  close: "M6 6l12 12M18 6 6 18",
  trash: "M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13M10 11v6M14 11v6",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM21 21l-4.3-4.3",
  refresh: "M20 12a8 8 0 1 1-2.3-5.7M20 4v5h-5",
  chevron: "m6 9 6 6 6-6",
  copy: "M9 9h11v11H9zM5 15H4V4h11v1",
  check: "m5 12 5 5L20 7",
  alert: "M12 3 2 20h20L12 3zM12 10v4M12 17.5v.5",
  info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8v.5",
  play: "M7 4v16l13-8z",
  link: "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1",
  filter: "M3 5h18l-7 8v6l-4 2v-8z",
  film: "M4 4h16v16H4zM4 9h16M4 15h16M9 4v16M15 4v16",
  repeat: "M17 2l4 4-4 4M3 11V8a2 2 0 0 1 2-2h16M7 22l-4-4 4-4M21 13v3a2 2 0 0 1-2 2H3",
  rss: "M4 11a9 9 0 0 1 9 9M4 4a16 16 0 0 1 16 16M5 19h.5",
  database: "M12 8c4.4 0 8-1.3 8-3s-3.6-3-8-3-8 1.3-8 3 3.6 3 8 3zM4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3",
  route: "M6 20a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM18 8a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM6 16V9a3 3 0 0 1 3-3h4M18 8v7a3 3 0 0 1-3 3h-4",
  server: "M3 5h18v6H3zM3 13h18v6H3zM7 8h.5M7 16h.5",
  activity: "M3 12h4l3-8 4 16 3-8h4",
  download: "M12 4v11m-5-5 5 5 5-5M5 20h14",
  "arrow-right": "M5 12h14m-6-6 6 6-6 6",
};

interface IconProps extends Omit<SVGProps<SVGSVGElement>, "name"> {
  name: IconName;
  size?: number;
}

export function Icon({ name, size = 18, className = "", ...rest }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.75}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      focusable="false"
      className={`shrink-0 ${className}`}
      {...rest}
    >
      <path d={PATHS[name]} />
    </svg>
  );
}
