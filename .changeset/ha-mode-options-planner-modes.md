---
"forty-two-watts": patch
---

Fix Home Assistant logging "Invalid option for select.forty_two_watts_mode"
for the planner modes. The MQTT discovery for the Mode `select` only
advertised six modes, but the bridge publishes the live mode as state — and
the default UI choices (`planner_passive_arbitrage` / `planner_arbitrage`)
weren't in the advertised list, so HA rejected them every cycle. The discovery
options and the API mode validator now both derive from a single
`control.AllModes` source of truth, so all ten modes are advertised and the
two lists can't drift again.
