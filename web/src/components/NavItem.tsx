import { useLocation } from "react-router-dom";
import { useNavigationGuard } from "./NavigationGuard";
import { Icon, IconName } from "./Icon";

interface NavItemProps {
  to: string;
  icon: IconName;
  label: string;
  end?: boolean;
  badge?: number;
  onNavigate?: () => void;
}

// A single sidebar navigation entry. Routes through the navigation guard so an
// unsaved-changes prompt can intercept the move. The active entry gets a
// base-300 wash and a left marker; an optional count badge flags attention.
export function NavItem({ to, icon, label, end, badge, onNavigate }: NavItemProps) {
  const { pathname } = useLocation();
  const { requestNavigate } = useNavigationGuard();
  const isActive = end ? pathname === to : pathname.startsWith(to);

  return (
    <button
      onClick={() => {
        onNavigate?.();
        requestNavigate(to);
      }}
      aria-current={isActive ? "page" : undefined}
      className={[
        "group relative flex items-center gap-3 rounded-field px-3 py-2 text-left text-sm font-medium transition-colors",
        isActive
          ? "bg-base-300 text-base-content"
          : "text-base-content/60 hover:bg-base-300/60 hover:text-base-content",
      ].join(" ")}
    >
      {isActive && (
        <span
          className="absolute top-2 bottom-2 left-0 w-0.5 rounded-full bg-primary"
          aria-hidden
        />
      )}
      <Icon name={icon} className={isActive ? "text-primary" : "opacity-80"} />
      <span className="flex-1">{label}</span>
      {badge ? (
        <span className="badge badge-error badge-sm tabular-nums">{badge}</span>
      ) : null}
    </button>
  );
}
