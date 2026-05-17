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
			name: "EV at 2.5kW with 11kW max → 4.5kW reserve (current + 2kW)",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 2500, MaxChargeW: 11000},
			},
			want: 4500,
		},
		{
			name: "EV at 0W with 11kW max → 2kW reserve (just the headroom)",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 0, MaxChargeW: 11000},
			},
			want: 2000,
		},
		{
			name: "EV at 10kW with 11kW max → 11kW reserve (clamped to max, not 12kW)",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 10000, MaxChargeW: 11000},
			},
			want: 11000,
		},
		{
			name: "EV at 11kW with 11kW max → 11kW reserve (already at ceiling)",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 11000, MaxChargeW: 11000},
			},
			want: 11000,
		},
		{
			name: "multiple LPs sum",
			states: []State{
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 1500, MaxChargeW: 3700}, // 1Φ → 3500 < cap 3700
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 0, MaxChargeW: 11000},   // 2000
			},
			want: 5500,
		},
		{
			name: "negative current power floors at 0 then adds headroom",
			states: []State{
				// Telemetry glitch shouldn't push reserve negative.
				{SurplusOnly: true, PluggedIn: true, CurrentPowerW: -300, MaxChargeW: 11000},
			},
			want: 1700, // -300 + 2000 = 1700; still positive, no floor needed
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

func TestSurplusReserveWReleasesUnusedMaxToBattery(t *testing.T) {
	// Concrete regression: user's reported bug. EV at 2.5 kW on an
	// Easee with 11 kW max. Pre-fix reserve was 11 kW; post-fix is
	// 4.5 kW. With 3 kW of PV exporting beyond the EV, dispatch
	// should now leave ceiling = pvSurplus - (reserve - current) =
	// 3000 - (4500 - 2500) = 1000 W available to the battery. Pre-fix
	// reserve - current = 8500 W → ceiling = -5500 → 0.
	states := []State{
		{SurplusOnly: true, PluggedIn: true, CurrentPowerW: 2500, MaxChargeW: 11000},
	}
	reserve := SurplusReserveW(states)
	current := states[0].CurrentPowerW
	pvSurplus := 3000.0
	ceiling := pvSurplus - (reserve - current)
	if ceiling < 800 {
		t.Errorf("ceiling = %.0f W — battery should get ≥ ~1000 W of the 3 kW surplus (was 0 pre-fix)", ceiling)
	}
}
