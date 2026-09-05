import { ReactNode } from "react";
import { usePageTitle } from "../lib/usePageTitle";

interface PageHeaderProps {
  title: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
}

// Consistent page title row with an optional subtitle and right-aligned
// actions. Also labels the browser tab with the page name.
export function PageHeader({ title, subtitle, actions }: PageHeaderProps) {
  usePageTitle(title);
  return (
    <div className="flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-[22px] font-bold tracking-brand">{title}</h1>
        {subtitle && <p className="mt-1 text-sm opacity-60">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
