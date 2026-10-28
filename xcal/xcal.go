// Package xcal implements bidirectional conversion between iCalendar
// data (RFC 5545) and xCal, its XML representation defined in RFC 6321.
//
// The package is opt-in: the iCalendar text codec and public types in
// the parent package are unaffected when this package is not used.
// Conversion works with the existing ical.Calendar, ical.Component,
// ical.Prop and ical.Params data model and preserves the iCalendar
// semantics described in RFC 6321: multiple calendars, nested
// component order, repeated properties, single- and multi-valued
// parameters and values, structured values (GEO, REQUEST-STATUS,
// PERIOD, RECUR), default and explicit VALUE types, ENCODING=BASE64
// pre-processing, extensions and foreign-namespace XML elements.
//
// Round-trips preserve data semantics, not original line folding,
// property ordering, spelling of XML prefixes or byte-for-byte output.
package xcal

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

// Namespace is the iCalendar XML namespace defined in RFC 6321
// section 7.1.
const Namespace = "urn:ietf:params:xml:ns:icalendar-2.0"

// MIMEType is the xCal media type defined in RFC 6321 section 7.2.
const MIMEType = "application/calendar+xml"

// XMLPropertyName is the name of the iCalendar "XML" extension
// property defined in RFC 6321 section 4.2. It is used to carry XML
// elements that are in a namespace other than the iCalendar namespace.
const XMLPropertyName = "XML"

const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

// valueUnknown marks an xCal "unknown" value element (RFC 6321
// section 5). It is distinct from ical.ValueDefault.
const valueUnknown ical.ValueType = "UNKNOWN"

// defaultValueTypes mirrors ical.defaultValueTypes. It is read-only
// and keeps the xcal package decoupled from unexported identifiers in
// the parent package.
var defaultValueTypes = map[string]ical.ValueType{
	ical.PropCalendarScale:      ical.ValueText,
	ical.PropMethod:             ical.ValueText,
	ical.PropProductID:          ical.ValueText,
	ical.PropVersion:            ical.ValueText,
	ical.PropAttach:             ical.ValueURI,
	ical.PropCategories:         ical.ValueText,
	ical.PropClass:              ical.ValueText,
	ical.PropComment:            ical.ValueText,
	ical.PropDescription:        ical.ValueText,
	ical.PropGeo:                ical.ValueFloat,
	ical.PropLocation:           ical.ValueText,
	ical.PropPercentComplete:    ical.ValueInt,
	ical.PropPriority:           ical.ValueInt,
	ical.PropResources:          ical.ValueText,
	ical.PropStatus:             ical.ValueText,
	ical.PropSummary:            ical.ValueText,
	ical.PropCompleted:          ical.ValueDateTime,
	ical.PropDateTimeEnd:        ical.ValueDateTime,
	ical.PropDue:                ical.ValueDateTime,
	ical.PropDateTimeStart:      ical.ValueDateTime,
	ical.PropDuration:           ical.ValueDuration,
	ical.PropFreeBusy:           ical.ValuePeriod,
	ical.PropTransparency:       ical.ValueText,
	ical.PropTimezoneID:         ical.ValueText,
	ical.PropTimezoneName:       ical.ValueText,
	ical.PropTimezoneOffsetFrom: ical.ValueUTCOffset,
	ical.PropTimezoneOffsetTo:   ical.ValueUTCOffset,
	ical.PropTimezoneURL:        ical.ValueURI,
	ical.PropAttendee:           ical.ValueCalendarAddress,
	ical.PropContact:            ical.ValueText,
	ical.PropOrganizer:          ical.ValueCalendarAddress,
	ical.PropRecurrenceID:       ical.ValueDateTime,
	ical.PropRelatedTo:          ical.ValueText,
	ical.PropURL:                ical.ValueURI,
	ical.PropUID:                ical.ValueText,
	ical.PropExceptionDates:     ical.ValueDateTime,
	ical.PropRecurrenceDates:    ical.ValueDateTime,
	ical.PropRecurrenceRule:     ical.ValueRecurrence,
	ical.PropAction:             ical.ValueText,
	ical.PropRepeat:             ical.ValueInt,
	ical.PropTrigger:            ical.ValueDuration,
	ical.PropCreated:            ical.ValueDateTime,
	ical.PropDateTimeStamp:      ical.ValueDateTime,
	ical.PropLastModified:       ical.ValueDateTime,
	ical.PropSequence:           ical.ValueInt,
	ical.PropRequestStatus:      ical.ValueText,
	ical.PropName:               ical.ValueText,
	ical.PropRefreshInterval:    ical.ValueDuration,
	ical.PropSource:             ical.ValueURI,
	ical.PropColor:              ical.ValueText,
	ical.PropImage:              ical.ValueURI,
	ical.PropConference:         ical.ValueURI,
}

