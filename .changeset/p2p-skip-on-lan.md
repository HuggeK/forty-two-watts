---
"forty-two-watts": patch
---

Dashboard: fix the 5–10 s first-load stall and collapse duplicate request
storms — three related changes to how the live dashboard talks to the backend.

**P2P no longer stalls the first paint on LAN / home-host.** The Phase 5 P2P
transport (`window.p2pFetch`) engaged on every visit, including direct
connections where `apiBase()` is empty. There the WebRTC handshake (STUN, needs
WAN) never opens a channel, so the first `/api/status` poll — which gates the
whole live render — blocked on the 8 s `CONNECT_TIMEOUT_MS` before falling back
to plain `fetch`. P2P now skips that path and `p2pFetch` falls straight through
to `fetch`, so the dashboard paints immediately; the transport indicator stays
hidden where P2P doesn't apply instead of showing an un-toggleable "Relay" badge.

**Live 24 h history is deduped.** `/api/history?range=24h&points=288` was
fetched on boot, the 1-min poll, and every (undebounced) window resize, so a
first-load layout resize storm fanned out into many identical requests. A small
in-flight-coalescing + 15 s-TTL cache (`fetchHistory`, mirroring
`ftw-history-card`'s `dailyFetchCache`) now shares one payload across those
triggers; the periodic poll forces a fresh sample.

**Notification-history badge is deduped.** `<ftw-notif-history>` now shares one
in-flight request and a short-TTL cache for `/api/notifications/history` across
the badge poll and modal open, collapsing transient bursts to a single request.
The modal's manual Refresh button forces a fresh fetch, and non-OK responses are
never cached.
