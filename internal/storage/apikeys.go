package storage

// apiKeysTable holds the access keys for outside agents (internal/apikey). The
// single MCP key that settings kept before becomes a full-access key named
// "MCP", so clients already set up keep working.
const apiKeysTable = `CREATE TABLE api_keys(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  hash TEXT NOT NULL,
  max_risk INTEGER NOT NULL,
  modules TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  last_ip TEXT NOT NULL DEFAULT '',
  revoked_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO api_keys(id, name, hash, max_risk, created_at)
  SELECT 'mcp', 'MCP', json_extract(value, '$.hash'), 2,
         coalesce(CAST(strftime('%s', json_extract(value, '$.created_at')) AS INTEGER), CAST(strftime('%s','now') AS INTEGER))
  FROM settings WHERE key = 'mcp_api_key' AND json_valid(value) AND coalesce(json_extract(value, '$.hash'), '') <> '';
DELETE FROM settings WHERE key = 'mcp_api_key';`