// paramValueTypes maps the recognized iCalendar parameters (RFC 6321
// section 3.5 / Appendix A # 3.2) to their xCal value types. Unknown
// parameters use valueUnknown.
var paramValueTypes = map[string]ical.ValueType{
	ical.ParamAltRep:              ical.ValueURI,
	ical.ParamCommonName:          ical.ValueText,
	ical.ParamCalendarUserType:    ical.ValueText,
	ical.ParamDelegatedFrom:       ical.ValueCalendarAddress,
	ical.ParamDelegatedTo:         ical.ValueCalendarAddress,
	ical.ParamDir:                 ical.ValueURI,
	ical.ParamEncoding:            ical.ValueText,
	ical.ParamFormatType:          ical.ValueText,
	ical.ParamFreeBusyType:        ical.ValueText,
	ical.ParamLanguage:            ical.ValueText,
	ical.ParamMember:              ical.ValueCalendarAddress,
	ical.ParamParticipationStatus: ical.ValueText,
	ical.ParamRange:               ical.ValueText,
	ical.ParamRelated:             ical.ValueText,
	ical.ParamRelationshipType:    ical.ValueText,
	ical.ParamRole:                ical.ValueText,
	ical.ParamRSVP:                ical.ValueBool,
	ical.ParamSentBy:              ical.ValueCalendarAddress,
	ical.ParamTimezoneID:          ical.ValueText,
}

// xmlScalarElements maps xCal scalar value element names to iCalendar
// value types.
var xmlScalarElements = map[string]ical.ValueType{
	"binary":      ical.ValueBinary,
	"boolean":     ical.ValueBool,
	"cal-address": ical.ValueCalendarAddress,
	"date":        ical.ValueDate,
	"date-time":   ical.ValueDateTime,
	"duration":    ical.ValueDuration,
	"float":       ical.ValueFloat,
	"integer":     ical.ValueInt,
	"text":        ical.ValueText,
	"time":        ical.ValueTime,
	"uri":         ical.ValueURI,
	"utc-offset":  ical.ValueUTCOffset,
	"unknown":     valueUnknown,
}

func isKnownValueType(t ical.ValueType) bool {
	switch t {
	case ical.ValueBinary,
		ical.ValueBool,
		ical.ValueCalendarAddress,
		ical.ValueDate,
		ical.ValueDateTime,
		ical.ValueDuration,
		ical.ValueFloat,
		ical.ValueInt,
		ical.ValuePeriod,
		ical.ValueRecurrence,
		ical.ValueText,
		ical.ValueTime,
		ical.ValueURI,
		ical.ValueUTCOffset:
		return true
	}
	return false
}

func defaultValueType(propName string) ical.ValueType {
	return defaultValueTypes[propName]
}

// effectiveValueType resolves the value type of a property, applying
// the rules of RFC 6321 section 5: an explicit recognized VALUE
// parameter wins; otherwise the default type of the property is used;
// unknown properties without either resolve to "unknown".
func effectiveValueType(prop *ical.Prop) ical.ValueType {
	if v := strings.ToUpper(prop.Params.Get(ical.ParamValue)); v != "" {
		t := ical.ValueType(v)
		if isKnownValueType(t) {
			return t
		}
		return valueUnknown
	}
	if t := defaultValueType(prop.Name); t != ical.ValueDefault {
		return t
	}
	return valueUnknown
}

