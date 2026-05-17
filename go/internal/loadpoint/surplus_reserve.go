package loadpoint

// EVRampHeadroomW is the per-LP buffer added on top of the EV's
// current draw when computing the surplus reserve. It must cover the
// worst-case single-step ramp the EV controller will take in the next
// ~30 s: jumping from 1Φ at minimum to 3Φ at minimum (~6 A × 3 phases
// × 230 V ≈ 4140 W, minus the current 1Φ × 6 A ≈ 1380 W draw, ≈ 2 kW
// net). 2000 W is the round-trip step bound, generous enough that a
// flapping EV controller doesn't immediately lose its ramp window
// while still releasing the unused portion of MaxChargeW to the home
// battery — the prior `evReserveW += MaxChargeW` reserved the EV's
// theoretical max (often 11 kW for a 3Φ × 16 A wallbox) even when it
// was physically holding at 2-3 kW, starving battery charge on
// surprise PV slots.
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
