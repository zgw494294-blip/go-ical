package xcal

import (
	"fmt"
	"strings"

	"github.com/emersion/go-ical"
)

// recurPartInfo describes one RECUR rule part and its xCal element.
type recurPartInfo struct {
	icalName string
	xmlName  string
}

// Singleton parts and list parts, in the order mandated by the RFC
// 6321 Appendix A schema.
var (
	recurSingles = []recurPartInfo{
		{"FREQ", "freq"},
		{"UNTIL", "until"},
		{"COUNT", "count"},
		{"INTERVAL", "interval"},
	}
	recurLists = []recurPartInfo{
		{"BYSECOND", "bysecond"},
		{"BYMINUTE", "byminute"},
		{"BYHOUR", "byhour"},
		{"BYDAY", "byday"},
		{"BYMONTHDAY", "bymonthday"},
		{"BYYEARDAY", "byyearday"},
		{"BYWEEKNO", "byweekno"},
		{"BYMONTH", "bymonth"},
		{"BYSETPOS", "bysetpos"},
	}
	recurWKST = recurPartInfo{"WKST", "wkst"}
)

var recurFreqs = map[string]bool{
	"SECONDLY": true,
	"MINUTELY": true,
	"HOURLY":   true,
	"DAILY":    true,
	"WEEKLY":   true,
	"MONTHLY":  true,
	"YEARLY":   true,
}

func isWeekday(s string) bool {
	return reWeekday.MatchString(strings.ToUpper(s))
}

func isByDayToken(s string) bool {
	u := strings.ToUpper(s)
	return reWeekday.MatchString(u) || reByDay.MatchString(u)
}

func recurListPart(xmlName string) (string, bool) {
	for _, p := range recurLists {
		if p.xmlName == xmlName {
			return p.icalName, true
		}
	}
	return "", false
}

// encodeRecurProperty parses an iCalendar RECUR value and emits its
// structured xCal form. Rule parts are emitted in the schema order.
func (e *Encoder) encodeRecurProperty(value string) error {
	seen := make(map[string]string)
	lists := make(map[string][]string)
	for _, part := range strings.Split(value, ";") {
		eq := strings.IndexByte(part, '=')
		if eq <= 0 || eq == len(part)-1 {
			return fmt.Errorf("xcal: malformed recurrence rule part: %q", part)
		}
		name := strings.ToUpper(part[:eq])
		v := part[eq+1:]
		switch name {
		case "FREQ":
			if _, ok := seen[name]; ok {
				return fmt.Errorf("xcal: duplicate recurrence rule part %q", name)
			}
			if !recurFreqs[strings.ToUpper(v)] {
				return &TypeError{Value: v, Type: ical.ValueRecurrence, msg: "invalid FREQ value"}
			}
			seen[name] = v
		case "UNTIL":
			if _, ok := seen[name]; ok {
				return fmt.Errorf("xcal: duplicate recurrence rule part %q", name)
			}
			if _, err := convertDateTimeICAL(v); err == nil {
				seen["UNTIL"] = v
				continue
			}
			if _, err := convertDateICAL(v); err != nil {
				return &TypeError{Value: v, Type: ical.ValueRecurrence, msg: "invalid UNTIL value"}
			}
			seen["UNTIL"] = v
		case "COUNT", "INTERVAL":
			if _, ok := seen[name]; ok {
				return fmt.Errorf("xcal: duplicate recurrence rule part %q", name)
			}
			if err := validateInteger(v); err != nil {
				return fmt.Errorf("xcal: invalid %s value: %w", name, err)
			}
			seen[name] = v
		case "WKST":
			if _, ok := seen[name]; ok {
				return fmt.Errorf("xcal: duplicate recurrence rule part %q", name)
			}
			if !isWeekday(v) {
				return &TypeError{Value: v, Type: ical.ValueRecurrence, msg: "invalid WKST value"}
			}
			seen[name] = strings.ToUpper(v)
		default:
			info := recurInfoByICALName(name)
			if info == nil {
				return fmt.Errorf("xcal: unrecognized recurrence rule part %q", name)
			}
			if _, dup := seen[info.icalName]; dup || lists[info.icalName] != nil {
				return fmt.Errorf("xcal: duplicate recurrence rule part %q", name)
			}
			items := strings.Split(v, ",")
			for _, item := range items {
				if item == "" {
					return fmt.Errorf("xcal: empty %s value", name)
				}
				if name == "BYDAY" {
					if !isByDayToken(item) {
						return &TypeError{Value: item, Type: ical.ValueRecurrence, msg: "invalid BYDAY value"}
					}
				} else {
					if err := validateInteger(item); err != nil {
						return fmt.Errorf("xcal: invalid %s value: %w", name, err)
					}
				}
			}
			if name == "BYDAY" {
				for i := range items {
					items[i] = strings.ToUpper(items[i])
				}
			}
			lists[info.icalName] = items
		}
	}

	freq, ok := seen["FREQ"]
	if !ok {
		return fmt.Errorf("xcal: recurrence rule is missing FREQ")
	}
	_, hasUntil := seen["UNTIL"]
	_, hasCount := seen["COUNT"]
	if hasUntil && hasCount {
		return fmt.Errorf("xcal: recurrence rule cannot contain both UNTIL and COUNT")
	}

	if err := e.startElement("recur"); err != nil {
		return err
	}

	emitSimple := func(xmlName, text string) error {
		return e.elementWithText(xmlName, text)
	}

	if err := emitSimple("freq", freq); err != nil {
		return err
	}
	if v, ok := seen["UNTIL"]; ok {
		if err := e.startElement("until"); err != nil {
			return err
		}
		if strings.ContainsRune(v, 'T') {
			conv, err := convertDateTimeICAL(v)
			if err != nil {
				return err
			}
			if err := e.elementWithText("date-time", conv); err != nil {
				return err
			}
		} else {
			conv, err := convertDateICAL(v)
			if err != nil {
				return err
			}
			if err := e.elementWithText("date", conv); err != nil {
				return err
			}
		}
		if err := e.endElement("until"); err != nil {
			return err
		}
	}
	if v, ok := seen["COUNT"]; ok {
		if err := emitSimple("count", v); err != nil {
			return err
		}
	}
	if v, ok := seen["INTERVAL"]; ok {
		if err := emitSimple("interval", v); err != nil {
			return err
		}
	}
	for _, info := range recurLists {
		for _, item := range lists[info.icalName] {
			if err := emitSimple(info.xmlName, item); err != nil {
				return err
			}
		}
	}
	if v, ok := seen["WKST"]; ok {
		if err := emitSimple("wkst", v); err != nil {
			return err
		}
	}
	return e.endElement("recur")
}

