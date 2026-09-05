import { SectionProps } from "../types";
import { SectionCard, NumberField, SelectField, ToggleField } from "../fields";

export function Seeding({ config, update }: SectionProps) {
  return (
    <SectionCard title="Seeding & cleanup">
      <ToggleField
        label="Download the whole torrent (played file first) so a complete copy seeds — better for ratio on private trackers. Off = download only the played file."
        checked={config.seeding.full}
        onChange={(v) => update((c) => (c.seeding.full = v))}
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <NumberField
          label="Seed time before removal (hours, 0 = never)"
          min={0}
          value={config.cleanup.seed_time_hours}
          onChange={(v) => update((c) => (c.cleanup.seed_time_hours = v))}
          hint="A completed torrent is removed once it has seeded this long."
        />
        <NumberField
          label="Target seeding ratio (0 = disabled)"
          min={0}
          step={0.1}
          value={config.cleanup.target_ratio}
          onChange={(v) => update((c) => (c.cleanup.target_ratio = v))}
          hint="A completed torrent also becomes removal-eligible once it reaches this upload/download ratio — whichever of ratio or seed time comes first."
        />
      </div>
      <IndexerSeedTimes config={config} update={update} />
      <div className="grid gap-4 sm:grid-cols-2">
        <SelectField
          label="Delete order when cleaning up"
          value={config.cleanup.delete_policy || "oldest_first"}
          options={[
            { value: "oldest_first", label: "Oldest first (by add time)" },
            { value: "lowest_upload", label: "Lowest upload activity" },
          ]}
          onChange={(v) => update((c) => (c.cleanup.delete_policy = v))}
          hint="Order in which removal-eligible torrents are cleaned up."
        />
        <NumberField
          label="Min progress to keep on abandoned playback (%)"
          min={0}
          max={100}
          value={config.cleanup.min_progress_for_cancel_percent}
          onChange={(v) => update((c) => (c.cleanup.min_progress_for_cancel_percent = v))}
        />
      </div>
    </SectionCard>
  );
}

// IndexerSeedTimes edits per-indexer overrides of the global seed time —
// private trackers impose minimum seed times that public ones do not.
// Rows left with a blank indexer name are dropped on save.
function IndexerSeedTimes({ config, update }: SectionProps) {
  const rows = config.cleanup.indexer_seed_times ?? [];
  return (
    <div className="flex flex-col gap-2">
      <span className="label-text">Per-indexer seed time overrides</span>
      <span className="label-text-alt text-base-content/60">
        Torrents grabbed from these indexers use their own seed time instead of the global one.
        The name must match the Prowlarr indexer (case-insensitive); 0 hours = never remove.
      </span>
      {rows.map((row, i) => (
        <div className="flex items-end gap-2" key={i}>
          <label className="flex flex-1 flex-col gap-1">
            <span className="label-text mb-1">Indexer</span>
            <input
              className="input input-bordered w-full"
              value={row.indexer}
              onChange={(e) =>
                update((c) => (c.cleanup.indexer_seed_times[i].indexer = e.target.value))
              }
            />
          </label>
          <label className="flex w-40 flex-col gap-1">
            <span className="label-text mb-1">Seed time (hours)</span>
            <input
              type="number"
              min={0}
              className="input input-bordered w-full"
              value={row.seed_time_hours}
              onChange={(e) =>
                update(
                  (c) => (c.cleanup.indexer_seed_times[i].seed_time_hours = Number(e.target.value)),
                )
              }
            />
          </label>
          <button
            type="button"
            className="btn btn-outline btn-error"
            aria-label="Remove override"
            onClick={() => update((c) => c.cleanup.indexer_seed_times.splice(i, 1))}
          >
            ✕
          </button>
        </div>
      ))}
      <div>
        <button
          type="button"
          className="btn btn-outline btn-sm"
          onClick={() =>
            update((c) => {
              c.cleanup.indexer_seed_times = [
                ...(c.cleanup.indexer_seed_times ?? []),
                { indexer: "", seed_time_hours: 0 },
              ];
            })
          }
        >
          + Add override
        </button>
      </div>
    </div>
  );
}
