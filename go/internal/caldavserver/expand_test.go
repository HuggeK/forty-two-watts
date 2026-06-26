package caldavserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	webdav "github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
)

// expandQuery is a calendar-query REPORT carrying a VEVENT time-range — the
// shape 42W's calendar client sends — which the backend uses as the recurrence
// expansion window.
func expandQuery(start, end time.Time) *caldav.CalendarQuery {
	return &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name: "VCALENDAR",
			Comps: []caldav.CalendarCompRequest{{
				Name:     "VEVENT",
				AllProps: true,
				Expand:   &caldav.CalendarExpandRequest{Start: start, End: end},
			}},
		},
		CompFilter: caldav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start, End: end}},
		},
	}
}

// TestNativeServerExpandsRecurrence proves the gap that used to require an
// external CalDAV server is closed: a daily-recurring event is returned as one
// concrete instance per occurrence in the queried window — each with a
// RECURRENCE-ID and no RRULE — rather than just its master VEVENT.
func TestNativeServerExpandsRecurrence(t *testing.T) {
	srv := httptest.NewServer(testHandler("u", "p", "/u/", []string{"/u/energy/"}))
	defer srv.Close()
	hc := webdav.HTTPClientWithBasicAuth(http.DefaultClient, "u", "p")
	c, err := caldav.NewClient(hc, srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	// A daily-recurring 1 h "Away" event anchored at a fixed instant so the test
	// is independent of the wall clock (the window below is explicit).
	anchor := time.Date(2026, time.June, 1, 9, 0, 0, 0, time.UTC)
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//ftw-test//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, "away-daily")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, anchor)
	ev.Props.SetDateTime(ical.PropDateTimeStart, anchor)
	ev.Props.SetDateTime(ical.PropDateTimeEnd, anchor.Add(time.Hour))
	ev.Props.SetText(ical.PropSummary, "Away — daily")
	// RRULE must keep its default RECUR value type — SetText would tag it
	// VALUE=TEXT and break parsing, which real calendar apps never do.
	ev.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Value: "FREQ=DAILY;COUNT=10"})
	cal.Children = append(cal.Children, ev.Component)
	if _, err := c.PutCalendarObject(context.Background(), "/u/energy/away.ics", cal); err != nil {
		t.Fatalf("PUT: %v", err)
	}

	// A window covering Jun 1, 2, 3 (ending just before the Jun 4 occurrence).
	start := anchor.Add(-time.Hour)
	end := anchor.Add(3*24*time.Hour - time.Minute)
	objs, err := c.QueryCalendar(context.Background(), "/u/energy/", expandQuery(start, end))
	if err != nil {
		t.Fatalf("REPORT: %v", err)
	}

	instances := 0
	for _, o := range objs {
		if o.Data == nil {
			continue
		}
		for _, e := range o.Data.Events() {
			instances++
			if rr, _ := e.Props.Text(ical.PropRecurrenceRule); rr != "" {
				t.Fatalf("expanded instance must not carry an RRULE, got %q", rr)
			}
			if rid := e.Props.Get(ical.PropRecurrenceID); rid == nil {
				t.Fatalf("expanded instance must carry a RECURRENCE-ID")
			}
		}
	}
	if instances != 3 {
		t.Fatalf("expected 3 expanded instances in the 3-day window, got %d", instances)
	}
}

// TestExpandCalendarUnit exercises the pure expander without the HTTP layer:
// a non-recurring event passes through untouched; a recurring one fans out.
func TestExpandCalendarUnit(t *testing.T) {
	anchor := time.Date(2026, time.June, 1, 9, 0, 0, 0, time.UTC)
	mk := func(rrule string) *ical.Calendar {
		cal := ical.NewCalendar()
		ev := ical.NewEvent()
		ev.Props.SetText(ical.PropUID, "x")
		ev.Props.SetDateTime(ical.PropDateTimeStart, anchor)
		ev.Props.SetDateTime(ical.PropDateTimeEnd, anchor.Add(time.Hour))
		ev.Props.SetText(ical.PropSummary, "x")
		if rrule != "" {
			ev.Props.Set(&ical.Prop{Name: ical.PropRecurrenceRule, Value: rrule})
		}
		cal.Children = append(cal.Children, ev.Component)
		return cal
	}
	start, end := anchor.Add(-time.Hour), anchor.Add(3*24*time.Hour-time.Minute)

	// Non-recurring: returned unchanged (still has exactly one event).
	if got := expandCalendar(mk(""), start, end); got == nil || len(got.Events()) != 1 {
		t.Fatalf("non-recurring event should pass through as 1 event, got %v", got)
	}
	// Recurring daily: 3 instances in the window.
	if got := expandCalendar(mk("FREQ=DAILY;COUNT=10"), start, end); got == nil || len(got.Events()) != 3 {
		n := 0
		if got != nil {
			n = len(got.Events())
		}
		t.Fatalf("daily recurrence should expand to 3 instances, got %d", n)
	}
}
