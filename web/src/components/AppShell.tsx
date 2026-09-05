import { useEffect, useState } from "react";
import { Outlet, useNavigate } from "react-router-dom";
import { api, SESSION_EXPIRED_EVENT } from "../api";
import { applyTheme, getStoredTheme, ThemeName, toggleTheme } from "../lib/theme";
import { usePolling } from "../lib/usePolling";
import { Sidebar } from "./Sidebar";
import { OfflineBanner } from "./OfflineBanner";
import { ConfirmDialog } from "./ConfirmDialog";
import { Skeleton } from "./Skeleton";
import { NavigationGuardProvider } from "./NavigationGuard";
import { Icon } from "./Icon";
import mark from "../assets/mark.png";

// The authenticated application shell: left sidebar on desktop, a top bar +
// slide-in drawer on mobile, plus app-wide offline and session-expired handling.
export function AppShell() {
  const navigate = useNavigate();
  const [checked, setChecked] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [expired, setExpired] = useState(false);
  const [theme, setTheme] = useState<ThemeName>(getStoredTheme);

  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  useEffect(() => {
    api
      .sessionInfo()
      .then(() => setChecked(true))
      .catch(() => navigate("/login"));
  }, [navigate]);

  useEffect(() => {
    const onExpired = () => setExpired(true);
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
  }, []);

  // Close the mobile drawer with Escape.
  useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setDrawerOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [drawerOpen]);

  const onToggleTheme = () => setTheme((t) => toggleTheme(t));

  const onLogout = () => {
    api
      .logout()
      .catch(() => {})
      .finally(() => navigate("/login"));
  };

  if (!checked) {
    return (
      <div className="min-h-screen bg-base-200 p-4">
        <div className="mx-auto max-w-5xl space-y-4 pt-8">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-64 w-full" />
        </div>
      </div>
    );
  }

  return (
    <NavigationGuardProvider>
      <Shell
        theme={theme}
        drawerOpen={drawerOpen}
        setDrawerOpen={setDrawerOpen}
        onToggleTheme={onToggleTheme}
        onLogout={onLogout}
      />
      <ConfirmDialog
        open={expired}
        title="Session expired"
        confirmLabel="Go to login"
        cancelLabel="Dismiss"
        onCancel={() => setExpired(false)}
        onConfirm={() => {
          setExpired(false);
          navigate("/login");
        }}
      >
        You&rsquo;ve been signed out. Log in again to continue.
      </ConfirmDialog>
    </NavigationGuardProvider>
  );
}

interface ShellProps {
  theme: ThemeName;
  drawerOpen: boolean;
  setDrawerOpen: (open: boolean) => void;
  onToggleTheme: () => void;
  onLogout: () => void;
}

// Split out so the health poll only starts once the session check passed.
function Shell({ theme, drawerOpen, setDrawerOpen, onToggleTheme, onLogout }: ShellProps) {
  const health = usePolling(api.status, { baseIntervalMs: 15000 });
  const sidebar = (onNavigate?: () => void) => (
    <Sidebar
      theme={theme}
      status={health.data}
      statusStale={health.isStale || health.isOffline}
      onToggleTheme={onToggleTheme}
      onLogout={onLogout}
      onNavigate={onNavigate}
    />
  );

  return (
    <div className="min-h-screen bg-base-200 md:flex">
      <aside className="sticky top-0 hidden h-screen shrink-0 border-r border-base-content/10 md:block">
        {sidebar()}
      </aside>

      <div className="flex items-center gap-2 border-b border-base-content/10 bg-base-100 px-3 py-2 md:hidden">
        <button
          className="btn btn-ghost btn-sm btn-square"
          aria-label="Open menu"
          aria-expanded={drawerOpen}
          onClick={() => setDrawerOpen(true)}
        >
          <Icon name="menu" size={20} />
        </button>
        <span className="flex items-center gap-2 font-bold tracking-brand">
          <span className="grid h-7 w-7 place-items-center rounded-field bg-[#0b1220] ring-1 ring-white/10">
            <img src={mark} alt="" className="h-5 w-5" />
          </span>
          seed<span className="text-accent">strem</span>
        </span>
      </div>

      {drawerOpen && (
        <div className="fixed inset-0 z-40 md:hidden">
          <button
            className="absolute inset-0 bg-black/50"
            aria-label="Close menu"
            onClick={() => setDrawerOpen(false)}
          />
          <div className="absolute top-0 left-0 h-full shadow-xl">
            {sidebar(() => setDrawerOpen(false))}
          </div>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <OfflineBanner />
        <main className="w-full max-w-6xl flex-1 p-4 md:p-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
