---
"forty-two-watts": patch
---

Dashboard: stop the ~8 s first-load stall on the LAN / home-host path.

The Phase 5 P2P transport (`window.p2pFetch`) was engaging on **every** visit,
including direct LAN / home-host connections where `apiBase()` is empty. On
those the WebRTC handshake (STUN, needs WAN) never produces a usable channel, so
the first `/api/status` poll — which gates the whole live dashboard render —
blocked on the 8 s `CONNECT_TIMEOUT_MS` before falling back to plain `fetch`,
making the dashboard take 5–10 s to come alive.

P2P now only engages on the remote relay path (`apiBase() !== ""`), where it
actually buys something. On LAN / home-host `p2pFetch` falls straight through to
`fetch`, so the first paint is immediate again. Remote relay behaviour is
unchanged.

The transport indicator is also corrected on the direct path: P2P is not
applicable there, so the badge stays hidden (`off`) instead of showing a
misleading, un-toggleable "Relay" state.

Also dedupes the live 24h history request (`/api/history?range=24h&points=288`).
It was triggered by boot, the 1-min poll, and every (undebounced) window
resize, so a first-load layout resize storm fanned out into many identical
requests. A small in-flight-coalescing + short-TTL cache (mirroring
`ftw-history-card`'s `dailyFetchCache`) now shares one payload across those
triggers; the periodic poll forces a fresh sample.
