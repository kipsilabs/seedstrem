import { useState } from "react";
import { api, ProwlarrIndexer } from "../../../api";
import { SectionProps } from "../types";
import { SectionCard, TextField, NumberField } from "../fields";

// Category lists are edited as comma-separated strings for convenience.
function parseCategories(s: string): number[] {
  return s
    .split(",")
    .map((p) => Number(p.trim()))
    .filter((n) => Number.isFinite(n) && n > 0);
}

export function Prowlarr({ config, update }: SectionProps) {
  const [prowlarrTest, setProwlarrTest] = useState("");
  const [indexers, setIndexers] = useState<ProwlarrIndexer[] | null>(null);
  const [indexersMsg, setIndexersMsg] = useState("");

  async function testProwlarr() {
    setProwlarrTest("…");
    try {
      const res = await api.testProwlarr(config.prowlarr.url, config.prowlarr.api_key);
      setProwlarrTest(res.ok ? "✓ Connected" : `✗ ${res.error}`);
    } catch (err) {
      setProwlarrTest(`✗ ${(err as Error).message}`);
    }
  }

  async function loadIndexers() {
    setIndexersMsg("…");
    try {
      const res = await api.listProwlarrIndexers(config.prowlarr.url, config.prowlarr.api_key);
      if (res.ok && res.indexers) {
        setIndexers(res.indexers);
        setIndexersMsg(res.indexers.length ? "" : "No indexers found");
      } else {
        setIndexersMsg(`✗ ${res.error ?? "failed"}`);
      }
    } catch (err) {
      setIndexersMsg(`✗ ${(err as Error).message}`);
    }
  }

  const toggleIndexer = (id: number, checked: boolean) =>
    update((c) => {
      const set = new Set(c.prowlarr.indexer_ids);
      if (checked) set.add(id);
      else set.delete(id);
      c.prowlarr.indexer_ids = [...set].sort((a, b) => a - b);
    });

  return (
    <SectionCard
      title="Prowlarr"
      description="seedstrem searches your Prowlarr indexers for torrents. Configure at least one indexer inside Prowlarr itself."
    >
      <TextField
        label="Prowlarr URL"
        placeholder="http://prowlarr:9696"
        value={config.prowlarr.url}
        onChange={(v) => update((c) => (c.prowlarr.url = v))}
      />
      <TextField
        label="API key"
        type="password"
        placeholder="unchanged"
        value={config.prowlarr.api_key}
        onChange={(v) => update((c) => (c.prowlarr.api_key = v))}
      />
      <div className="grid gap-4 sm:grid-cols-3">
        <TextField
          label="Movie categories"
          value={config.prowlarr.movie_categories.join(", ")}
          onChange={(v) => update((c) => (c.prowlarr.movie_categories = parseCategories(v)))}
        />
        <TextField
          label="TV categories"
          value={config.prowlarr.tv_categories.join(", ")}
          onChange={(v) => update((c) => (c.prowlarr.tv_categories = parseCategories(v)))}
        />
        <TextField
          label="Anime categories"
          value={config.prowlarr.anime_categories.join(", ")}
          onChange={(v) => update((c) => (c.prowlarr.anime_categories = parseCategories(v)))}
        />
      </div>

      <NumberField
        label="Search timeout (seconds)"
        min={1}
        hint="Global budget for a search. Indexers still answering when it elapses are dropped, and partial results are returned."
        value={config.prowlarr.search_timeout_seconds}
        onChange={(v) => update((c) => (c.prowlarr.search_timeout_seconds = v))}
      />

      <div className="flex flex-col gap-1">
        <span className="label-text">Search indexers</span>
        <p className="mt-1 text-sm opacity-70">
          Restrict searches to specific indexers. Leave all unchecked to search every indexer.
        </p>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <button type="button" className="btn btn-outline btn-sm" onClick={loadIndexers}>
            Load indexers
          </button>
          {indexersMsg && <span className="text-sm">{indexersMsg}</span>}
        </div>
        {indexers && indexers.length > 0 && (
          <div className="mt-3 grid grid-cols-2 gap-1 sm:grid-cols-3">
            {indexers.map((ix) => (
              <label key={ix.id} className="flex cursor-pointer items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  className="checkbox checkbox-sm"
                  checked={(config.prowlarr.indexer_ids ?? []).includes(ix.id)}
                  onChange={(e) => toggleIndexer(ix.id, e.target.checked)}
                />
                <span className="label-text">{ix.name}</span>
              </label>
            ))}
          </div>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <button type="button" className="btn btn-outline" onClick={testProwlarr}>
          Test connection
        </button>
        {prowlarrTest && <span className="text-sm">{prowlarrTest}</span>}
      </div>
    </SectionCard>
  );
}
