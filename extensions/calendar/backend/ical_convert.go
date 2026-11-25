package backend

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// ParsedObject is the result of parsing one CalendarObject's ICS data. It
// holds the master event (the VEVENT without RECURRENCE-ID, or the only
// VEVENT for non-recurring objects) plus any RECURRENCE-ID exceptions
// found alongside it. The sync layer fills in calendar_id / etag / href /
// uuids before persisting.
type ParsedObject struct {
	Master    Event
	Overrides []EventOverride
}

// ParseCalendarObject parses an ICS blob (one CalendarObject's full data,
// usually a VCALENDAR wrapping one or more VEVENTs that share a UID).
// Returns the master event with all fields except ID, CalendarID, ETag,
// Href populated. The master's ICSBlob is set to the input string for
// re-parse during recurrence expansion.
//
// Mapping notes:
//   - Master event: the VEVENT without RECURRENCE-ID. If multiple, picks
//     the first; CalDAV servers should not produce ambiguous objects.
//   - All-day detection: DTSTART with VALUE=DATE parameter.
//   - DTEnd fallback: if no DTEND, treat as DTSTART+0 (per RFC 5545, DTEND
//     is optional; without it, the event is "instantaneous"). DURATION
//     would be a more complete handling — deferred until users hit it.
//   - TZName: extracted from DTSTART's TZID parameter, or empty for
//     floating / UTC times.
//   - Floating local times (no TZID, no Z suffix): parsed as time.Local.
//     The Unix timestamp captures the instant for the user's current
//     locale at parse time; if the user's tz changes later, floating
//     events would display at a different absolute time. Documented as
//     a known limitation.
func ParseCalendarObject(rawICS string) (*ParsedObject, error) {
	if rawICS == "" {
		return nil, fmt.Errorf("ical: empty ICS data")
	}

	dec := ical.NewDecoder(strings.NewReader(rawICS))
	cal, err := dec.Decode()
	if err != nil {
		return nil, fmt.Errorf("ical decode: %w", err)
	}
	normalizeTZIDs(cal.Component)

	events := cal.Events()
	if len(events) == 0 {
		return nil, fmt.Errorf("ical: no VEVENT found in ICS data")
	}

	// Find the master (no RECURRENCE-ID) + collect overrides.
	var master *ical.Event
	var overrideEvents []ical.Event
	for i := range events {
		ev := events[i]
		if ev.Props.Get(ical.PropRecurrenceID) == nil {
			if master == nil {
				master = &ev
				continue
			}
			// Multiple VEVENTs without RECURRENCE-ID — pick the first,
			// drop the rest. CalDAV servers shouldn't produce these.
			continue
		}
		overrideEvents = append(overrideEvents, ev)
	}

	if master == nil {
		// All events have RECURRENCE-ID — unusual but possible (server
		// sent only the override). Treat the first as the master.
		first := events[0]
		master = &first
		overrideEvents = nil
	}

	masterEvent, err := buildEvent(master, rawICS)
	if err != nil {
		return nil, fmt.Errorf("build master event: %w", err)
	}

	overrides := make([]EventOverride, 0, len(overrideEvents))
	for i := range overrideEvents {
		ov, err := buildOverride(&overrideEvents[i])
		if err != nil {
			// Skip malformed overrides rather than fail the whole parse;
			// they're additive metadata, not the master event itself.
			continue
		}
		overrides = append(overrides, ov)
	}

	return &ParsedObject{
		Master:    masterEvent,
		Overrides: overrides,
	}, nil
}

func buildEvent(ev *ical.Event, rawICS string) (Event, error) {
	uid := propText(ev, ical.PropUID)
	if uid == "" {
		return Event{}, fmt.Errorf("VEVENT missing UID")
	}

	dtstartProp := ev.Props.Get(ical.PropDateTimeStart)
	if dtstartProp == nil {
		return Event{}, fmt.Errorf("VEVENT missing DTSTART")
	}

	isAllDay := dtstartProp.Params.Get(ical.ParamValue) == string(ical.ValueDate)
	tzName := dtstartProp.Params.Get(ical.ParamTimezoneID)

	// tz-less values (all-day VALUE=DATE, floating no-TZID) are interpreted in
	// the user's configured display tz so they bucket on the right day; an
	// explicit TZID still wins.
	loc := configuredTZ()
	if tzName != "" {
		if l, err := time.LoadLocation(tzName); err == nil {
			loc = l
		}
	}

	dtstart, err := ev.DateTimeStart(loc)
	if err != nil {
		return Event{}, fmt.Errorf("parse DTSTART: %w", err)
	}

	dtend, err := ev.DateTimeEnd(loc)
	if err != nil {
		// No DTEND / DURATION → treat as instantaneous (same as DTSTART).
		dtend = dtstart
	}

	rrule := propText(ev, ical.PropRecurrenceRule)
	// go-ical exposes the raw RRULE value (e.g., "FREQ=WEEKLY;BYDAY=MO").
	// We store the property line including the prefix so re-parse via
	// RecurrenceSet on the master Component works. The raw rrule_text
	// column is purely a denormalized hint for "is this recurring?"
	// queries (NULL = non-recurring).

	return Event{
		UID:               uid,
		Summary:           propText(ev, ical.PropSummary),
		Description:       propText(ev, ical.PropDescription),
		Location:          propText(ev, ical.PropLocation),
		DTStartUnix:       dtstart.Unix(),
		DTEndUnix:         dtend.Unix(),
		IsAllDay:          isAllDay,
		TZName:            tzName,
		RRuleText:         rrule,
		Transparency:      transparencyFromICS(propText(ev, icsPropTransp)),
		Visibility:        visibilityFromICS(propText(ev, icsPropClass)),
		Status:            strings.ToUpper(propText(ev, ical.PropStatus)),
		Sequence:          parseSequence(propText(ev, ical.PropSequence)),
		Categories:        parseCategories(ev),
		ExceptionDates:    parseExceptionDates(ev, loc),
		AlarmFingerprints: alarmFingerprintsFromBlob(rawICS),
		ICSBlob:           rawICS,
		Attendees:         parseAttendeesFromVEVENT(ev),
		Organizer:         parseOrganizerFromVEVENT(ev),
	}, nil
}