func recurInfoByICALName(name string) *recurPartInfo {
	for i := range recurLists {
		if recurLists[i].icalName == name {
			return &recurLists[i]
		}
	}
	return nil
}

// recurBuilder assembles an iCalendar RECUR value from xCal child
// elements, preserving the order in which parts appear and the
// individual BY values.
type recurBuilder struct {
	order    []string
	singles  map[string]string
	lists    map[string][]string
	untilSet bool
}

func newRecurBuilder() *recurBuilder {
	return &recurBuilder{
		singles: make(map[string]string),
		lists:   make(map[string][]string),
	}
}

func (b *recurBuilder) add(name, value string) error {
	if _, ok := b.singles[name]; ok {
		return fmt.Errorf("xcal: duplicate recurrence rule part %q", name)
	}
	b.singles[name] = value
	b.order = append(b.order, name)
	return nil
}

func (b *recurBuilder) setUntil(value string) error {
	if b.untilSet {
		return fmt.Errorf("xcal: duplicate recurrence rule part %q", "UNTIL")
	}
	b.untilSet = true
	b.singles["UNTIL"] = value
	b.order = append(b.order, "UNTIL")
	return nil
}

func (b *recurBuilder) addListItem(icalName, value string) {
	if _, ok := b.lists[icalName]; !ok {
		b.order = append(b.order, icalName)
	}
	b.lists[icalName] = append(b.lists[icalName], value)
}

func (b *recurBuilder) build() (string, error) {
	freq, ok := b.singles["FREQ"]
	if !ok {
		return "", fmt.Errorf("xcal: recurrence rule is missing FREQ")
	}
	if !recurFreqs[strings.ToUpper(freq)] {
		return "", &TypeError{Value: freq, Type: ical.ValueRecurrence, msg: "invalid FREQ value"}
	}
	if _, hasUntil := b.singles["UNTIL"]; hasUntil {
		if _, hasCount := b.singles["COUNT"]; hasCount {
			return "", fmt.Errorf("xcal: recurrence rule cannot contain both UNTIL and COUNT")
		}
	}

	var sb strings.Builder
	writePart := func(name, value string) {
		if sb.Len() > 0 {
			sb.WriteByte(';')
		}
		sb.WriteString(name)
		sb.WriteByte('=')
		sb.WriteString(value)
	}
	seen := make(map[string]bool)
	for _, name := range b.order {
		switch name {
		case "FREQ", "UNTIL", "COUNT", "INTERVAL", "WKST":
			if seen[name] {
				continue
			}
			seen[name] = true
			writePart(name, b.singles[name])
		default:
			if seen[name] {
				continue
			}
			seen[name] = true
			writePart(name, strings.Join(b.lists[name], ","))
		}
	}
	return sb.String(), nil
}
