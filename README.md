# Redmine Link Cards for Mattermost

Personal Redmine issue previews for Mattermost 11.10+. Read-only Go server plugin and TypeScript/React webapp for web and desktop. The server never copies private issue details into posts, attachments or channel-wide metadata.

## Install and configure

1. Build with `make bundle`, or download the build artifact. Upload `dist/space.altlinux.redmine-0.1.0.tar.gz` under System Console → Plugins → Management.
2. In **System Console → Plugins → Redmine Link Cards**, enter **Redmine server URL**, for example `https://my.basealt.space`. An installation subpath is supported. HTTPS is required; redirects are refused so API keys cannot be forwarded to another host.
3. Supply a 32-byte base64 encryption secret to the Mattermost process as `MM_REDMINE_ENCRYPTION_KEY`, or set `MM_REDMINE_ENCRYPTION_KEY_FILE` to a file containing that secret. Generate using `openssl rand -base64 32`. Use the same secret on every HA node. The administrator EncryptionKey setting is a fallback; keeping the secret outside the database is preferred. Do not publish this key or change it without reconnecting accounts.
4. Enable Redmine REST API in Administration → Settings → API. The Mattermost server must reach the configured URL.
5. Enable the plugin. Each user selects **Подключить Redmine** from Mattermost's main menu and submits their own Redmine API key through the private HTTPS form. The key is checked using `/users/current.json`, encrypted with AES-256-GCM and stored in plugin KV. The original key is never returned to the client.

A browser cannot safely use this integration over plain HTTP. Proxy the Mattermost service with HTTPS in production.

## Using cards

Hover or keyboard-focus a matching issue link to open its personal card. Plain URL labels are shortened to `RM #123`; after that user's card has loaded, its title and status are shown in that user's client. Custom link labels are preserved. Click the link to open Redmine. Use ⟳ in the card to bypass both caches. Native mobile clients need their own UI integration against the same endpoints; this release does not modify native apps.

Cards include title, status, priority, assignee, project, tracker, updated date and up to 600 characters of description. Description is displayed as plain text; private notes and attachments are not requested.

## Cache and access model

- Client: IndexedDB plus memory, maximum 500 entries; usable for 300 seconds from the original Redmine response. No stale display after expiry. Offline/timeout does not extend validity. Browser storage failure falls back to memory.
- Server: bounded in-process per-user cache, maximum 1024 entries, TTL 90 seconds. Same-user concurrent requests share one upstream request; requests are limited to eight upstream calls at a time with a bounded pending set.
- Keys include MM user, Redmine instance, credential revision and issue ID. Cached data are never reused between different MM users.
- Original source timestamps survive server cache hits. Personal HTTP responses use `Cache-Control: no-store`; IndexedDB is explicitly managed by the plugin.
- Logout clears client memory/IndexedDB and fences outstanding responses. Server entries remain until their 90-second expiry. A valid MM session is required even for a server cache hit.
- Disconnect or API-key replacement changes an authoritative KV revision. Server requests check that revision before serving data and after upstream fetch. Old data become unreachable even on another HA node. Concurrent account changes use KV compare-and-set.
- HA nodes may have independent card caches; duplicate upstream requests on different nodes are possible. A shared Redis cache is not required.
- Access revocation in Redmine can remain invisible while cached data are fresh (up to five minutes). Cache storage is convenience, not a new permission source. Administrators controlling Mattermost can access stored credentials despite encryption at rest.

## API for custom clients

All endpoints require a valid Mattermost session and verified identity. Prefix: `/plugins/space.altlinux.redmine/api/v1`.

| Method | Path                    | Function                                                    |
| ------ | ----------------------- | ----------------------------------------------------------- |
| GET    | `/connection`           | Connected state, credential revision, configured server URL |
| POST   | `/connection`           | Body `{ "api_key": "..." }`; check and save personal key    |
| DELETE | `/connection`           | Disconnect and rotate revision                              |
| GET    | `/issues/123`           | Normalized personal card                                    |
| GET    | `/issues/123?refresh=1` | Bypass server cache                                         |

Successful cards include `source_fetched_at`, `expires_at` and `server_now` (Unix milliseconds). Custom clients must preserve age, account for clock offset using `server_now`, and never restart the 300-second lifetime when receiving a server cache hit. Handle `connection_changed` by dropping old-revision data. No raw Redmine API key belongs in client card storage.

## Development

Go 1.25+ and Node 22+ are required. Run `make test` and `make bundle`. Tests cover credential encryption boundaries, URL validation, per-user cache isolation, source-based TTL, simultaneous requests, disconnect during fetch, session requirements, and cross-host redirect rejection.

This release uses supported MM tooltip, global-component and message-format hooks. Compact plain-URL labels are produced only during local rendering; existing Markdown labels and code spans are preserved. No DOM patch or shared message mutation is used. Verify UI after Mattermost upgrades.
