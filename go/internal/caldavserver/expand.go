package caldavserver

import (
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

// findExpand walks a CalendarCompRequest tree for a CALDAV:expand directive.
// Clients nest it under the VEVENT comp request, so a plain top-level check
// isn't enough. Returns nil when the client did not request expansion.
//
// NB: go-webdav v0.7's REPORT handler does not decode the <C:expand> element
// into the backend query (it only surfaces the comp-filter), so in practice the
// expansion window comes from filterTimeRange below. This is kept for forward
// compatibility should a future go-webdav start passing it through.
func findExpand(req caldav.CalendarCompRequest) *caldav.CalendarExpandRequest {
	if req.Expand != nil {
		return req.Expand
	}
	for i := range req.Comps {
		if e := findExpand(req.Comps[i]); e != nil {
			return e
		}
	}
	return nil
}

// filterTimeRange returns the [start, end] window carried by a calendar-query's
// (VEVENT) comp-filter time-range, if any. This is how the requested window
// actually reaches the backend in go-webdav v0.7 — the client sends the same
// window on both the comp-filter and the (dropped) expand element. Only returns
// ok when both bounds are present, so an open-ended query keeps its masters.
func filterTimeRange(cf caldav.CompFilter) (time.Time, time.Time, bool) {
	for i := range cf.Comps {
		if s, e, ok := filterTimeRange(cf.Comps[i]); ok {
			return s, e, ok
		}
	}
	if !cf.Start.IsZero() && !cf.End.IsZero() {
		return cf.Start, cf.End, true
	}
	return time.Time{}, time.Time{}, false
}

// expandObjects implements RFC 4791 CALDAV:expand. Every recurring VEVENT in a
// resource is replaced by the concrete instances whose start falls inside
// [start, end], each carrying its own RECURRENCE-ID and stripped of
// RRULE/RDATE/EXDATE. Non-recurring components pass through unchanged. A
// resource left with no in-range component after expansion is dropped.
//
// caldav.Filter has already kept only resources with at least one instance in
// range (it evaluates the recurrence set for the time-range match), so this
// only ever expands events that genuinely have occurrences in the window.
func expandObjects(objs []caldav.CalendarObject, start, end time.Time) []caldav.CalendarObject {
	out := make([]caldav.CalendarObject, 0, len(objs))
	for _, co := range objs {
		if co.Data == nil {
			out = append(out, co)
			continue
		}
		expanded := expandCalendar(co.Data, start, end)
		if expanded == nil {
			continue
		}
		co.Data = expanded
		out = append(out, co)
	}
	return out
}

// expandCalendar returns a copy of cal with every recurring VEVENT expanded
// into its per-occurrence instances within [start, end]. Non-event components
// (e.g. VTIMEZONE) and non-recurring events are preserved verbatim. Returns nil
// when no component remains.
func expandCalendar(cal *ical.Calendar, start, end time.Time) *ical.Calendar {
	loc := start.Location()
	if loc == nil {
		loc = time.UTC
	}
	out := ical.NewCalendar()
	for name, props := range cal.Props {
		out.Props[name] = append([]ical.Prop(nil), props...)
	}
	for _, child := range cal.Children {
		if child.Name != ical.CompEvent {
			out.Children = append(out.Children, child)
			continue
		}
		rset, err := child.RecurrenceSet(loc)
		if err != nil || rset == nil {
			// Non-recurring (or an RRULE we can't parse): leave it as-is rather
			// than silently dropping a real event.
			out.Children = append(out.Children, child)
			continue
		}
		ev := ical.Event{Component: child}
		st0, errS := ev.DateTimeStart(loc)
		en0, errE := ev.DateTimeEnd(loc)
		var dur time.Duration
		if errS == nil && errE == nil && en0.After(st0) {
			dur = en0.Sub(st0)
		}
		allDay := false
		if p := child.Props.Get(ical.PropDateTimeStart); p != nil && p.ValueType() == ical.ValueDate {
			allDay = true
		}
		for _, occ := range rset.Between(start, end, true) {
			occ = occ.In(loc)
			inst := cloneComponent(child)
			inst.Props.Del(ical.PropRecurrenceRule)
			inst.Props.Del(ical.PropRecurrenceDates)
			inst.Props.Del(ical.PropExceptionDates)
			if allDay {
				inst.Props.SetDate(ical.PropDateTimeStart, occ)
				inst.Props.SetDate(ical.PropRecurrenceID, occ)
				if dur > 0 {
					inst.Props.SetDate(ical.PropDateTimeEnd, occ.Add(dur))
				}
			} else {
				inst.Props.SetDateTime(ical.PropDateTimeStart, occ)
				inst.Props.SetDateTime(ical.PropRecurrenceID, occ)
				if dur > 0 {
					inst.Props.SetDateTime(ical.PropDateTimeEnd, occ.Add(dur))
				}
			}
			out.Children = append(out.Children, inst)
		}
	}
	if len(out.Children) == 0 {
		return nil
	}
	return out
}

// cloneComponent deep-copies a component's property slices (so per-instance
// edits never touch the stored master) and shallow-copies its children, which
// the expander only reads.
func cloneComponent(c *ical.Component) *ical.Component {
	nc := ical.NewComponent(c.Name)
	for name, props := range c.Props {
		nc.Props[name] = append([]ical.Prop(nil), props...)
	}
	nc.Children = append(nc.Children, c.Children...)
	return nc
}