var (
	reInteger  = regexp.MustCompile(`^[+-]?[0-9]+$`)
	reDateICAL = regexp.MustCompile(`^[0-9]{8}$`)
	reDateXML  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	reDTICAL   = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z?$`)
	reDTXML    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z?$`)
	reTimeICAL = regexp.MustCompile(`^[0-9]{6}Z?$`)
	reTimeXML  = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}:[0-9]{2}Z?$`)
	reOffICAL  = regexp.MustCompile(`^[+-][0-9]{4}([0-9]{2})?$`)
	reOffXML   = regexp.MustCompile(`^[+-][0-9]{2}:[0-9]{2}(:[0-9]{2})?$`)
	reWeekday  = regexp.MustCompile(`^(SU|MO|TU|WE|TH|FR|SA)$`)
	reByDay    = regexp.MustCompile(`^[+-]?[0-9]{1,2}(SU|MO|TU|WE|TH|FR|SA)$`)
)

const (
	fmtDateICAL = "20060102"
	fmtDateXML  = "2006-01-02"
	fmtDTICAL   = "20060102T150405"
	fmtDTICALZ  = "20060102T150405Z"
	fmtDTXML    = "2006-01-02T15:04:05"
	fmtDTXMLZ   = "2006-01-02T15:04:05Z"
)

func validateInteger(s string) error {
	if !reInteger.MatchString(s) {
		return &TypeError{Value: s, Type: ical.ValueInt}
	}
	return nil
}

func validateFloat(s string) error {
	if strings.ContainsAny(s, " \t\r\n") {
		return &TypeError{Value: s, Type: ical.ValueFloat}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != f { // NaN
		return &TypeError{Value: s, Type: ical.ValueFloat}
	}
	return nil
}

func validateDate(s, layout string) error {
	t, err := time.Parse(layout, s)
	if err != nil || t.Format(layout) != s {
		return &TypeError{Value: s, Type: ical.ValueDate}
	}
	return nil
}

func validateDateTime(s string, xml bool) error {
	layouts := []string{fmtDTICALZ, fmtDTICAL}
	if xml {
		layouts = []string{fmtDTXMLZ, fmtDTXML}
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil && t.Format(l) == s {
			return nil
		}
	}
	return &TypeError{Value: s, Type: ical.ValueDateTime}
}

// convertDateICAL converts an iCalendar date (YYYYMMDD) to xCal
// (YYYY-MM-DD). No timezone or numeric normalization is done.
func convertDateICAL(s string) (string, error) {
	if !reDateICAL.MatchString(s) {
		return "", &TypeError{Value: s, Type: ical.ValueDate}
	}
	if err := validateDate(s, fmtDateICAL); err != nil {
		return "", err
	}
	return s[0:4] + "-" + s[4:6] + "-" + s[6:8], nil
}

func convertDateXML(s string) (string, error) {
	if !reDateXML.MatchString(s) {
		return "", &TypeError{Value: s, Type: ical.ValueDate}
	}
	compact := s[0:4] + s[5:7] + s[8:10]
	if err := validateDate(compact, fmtDateICAL); err != nil {
		return "", err
	}
	return compact, nil
}

// convertDateTimeICAL inserts the date/time separators required by
// RFC 6321 while keeping the value otherwise untouched (a trailing Z
// and any TZID parameter are preserved as-is).
func convertDateTimeICAL(s string) (string, error) {
	if !reDTICAL.MatchString(s) {
		return "", &TypeError{Value: s, Type: ical.ValueDateTime}
	}
	if err := validateDateTime(s, false); err != nil {
		return "", err
	}
	out := s[0:4] + "-" + s[4:6] + "-" + s[6:8] + "T" +
		s[9:11] + ":" + s[11:13] + ":" + s[13:15]
	if len(s) == 16 {
		out += "Z"
	}
	return out, nil
}

func convertDateTimeXML(s string) (string, error) {
	if !reDTXML.MatchString(s) {
		return "", &TypeError{Value: s, Type: ical.ValueDateTime}
	}
	if err := validateDateTime(s, true); err != nil {
		return "", err
	}
	compact := s[0:4] + s[5:7] + s[8:10] + "T" +
		s[11:13] + s[14:16] + s[17:19]
	if len(s) == 20 {
		compact += "Z"
	}
	return compact, nil
}

func validateTimeRanges(hh, mm, ss string) bool {
	h, m, sec := atoiOrNeg(hh), atoiOrNeg(mm), atoiOrNeg(ss)
	return h >= 0 && h <= 23 && m >= 0 && m <= 59 && sec >= 0 && sec <= 60
}

func atoiOrNeg(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func convertTimeICAL(s string) (string, error) {
	if !reTimeICAL.MatchString(s) || !validateTimeRanges(s[0:2], s[2:4], s[4:6]) {
		return "", &TypeError{Value: s, Type: ical.ValueTime}
	}
	out := s[0:2] + ":" + s[2:4] + ":" + s[4:6]
	if strings.HasSuffix(s, "Z") {
		out += "Z"
	}
	return out, nil
}

func convertTimeXML(s string) (string, error) {
	if !reTimeXML.MatchString(s) || !validateTimeRanges(s[0:2], s[3:5], s[6:8]) {
		return "", &TypeError{Value: s, Type: ical.ValueTime}
	}
	compact := s[0:2] + s[3:5] + s[6:8]
	if strings.HasSuffix(s, "Z") {
		compact += "Z"
	}
	return compact, nil
}

func convertUTCOffsetICAL(s string) (string, error) {
	if !reOffICAL.MatchString(s) || !validateOffsetRanges(s[1:3], s[3:5], offsetSecondsICAL(s)) {
		return "", &TypeError{Value: s, Type: ical.ValueUTCOffset}
	}
	out := s[0:3] + ":" + s[3:5]
	if len(s) == 7 {
		out += ":" + s[5:7]
	}
	return out, nil
}

func offsetSecondsICAL(s string) string {
	if len(s) == 7 {
		return s[5:7]
	}
	return "00"
}

func convertUTCOffsetXML(s string) (string, error) {
	seconds := "00"
	if len(s) == 9 {
		seconds = s[7:9]
	}
	if !reOffXML.MatchString(s) || !validateOffsetRanges(s[1:3], s[4:6], seconds) {
		return "", &TypeError{Value: s, Type: ical.ValueUTCOffset}
	}
	compact := s[0:3] + s[4:6]
	if len(s) == 9 {
		compact += s[7:9]
	}
	return compact, nil
}

func validateOffsetRanges(hh, mm, ss string) bool {
	h, m, sec := atoiOrNeg(hh), atoiOrNeg(mm), atoiOrNeg(ss)
	return h >= 0 && h <= 23 && m >= 0 && m <= 59 && sec >= 0 && sec <= 60
}

// validateDuration checks an iCalendar duration value without
// reformatting it: RFC 6321 keeps the iCalendar spelling verbatim.
func validateDuration(s string) error {
	fail := func() error { return &TypeError{Value: s, Type: ical.ValueDuration} }
	p := s
	if len(p) > 0 && (p[0] == '+' || p[0] == '-') {
		p = p[1:]
	}
	if len(p) < 2 || p[0] != 'P' {
		return fail()
	}
	p = p[1:]

	// A week duration cannot be combined with any other component.
	if n := leadingDigits(p); n > 0 {
		if p[n] == 'W' {
			if len(p) == n+1 {
				return nil
			}
			return fail()
		}
	}

	got := false
	if n := leadingDigits(p); n > 0 && p[n] == 'D' {
		got = true
		p = p[n+1:]
	}

	if strings.HasPrefix(p, "T") {
		p = p[1:]
		if p == "" {
			return fail()
		}
		prev := 0
		for len(p) > 0 {
			n := leadingDigits(p)
			if n == 0 || n >= len(p) {
				return fail()
			}
			rank := 0
			switch p[n] {
			case 'H':
				rank = 1
			case 'M':
				rank = 2
			case 'S':
				rank = 3
			default:
				return fail()
			}
			if rank <= prev {
				return fail()
			}
			prev = rank
			got = true
			p = p[n+1:]
		}
	} else if p != "" {
		return fail()
	}

	if !got {
		return fail()
	}
	return nil
}

func leadingDigits(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

func validateBooleanICAL(s string) (string, error) {
	switch strings.ToUpper(s) {
	case "TRUE":
		return "true", nil
	case "FALSE":
		return "false", nil
	default:
		return "", &TypeError{Value: s, Type: ical.ValueBool}
	}
}

func convertBooleanXML(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1":
		return "TRUE", nil
	case "false", "0":
		return "FALSE", nil
	default:
		return "", &TypeError{Value: s, Type: ical.ValueBool}
	}
}

// unescapeText removes iCalendar TEXT escaping (RFC 5545 section
// 3.3.11) as required by RFC 6321 section 3.1.
func unescapeText(s string) (string, error) {
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			sb.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", &TypeError{Value: s, Type: ical.ValueText, msg: "malformed text: trailing backslash"}
		}
		switch s[i] {
		case '\\', ';', ',':
			sb.WriteByte(s[i])
		case 'n', 'N':
			sb.WriteByte('\n')
		default:
			return "", &TypeError{Value: s, Type: ical.ValueText, msg: "malformed text: invalid escape sequence"}
		}
	}
	return sb.String(), nil
}

// escapeText applies iCalendar TEXT escaping.
func escapeText(s string) (string, error) {
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', ';', ',':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			return "", &TypeError{Value: s, Type: ical.ValueText, msg: "carriage return is not allowed in text values"}
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String(), nil
}

// splitText unescapes an iCalendar TEXT value and splits it on
// unescaped delimiter characters. Escaped delimiters remain part of
// their text item (RFC 6321 section 3.4.1.1).
func splitText(s string, delim byte) ([]string, error) {
	var items []string
	var sb strings.Builder
	flush := func() {
		items = append(items, sb.String())
		sb.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			i++
			if i >= len(s) {
				return nil, &TypeError{Value: s, Type: ical.ValueText, msg: "malformed text: trailing backslash"}
			}
			switch s[i] {
			case '\\', ';', ',':
				sb.WriteByte(s[i])
			case 'n', 'N':
				sb.WriteByte('\n')
			default:
				return nil, &TypeError{Value: s, Type: ical.ValueText, msg: "malformed text: invalid escape sequence"}
			}
			continue
		}
		if c == delim {
			flush()
			continue
		}
		sb.WriteByte(c)
	}
	flush()
	return items, nil
}

// TypeError reports an iCalendar value that does not conform to its
// declared xCal type.
type TypeError struct {
	Value string
	Type  ical.ValueType
	msg   string
}

func (e *TypeError) Error() string {
	if e.msg != "" {
		return "xcal: " + e.msg
	}
	return "xcal: invalid " + strings.ToLower(string(e.Type)) + " value: " + strconv.Quote(e.Value)
}
