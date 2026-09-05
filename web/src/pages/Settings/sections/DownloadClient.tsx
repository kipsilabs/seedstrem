import { useState } from "react";
import { api } from "../../../api";
import { SectionProps } from "../types";
import { SectionCard, TextField, NumberField } from "../fields";

export function DownloadClient({ config, update }: SectionProps) {
  const [testResult, setTestResult] = useState("");

  async function testQbittorrent() {
    setTestResult("…");
    try {
      const res = await api.testQbittorrent(
        config.qbittorrent.url,
        config.qbittorrent.username,
        config.qbittorrent.password,
        config.qbittorrent.category,
      );
      setTestResult(res.ok ? `✓ Connected (qBittorrent ${res.version})` : `✗ ${res.error}`);
    } catch (err) {
      setTestResult(`✗ ${(err as Error).message}`);
    }
  }

  async function testDeluge() {
    setTestResult("…");
    try {
      const res = await api.testDeluge(
        config.deluge.host,
        config.deluge.port,
        config.deluge.username,
        config.deluge.password,
      );
      setTestResult(res.ok ? `✓ Connected (${res.version})` : `✗ ${res.error}`);
    } catch (err) {
      setTestResult(`✗ ${(err as Error).message}`);
    }
  }

  const isDeluge = config.downloader.type === "deluge";

  return (
    <div className="flex flex-col gap-4">
      <SectionCard title="Download client" description="The torrent client seedstrem drives.">
        <label className="flex max-w-xs flex-col gap-1">
          <span className="label-text mb-1">Client</span>
          <select
            className="select select-bordered w-full"
            value={config.downloader.type}
            onChange={(e) => {
              setTestResult("");
              update((c) => (c.downloader.type = e.target.value));
            }}
          >
            <option value="qbittorrent">qBittorrent</option>
            <option value="deluge">Deluge</option>
          </select>
        </label>
      </SectionCard>

      {!isDeluge && (
        <SectionCard
          title="qBittorrent"
          description="Connects to the qBittorrent WebUI API. The WebUI must be enabled and reachable with the username/password below."
        >
          <TextField
            label="WebUI URL"
            placeholder="http://qbittorrent:8080"
            value={config.qbittorrent.url}
            onChange={(v) => update((c) => (c.qbittorrent.url = v))}
          />
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              label="Username"
              value={config.qbittorrent.username}
              onChange={(v) => update((c) => (c.qbittorrent.username = v))}
            />
            <TextField
              label="Password"
              type="password"
              placeholder="unchanged"
              value={config.qbittorrent.password}
              onChange={(v) => update((c) => (c.qbittorrent.password = v))}
            />
          </div>
          <TextField
            label="Category"
            placeholder="seedstrem"
            value={config.qbittorrent.category}
            onChange={(v) => update((c) => (c.qbittorrent.category = v))}
          />
          <TestRow onTest={testQbittorrent} result={testResult} />
        </SectionCard>
      )}

      {isDeluge && (
        <SectionCard
          title="Deluge"
          description="Connects to the Deluge 2 daemon RPC port (not the web UI). Enable “Allow Remote Connections” in the daemon settings and use an account from Deluge’s auth file. Install the bundled Seedstream plugin for fast seeking."
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              label="Host"
              placeholder="deluge"
              value={config.deluge.host}
              onChange={(v) => update((c) => (c.deluge.host = v))}
            />
            <NumberField
              label="Daemon port"
              placeholder="58846"
              value={config.deluge.port}
              onChange={(v) => update((c) => (c.deluge.port = v))}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              label="Username"
              placeholder="localclient"
              value={config.deluge.username}
              onChange={(v) => update((c) => (c.deluge.username = v))}
            />
            <TextField
              label="Password"
              type="password"
              placeholder="unchanged"
              value={config.deluge.password}
              onChange={(v) => update((c) => (c.deluge.password = v))}
            />
          </div>
          <TextField
            label="Label"
            placeholder="seedstrem"
            value={config.deluge.label}
            onChange={(v) => update((c) => (c.deluge.label = v))}
          />
          <TestRow onTest={testDeluge} result={testResult} />
        </SectionCard>
      )}
    </div>
  );
}

function TestRow({ onTest, result }: { onTest: () => void; result: string }) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <button type="button" className="btn btn-outline" onClick={onTest}>
        Test connection
      </button>
      {result && <span className="text-sm">{result}</span>}
    </div>
  );
}
