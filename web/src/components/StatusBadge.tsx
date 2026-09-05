import { statusPresentation } from "../lib/status";

// Colour-coded status pill driven by the shared presentation map. A dot
// carries the colour so the label stays readable at badge size.
export function StatusBadge({ status }: { status: string }) {
  const p = statusPresentation(status);
  // Soft badges tint their colour; the ghost fallback has none to tint.
  const soft = p.badgeClass === "badge-ghost" ? "" : "badge-soft";
  return (
    <span className={`badge ${p.badgeClass} ${soft} gap-1.5 whitespace-nowrap font-medium`}>
      <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden />
      {p.label}
    </span>
  );
}
