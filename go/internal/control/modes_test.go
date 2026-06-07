package control

import "testing"

// TestAllModesCoversPlannerModes is the regression guard for the Home
// Assistant "Invalid option for select" bug: the mode state topic can emit
// any planner_* mode (they are the default UI choice), and the HA discovery
// `select` options derive from AllModes. If a planner mode ever drops out of
// AllModes, HA rejects the published state again. See go/internal/ha/bridge.go.
func TestAllModesCoversPlannerModes(t *testing.T) {
	want := []Mode{
		ModeIdle, ModeSelfConsumption, ModePeakShaving,
		ModeCharge, ModePriority, ModeWeighted,
		ModePlannerSelf, ModePlannerCheap,
		ModePlannerPassiveArbitrage, ModePlannerArbitrage,
	}
	got := AllModes()
	if len(got) != len(want) {
		t.Fatalf("AllModes() returned %d modes, want %d: %v", len(got), len(want), got)
	}
	for i, m := range want {
		if got[i] != m {
			t.Errorf("AllModes()[%d] = %q, want %q", i, got[i], m)
		}
	}
}

// TestIsValidModeAgreesWithAllModes locks the validator to the canonical
// list so the API mode setter and the HA bridge can't drift from each other.
func TestIsValidModeAgreesWithAllModes(t *testing.T) {
	for _, m := range AllModes() {
		if !IsValidMode(m) {
			t.Errorf("IsValidMode(%q) = false, want true (mode is in AllModes)", m)
		}
	}
	for _, bad := range []Mode{"", "planner", "self", "arbitrage", "PLANNER_ARBITRAGE"} {
		if IsValidMode(bad) {
			t.Errorf("IsValidMode(%q) = true, want false", bad)
		}
	}
}

// TestEveryPlannerModeIsValid guards the specific failure from the field
// report: planner_arbitrage published as state must be a recognized mode.
func TestEveryPlannerModeIsValid(t *testing.T) {
	for _, m := range AllModes() {
		if !m.IsPlannerMode() {
			continue
		}
		if !IsValidMode(m) {
			t.Errorf("planner mode %q is not valid per IsValidMode", m)
		}
	}
}
