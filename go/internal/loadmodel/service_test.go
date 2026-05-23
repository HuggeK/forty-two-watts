package loadmodel

import (
	"math"
	"testing"
	"time"

	"github.com/frahlg/forty-two-watts/go/internal/telemetry"
)

func TestResetPreservesHeatingCoefficient(t *testing.T) {
	s := NewService(nil, telemetry.NewStore(), "site", 4000)
	s.SetHeatingCoef(275)

	s.Reset()

	if got := s.Model().HeatingW_per_degC; got != 275 {
		t.Fatalf("heating coefficient after reset = %v, want 275", got)
	}
}

func TestSampleRequiresOnlineSiteMeter(t *testing.T) {
	tel := telemetry.NewStore()
	tel.Update("site", telemetry.DerMeter, 1000, nil, nil)

	s := NewService(nil, tel, "site", 4000)
	s.sampleAt(time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC))

	if got := s.Model().Samples; got != 0 {
		t.Fatalf("samples = %d, want 0 when site meter has no online health", got)
	}
}

func TestSampleUsesOnlyOnlineDERsAndSubtractsEV(t *testing.T) {
	tel := telemetry.NewStore()
	tel.Update("site", telemetry.DerMeter, 1000, nil, nil)
	tel.RecordDriverSuccess("site")

	tel.Update("pv-offline", telemetry.DerPV, -700, nil, nil)
	tel.DriverHealthMut("pv-offline").SetOffline()
	tel.Update("bat-offline", telemetry.DerBattery, -200, nil, nil)
	tel.DriverHealthMut("bat-offline").SetOffline()

	tel.Update("charger", telemetry.DerEV, 300, nil, nil)
	tel.RecordDriverSuccess("charger")

	s := NewService(nil, tel, "site", 4000)
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	s.sampleAt(now)

	m := s.Model()
	if m.Samples != 1 {
		t.Fatalf("samples = %d, want 1", m.Samples)
	}
	got := m.Bucket[HourOfWeek(now)].Mean
	if math.Abs(got-700) > 1 {
		t.Fatalf("bucket mean = %.1f, want house load 700 W", got)
	}
}
