-- torrent_files keeps the raw .torrent of every torrent seedstrem has
-- seen so a removed torrent can be re-added from History. Keyed by
-- infohash and deliberately NOT a foreign key to torrents: the file must
-- outlive the torrent row. Pruned together with torrent_deletions.
CREATE TABLE torrent_files (
  hash       TEXT PRIMARY KEY,
  data       BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
