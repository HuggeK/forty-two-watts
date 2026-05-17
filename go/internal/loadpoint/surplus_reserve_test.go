package loadpoint

import "testing"

func TestSurplusReserveW(t *testing.T) {
	tests := []struct {
		name   string
		states []State
		want   float64
	}{
		{
			name:   "empty",
			states: nil,
			want:   0,
		},
		{
			name: "ignores not-surplus and not-plugged",
			states: []State{
				{SurplusOnly: false, PluggedIn: true, CurrentPowerW: 0, MaxChargeW: 11000},
				{SurplusOnly: true, PluggedIn: false, CurrentPowerW: 0, MaxChargeW: 11000},
			},
			want: 0,
		},
		{
			name: "EV at 2.5kW with 11kW max → current + headroom",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 2500, MaxChargeW: 11000},
			},
			want: 2500 + EVRampHeadroomW,
		},
		{
			name: "EV at 0W with 11kW max → just the headroom",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 0, MaxChargeW: 11000},
			},
			want: EVRampHeadroomW,
		},
		{
			name: "EV close to max → clamped to MaxChargeW (no overshoot)",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 10000, MaxChargeW: 11000},
			},
			want: 11000,
		},
		{
			name: "EV at max → reserve equals max",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 11000, MaxChargeW: 11000},
			},
			want: 11000,
		},
		{
			name: "multiple LPs sum",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 1500, MaxChargeW: 3700}, // 1500 + 2000 = 3500, under cap
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 0, MaxChargeW: 11000},   // 0 + 2000 = 2000
			},
			want: (1500 + EVRampHeadroomW) + (0 + EVRampHeadroomW),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SurplusReserveW(tt.states)
			if got != tt.want {
				t.Errorf("SurplusReserveW = %.0f, want %.0f", got, tt.want)
			}
		})
	}
}

// Concrete regression: user's reported bug. EV at 2.5 kW on an Easee
// with 11 kW max, plan says charge battery, 3 kW of PV exporting.
// Pre-fix the reserve was 11 kW so ceiling = pvSurplus − (reserve −
// current) = 3000 − (11000 − 2500) = −5500 → 0; battery idled and
// the 3 kW crossed the meter at low spot price. Post-fix the reserve
// is `current + EVRampHeadroomW`, so a meaningful share of the
// surprise surplus reaches the battery.
func TestSurplusReserveWReleasesUnusedMaxToBattery(t *testing.T) {
	states := []State{
		{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 2500, MaxChargeW: 11000},
	}
	reserve := SurplusReserveW(states)
	current := states[0].CurrentPowerW
	pvSurplus := 3000.0
	ceiling := pvSurplus - (reserve - current)
	if ceiling <= 0 {
		t.Fatalf("ceiling = %.0f W — battery should get some of the 3 kW surplus, was 0 pre-fix", ceiling)
	}
}

// 1Φ ladder climb: EV at 1Φ × 6 A (1380 W) should be able to step
// up several amps in one tick without the battery hoarding the
// surplus. The headroom needs to cover the largest practical
// single-tick climb that pickSurplusSteps will take on the 1Φ ladder
// — ~1Φ × 14 A (3220 W, +1840 W) sits inside 2 kW. After climbing
// the 1Φ ladder, the phase change (1Φ × 16 A → 3Φ × 6 A, +460 W) is
// trivial. The direct 1Φ × 6 A → 3Φ × 6 A jump (+2760 W) is NOT
// covered — that takes 2 ticks instead of 1, accepted on purpose to
// keep the headroom from re-imposing the user-reported bug on
// 3 kW-surplus scenarios.
func TestSurplusReserveWAllowsOnePhaseLadderClimb(t *testing.T) {
	states := []State{
		{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 1380, MaxChargeW: 11000},
	}
	reserve := SurplusReserveW(states)
	reserveRemaining := reserve - states[0].CurrentPowerW
	const ladderClimbW = 3220 - 1380 // 1Φ × 6 A → 1Φ × 14 A ≈ 1840 W
	if reserveRemaining < ladderClimbW {
		t.Errorf("reserveRemaining = %.0f W — must be ≥ %.0f W so EV can climb 1Φ ladder in one tick",
			reserveRemaining, float64(ladderClimbW))
	}
}
