package loadpoint

// EVRampHeadroomW is the per-LP buffer added on top of the EV's
// current draw when computing the surplus reserve. It needs to cover
// one single-amp step on the EV's current phase mode so the
// controller can ramp up between ticks without the battery hoarding
// the surplus.
//
// 2000 W comfortably covers:
//   - any single-amp 1Φ step (+230 W) and 3Φ step (+690 W)
//   - climbing 1Φ × 6 A → 1Φ × 14 A in one tick (+1840 W)
//   - 1Φ-max → 3Φ-min crossover (1Φ × 16 A 3680 W → 3Φ × 6 A 4140 W,
//     +460 W) — the realistic phase-change step the EV takes after
//     climbing the 1Φ ladder
//
// What 2 kW does NOT cover is the direct 1Φ × 6 A → 3Φ × 6 A
// cold-start jump (+2760 W). In practice pickSurplusSteps walks the
// 1Φ ladder first, so this transition takes ~2 dispatch ticks (≈ 10 s)
// instead of 1. Acceptable trade vs. the alternative — sizing the
// headroom to that worst-case (3 kW) recreates the operator-reported
// bug where the user's "3 kW exporting" scenario lines up exactly
// with the reserve and the battery gets nothing.
//
// Tracking the LP's actual next reachable step via AllowedStepsW
// would tighten this further; deferred until 2 kW proves wrong on a
// real site.
const EVRampHeadroomW = 2000

// SurplusReserveW returns the aggregate PV headroom that must be
// preserved for surplus_only loadpoints. For each surplus_only +
// plugged_in LP it reserves min(MaxChargeW, CurrentPowerW +
// EVRampHeadroomW) so the reserve tracks the EV's actual draw rather
// than its theoretical max.
//
// Dispatch consumes the result via control.State.EVSurplusOnlyReserveW
// in both the energy and the legacy/reactive paths.
func SurplusReserveW(states []State) float64 {
	var sum float64
	for _, st := range states {
		if !st.SurplusOnly || !st.PluggedIn {
			continue
		}
		// Tie the reserve to the EV's ACTUAL draw, not just "plugged in
		// + surplus_only". A car that's Complete, refusing the offer,
		// or whose vehicle driver has gone offline (Tesla proxy flake
		// etc.) reports CurrentPowerW≈0 — leaving 2 kW reserved for it
		// makes the home battery hold steady at SoC while the same
		// 2 kW exports to grid. The wake-kick path (controller.tickOne)
		// commands cmd_w = min-step (1380 W) during the kick window,
		// which materialises as CurrentPowerW>50 W within one tick if
		// the EV is actually going to start — at which point this
		// reserve activates and the battery makes room.
		//
		// Threshold 50 W picks up any non-trivial draw while ignoring
		// idle pilot / standby consumption that doesn't represent
		// "EV is actively claiming surplus".
		if st.CurrentPowerW < 50.0 {
			continue
		}
		ceiling := st.CurrentPowerW + EVRampHeadroomW
		if ceiling > st.MaxChargeW {
			ceiling = st.MaxChargeW
		}
		if ceiling < 0 {
			ceiling = 0
		}
		sum += ceiling
	}
	return sum
}