// parseSequence parses an ICS SEQUENCE value. Malformed values read as 0;
// the raw SEQUENCE line is still covered by canonicalMasterFingerprint,
// so a garbled-but-changed counter still counts as a change.
func parseSequence(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// parseCategories collects every CATEGORIES property (each a comma-separated
// TEXT list), trims, dedupes, and sorts the result so wire order does not
// count as a change.
func parseCategories(ev *ical.Event) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range ev.Props[ical.PropCategories] {
		list, err := p.TextList()
		if err != nil {
			list = strings.Split(p.Value, ",")
		}
		for _, c := range list {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// parseExceptionDates collects every EXDATE instant across all EXDATE
// properties. Each property value may be a comma-separated list; a TZID
// parameter on the property wins for floating values, else the event's
// location. Unparseable tokens are skipped — the raw EXDATE line is still
// covered by canonicalMasterFingerprint.
func parseExceptionDates(ev *ical.Event, loc *time.Location) []int64 {
	seen := make(map[int64]struct{})
	var out []int64
	for _, p := range ev.Props[ical.PropExceptionDates] {
		ploc := loc
		if tzid := p.Params.Get(ical.ParamTimezoneID); tzid != "" {
			if l, err := time.LoadLocation(tzid); err == nil {
				ploc = l
			}
		}
		for _, tok := range strings.Split(p.Value, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			t, err := parseICSDateTime(tok, ploc)
			if err != nil {
				continue
			}
			u := t.Unix()
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// alarmFingerprintsFromBlob returns one canonical string per VALARM on the
// master VEVENT (ACTION + trigger + description, sorted). A parse failure
// yields nil — the VALARM children are still covered by
// canonicalMasterFingerprint, so an alarm change is never silently missed.
func alarmFingerprintsFromBlob(icsBlob string) []string {
	tmpls, err := parseAlarmTemplates(icsBlob)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(tmpls))
	for _, t := range tmpls {
		var trig string
		if t.absoluteUnix != 0 {
			trig = fmt.Sprintf("abs:%d", t.absoluteUnix)
		} else {
			rel := "START"
			if t.relatedToEnd {
				rel = "END"
			}
			trig = fmt.Sprintf("rel:%d:%s", t.relativeSeconds, rel)
		}
		out = append(out, strings.ToUpper(t.action)+"|"+trig+"|"+t.description)
	}
	sort.Strings(out)
	return out
}

// volatileICSProps are VEVENT properties a server rewrites on its own on
// every store (timestamps) without any user-visible edit. They are excluded
// from canonicalMasterFingerprint so a server-side rewrite does not read
// as a concurrent edit.
var volatileICSProps = map[string]bool{
	"DTSTAMP":       true,
	"CREATED":       true,
	"LAST-MODIFIED": true,
}

// fingerprintExcludedProps are VEVENT properties already compared field by
// field (with normalisation) by sameCalendarSubstance. Excluding them from
// the canonical fingerprint keeps the backstop from re-imposing strictness
// the field comparison deliberately relaxes (e.g. CATEGORIES wire order,
// EXDATE representations of the same instant).
var fingerprintExcludedProps = map[string]bool{
	"UID":         true,
	"DTSTART":     true,
	"DTEND":       true,
	"SUMMARY":     true,
	"DESCRIPTION": true,
	"LOCATION":    true,
	"RRULE":       true,
	"TRANSP":      true,
	"CLASS":       true,
	"STATUS":      true,
	"SEQUENCE":    true,
	"ORGANIZER":   true,
	"ATTENDEE":    true,
	"CATEGORIES":  true,
	"EXDATE":      true,
}

// canonicalMasterFingerprint renders the master VEVENT's remaining
// properties (everything not compared field-wise and not volatile — RDATE,
// DURATION, X-ALT-DESC, X-* vendor props, COMMENT, URL, PRIORITY, ...)
// plus its VALARM children, in a canonical sorted form. It is the backstop
// in sameCalendarSubstance: any concurrent change to a property the parser
// does not model still counts as a change, while formatting differences
// (line folding, property order, DTSTAMP rewrites) do not.
//
// Returns "" when the blob cannot be decoded — callers treat that as
// "cannot prove unchanged".
func canonicalMasterFingerprint(po *ParsedObject) string {
	if po == nil || po.Master.ICSBlob == "" {
		return ""
	}
	dec := ical.NewDecoder(strings.NewReader(po.Master.ICSBlob))
	cal, err := dec.Decode()
	if err != nil {
		return ""
	}
	events := cal.Events()
	var master *ical.Event
	for i := range events {
		if events[i].Props.Get(ical.PropRecurrenceID) == nil {
			master = &events[i]
			break
		}
	}
	if master == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("VEVENT\n")
	b.WriteString(canonicalProps(master.Component.Props))
	for _, child := range master.Component.Children {
		if child.Name != ical.CompAlarm {
			continue
		}
		b.WriteString("VALARM{\n")
		b.WriteString(canonicalProps(child.Props))
		b.WriteString("}\n")
	}
	return b.String()
}

// canonicalProps renders a property set as sorted "NAME;params:value" lines,
// skipping volatile and field-compared properties. Param pairs are sorted
// so param order on the wire does not count as a change.
func canonicalProps(props ical.Props) string {
	var lines []string
	for name, plist := range props {
		if volatileICSProps[name] || fingerprintExcludedProps[name] {
			continue
		}
		for _, p := range plist {
			var params []string
			for k, vs := range p.Params {
				for _, v := range vs {
					params = append(params, k+"="+v)
				}
			}
			sort.Strings(params)
			line := name
			if len(params) > 0 {
				line += ";" + strings.Join(params, ";")
			}
			lines = append(lines, line+":"+p.Value)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func buildOverride(ev *ical.Event) (EventOverride, error) {
	recProp := ev.Props.Get(ical.PropRecurrenceID)
	if recProp == nil {
		return EventOverride{}, fmt.Errorf("override missing RECURRENCE-ID")
	}

	tzName := recProp.Params.Get(ical.ParamTimezoneID)
	loc := configuredTZ() // tz-less RECURRENCE-ID → configured display tz
	if tzName != "" {
		if l, err := time.LoadLocation(tzName); err == nil {
			loc = l
		}
	}

	recTime, err := recProp.DateTime(loc)
	if err != nil {
		return EventOverride{}, fmt.Errorf("parse RECURRENCE-ID: %w", err)
	}

	// Re-encode just this override VEVENT into its own ICS blob so the
	// expander can read it standalone.
	wrapper := ical.NewCalendar()
	wrapper.Props.SetText(ical.PropVersion, "2.0")
	wrapper.Props.SetText(ical.PropProductID, "-//Daak//Calendar//EN")
	wrapper.Children = append(wrapper.Children, ev.Component)

	var sb strings.Builder
	enc := ical.NewEncoder(&sb)
	if err := enc.Encode(wrapper); err != nil {
		return EventOverride{}, fmt.Errorf("encode override: %w", err)
	}

	return EventOverride{
		RecurrenceIDUnix: recTime.Unix(),
		ICSBlob:          sb.String(),
	}, nil
}

// propText returns the trimmed raw Value of a property, or "" when absent.
// Raw (not iCal-unescaped) on purpose: callers include RRULE, whose commas are
// RECUR part-separators — Prop.Text() would comma-split them. For human-facing
// TEXT bodies (DESCRIPTION, X-ALT-DESC) use propTextDecoded instead.
func propText(ev *ical.Event, name string) string {
	p := ev.Props.Get(name)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.Value)
}

// propTextDecoded returns a TEXT property iCal-unescaped (\n \, \; \\ → literal)
// via Prop.Text(). Use only for genuine TEXT properties — NOT RRULE/RECUR
// values, where Text()'s comma-splitting would corrupt the value.
func propTextDecoded(ev *ical.Event, name string) string {
	p := ev.Props.Get(name)
	if p == nil {
		return ""
	}
	if t, err := p.Text(); err == nil {
		return strings.TrimSpace(t)
	}
	return strings.TrimSpace(p.Value)
}

// unescapeICalText decodes RFC 5545 TEXT escapes in a raw property value:
// \n and \N → newline; \\ \, \; → literal backslash/comma/semicolon. Applied
// when a stored body is rendered, so DESCRIPTIONs that were persisted raw
// (any provider) display with real line breaks instead of literal "\n".
//
// Deliberately NOT go-ical's Prop.Text(): that splits on unescaped commas and
// returns only the first segment (silently truncating a body) and errors on
// any unknown escape. This preserves unknown escapes (backslash + char) and
// never truncates. Idempotent on already-decoded text (no backslash → returned
// unchanged), so it's safe to run on bodies from every sync path.
func unescapeICalText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ',', ';':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
