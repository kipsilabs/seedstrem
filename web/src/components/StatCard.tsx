import { ReactNode } from "react";

type Accent = "default" | "primary" | "success" | "error";

interface StatCardProps {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  accent?: Accent;
}

const VALUE_ACCENT: Record<Accent, string> = {
  default: "",
  primary: "text-primary",
  success: "text-success",
  error: "text-error",
};

// A single metric. Intended to sit inside a divided row rather than as a
// standalone card, so several read as one instrument panel.
export function StatCard({ label, value, hint, accent = "default" }: StatCardProps) {
  return (
    <div className="px-5 py-4">
      <div className="text-[13px] opacity-60">{label}</div>
      <div className={`mt-1.5 text-2xl font-semibold tabular-nums ${VALUE_ACCENT[accent]}`}>
        {value}
      </div>
      {hint && <div className="mt-1 text-xs opacity-50">{hint}</div>}
    </div>
  );
}

// Wraps StatCards in one surface with hairline dividers between them.
export function StatRow({ children, columns = 3 }: { children: ReactNode; columns?: 2 | 3 | 4 }) {
  const cols = { 2: "sm:grid-cols-2", 3: "sm:grid-cols-3", 4: "sm:grid-cols-4" }[columns];
  return (
    <div
      className={`surface grid grid-cols-1 divide-y divide-base-content/8 overflow-hidden sm:divide-x sm:divide-y-0 ${cols}`}
    >
      {children}
    </div>
  );
}
