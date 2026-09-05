// Presentation helpers for the deletion history. Kept pure and separate
// from the page so the per-reason wording is unit-testable.
import type { Deletion } from "../api";

const REASON_LABELS: Record<string, string> = {
  seed_time: "Seed time",
  ratio: "Ratio",
  manual: "Manual",
  abandoned: "Abandoned",
  unadopted: "Un-adopted",
};

export function deletionReasonLabel(reason: string): string {
  return REASON_LABELS[reason] ?? reason;
}

// Badge colour per reason, so a burst of one cause stands out at a glance.
export function deletionReasonClass(reason: string): string {
  switch (reason) {
    case "seed_time":
      return "badge-info";
    case "ratio":
      return "badge-success";
    case "manual":
      return "badge-ghost";
    case "abandoned":
      return "badge-warning";
    case "unadopted":
      return "badge-error";
    default:
      return "badge-ghost";
  }
}

function hours(seconds: number): string {
  return `${Math.round(seconds / 3600)}h`;
}

function percent(fraction: number): string {
  return `${Math.round(fraction * 100)}%`;
}

// deletionEvidence renders the measurement behind the decision: what was
// observed, and the limit it was measured against. This is the column
// that says whether a removal was correct.
export function deletionEvidence(d: Deletion): string {
  switch (d.reason) {
    case "seed_time":
      return `seeded ${hours(d.seeding_time)} of ${hours(d.seed_limit)}`;
    case "ratio":
      return `ratio ${d.ratio.toFixed(2)} of ${d.ratio_limit.toFixed(2)}`;
    case "abandoned":
      return `progress ${percent(d.progress)} of ${percent(d.progress_limit)}`;
    case "manual":
      return "removed from the admin UI";
    case "unadopted":
      return "label disappeared — still in client";
    default:
      return "—";
  }
}
