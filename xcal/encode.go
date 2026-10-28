package xcal

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/emersion/go-ical"
)

// Encoder writes iCalendar calendars as xCal XML (RFC 6321) to an
// output stream. A single Encoder can write several vcalendar
// elements under one icalendar root element: call Encode once per
// calendar and Close once after the last Encode.
type Encoder struct {
	w       io.Writer
	enc     *xml.Encoder
	started bool
	closed  bool
}

// NewEncoder creates a new xCal encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	return &Encoder{w: w, enc: enc}
}

// Marshal returns the xCal representation of a single iCalendar
// calendar.
func Marshal(cal *ical.Calendar) ([]byte, error) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(cal); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e *Encoder) startElement(local string, attrs ...xml.Attr) error {
	return e.enc.EncodeToken(xml.StartElement{
		Name: xml.Name{Local: local},
		Attr: attrs,
	})
}

func (e *Encoder) endElement(local string) error {
	return e.enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: local}})
}

func (e *Encoder) charData(s string) error {
	return e.enc.EncodeToken(xml.CharData([]byte(s)))
}

func (e *Encoder) begin() error {
	if _, err := io.WriteString(e.w, xmlHeader); err != nil {
		return err
	}
	attr := xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: Namespace}
	if err := e.enc.EncodeToken(xml.StartElement{
		Name: xml.Name{Local: "icalendar"},
		Attr: []xml.Attr{attr},
	}); err != nil {
		return err
	}
	e.started = true
	return nil
}

// Close writes the closing icalendar element. It must be called once
// after the last successful Encode.
func (e *Encoder) Close() error {
	if e.closed {
		return fmt.Errorf("xcal: encoder is already closed")
	}
	if !e.started {
		return fmt.Errorf("xcal: cannot close an encoder without any calendar")
	}
	e.closed = true
	if err := e.enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "icalendar"}}); err != nil {
		return err
	}
	if err := e.enc.Flush(); err != nil {
		return err
	}
	if _, err := io.WriteString(e.w, "\n"); err != nil {
		return err
	}
	return nil
}

// Encode writes one vcalendar element.
func (e *Encoder) Encode(cal *ical.Calendar) error {
	if e.closed {
		return fmt.Errorf("xcal: encoder is already closed")
	}
	if cal == nil || cal.Component == nil {
		return fmt.Errorf("xcal: cannot encode a nil calendar")
	}
	if !strings.EqualFold(cal.Name, ical.CompCalendar) {
		return fmt.Errorf("xcal: expected a %q root component, got %q", ical.CompCalendar, cal.Name)
	}
	if !e.started {
		if err := e.begin(); err != nil {
			return err
		}
	}
	return e.encodeComponent(cal.Component)
}

func (e *Encoder) encodeComponent(comp *ical.Component) error {
	local := strings.ToLower(comp.Name)
	if local == "" {
		return fmt.Errorf("xcal: component has an empty name")
	}
	if err := e.startElement(local); err != nil {
		return err
	}

	if err := e.startElement("properties"); err != nil {
		return err
	}
	propNames := make([]string, 0, len(comp.Props))
	for name := range comp.Props {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)
	for _, name := range propNames {
		for i := range comp.Props[name] {
			if err := e.encodeProp(&comp.Props[name][i]); err != nil {
				return fmt.Errorf("xcal: property %q: %w", name, err)
			}
		}
	}
	if err := e.endElement("properties"); err != nil {
		return err
	}

	if len(comp.Children) > 0 {
		if err := e.startElement("components"); err != nil {
			return err
		}
		for _, child := range comp.Children {
			if err := e.encodeComponent(child); err != nil {
				return err
			}
		}
		if err := e.endElement("components"); err != nil {
			return err
		}
	}

	return e.endElement(local)
}

// encodeProp converts a single iCalendar property to its xCal XML
// representation. It never mutates the input property.
func (e *Encoder) encodeProp(prop *ical.Prop) error {
	if prop.Name == XMLPropertyName {
		return e.encodeXMLProperty(prop)
	}

	local := strings.ToLower(prop.Name)
	if local == "" {
		return fmt.Errorf("empty property name")
	}
	if err := e.startElement(local); err != nil {
		return err
	}

	t := effectiveValueType(prop)
	value := prop.Value

	// RFC 6321 section 3.1: ENCODING=BASE64 pre-processing.
	dropEncoding := false
	if encoding := strings.ToUpper(prop.Params.Get(ical.ParamEncoding)); encoding == "BASE64" {
		if t == ical.ValueBinary {
			if err := validateBase64(value); err != nil {
				return err
			}
		} else {
			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				return fmt.Errorf("invalid ENCODING=BASE64 value: %w", err)
			}
			value = string(decoded)
		}
		dropEncoding = true
	}

	if err := e.encodeParameters(prop, t, dropEncoding); err != nil {
		return err
	}
	if err := e.encodePropertyValue(prop.Name, t, value); err != nil {
		return err
	}
	return e.endElement(local)
}

func (e *Encoder) encodeParameters(prop *ical.Prop, t ical.ValueType, dropEncoding bool) error {
	names := make([]string, 0, len(prop.Params))
	for name := range prop.Params {
		if strings.EqualFold(name, ical.ParamValue) {
			continue // RFC 6321 section 3.5.1
		}
		if dropEncoding && strings.EqualFold(name, ical.ParamEncoding) {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	if err := e.startElement("parameters"); err != nil {
		return err
	}
	for _, name := range names {
		values := prop.Params[name]
		pLocal := strings.ToLower(name)
		pt, known := paramValueTypes[strings.ToUpper(name)]
		if !known {
			// RFC 6321 section 5: unknown parameters use the unknown
			// element, preserving each list value separately.
			pt = valueUnknown
		}
		for _, v := range values {
			if err := e.encodeParameterValue(pLocal, pt, v); err != nil {
				return err
			}
		}
	}
	return e.endElement("parameters")
}

// encodeParameterValue emits one parameter value element. Parameter
// values are not iCalendar TEXT values, so their content is emitted
// verbatim (XML escaping aside).
func (e *Encoder) encodeParameterValue(pLocal string, t ical.ValueType, v string) error {
	valueLocal := xmlLocalName(t)
	switch t {
	case ical.ValueBool:
		converted, err := validateBooleanICAL(v)
		if err != nil {
			return err
		}
		v = converted
	case ical.ValueText, ical.ValueURI, ical.ValueCalendarAddress, valueUnknown:
		// Verbatim.
	default:
		return &TypeError{Value: v, Type: t, msg: "unsupported parameter value type"}
	}
	if err := e.startElement(pLocal); err != nil {
		return err
	}
	if err := e.elementWithText(valueLocal, v); err != nil {
		return err
	}
	return e.endElement(pLocal)
}

func xmlLocalName(t ical.ValueType) string {
	for local, typ := range xmlScalarElements {
		if typ == t {
			return local
		}
	}
	return "unknown"
}

// encodePropertyValue emits the value element(s) of a property.
func (e *Encoder) encodePropertyValue(name string, t ical.ValueType, value string) error {
	if t == ical.ValueRecurrence {
		// Structured RECUR is selected by value type, so extension
		// properties with an explicit VALUE=RECUR convert as well.
		return e.encodeRecurProperty(value)
	}
	switch name {
	case ical.PropGeo:
		if t != ical.ValueFloat {
			return &TypeError{Value: value, Type: t, msg: "GEO requires a FLOAT value"}
		}
		return e.encodeGeo(value)
	case ical.PropRequestStatus:
		if t == ical.ValueText {
			return e.encodeRequestStatus(value)
		}
	}

	switch t {
	case ical.ValueText:
		if name == ical.PropCategories || name == ical.PropResources {
			// Split on unescaped commas only: an escaped comma is
			// part of the text item.
			items, err := splitText(value, ',')
			if err != nil {
				return err
			}
			for _, item := range items {
				if err := e.elementWithText("text", item); err != nil {
					return err
				}
			}
			return nil
		}
	case ical.ValueDate, ical.ValueDateTime:
		if name == ical.PropExceptionDates || name == ical.PropRecurrenceDates {
			for _, item := range strings.Split(value, ",") {
				if err := e.encodeScalarValue(t, item); err != nil {
					return err
				}
			}
			return nil
		}
	case ical.ValuePeriod:
		if name == ical.PropFreeBusy || name == ical.PropRecurrenceDates {
			for _, item := range strings.Split(value, ",") {
				if err := e.encodeScalarValue(t, item); err != nil {
					return err
				}
			}
			return nil
		}
	}

	return e.encodeScalarValue(t, value)
}

// encodeScalarValue emits one xCal value element.
func (e *Encoder) encodeScalarValue(t ical.ValueType, value string) error {
	switch t {
	case ical.ValueBinary:
		if err := validateBase64(value); err != nil {
			return err
		}
		return e.elementWithText("binary", value)
	case ical.ValueBool:
		v, err := validateBooleanICAL(value)
		if err != nil {
			return err
		}
		return e.elementWithText("boolean", v)
	case ical.ValueCalendarAddress:
		return e.elementWithText("cal-address", value)
	case ical.ValueDate:
		v, err := convertDateICAL(value)
		if err != nil {
			return err
		}
		return e.elementWithText("date", v)
	case ical.ValueDateTime:
		v, err := convertDateTimeICAL(value)
		if err != nil {
			return err
		}
		return e.elementWithText("date-time", v)
	case ical.ValueDuration:
		if err := validateDuration(value); err != nil {
			return err
		}
		return e.elementWithText("duration", value)
	case ical.ValueFloat:
		if err := validateFloat(value); err != nil {
			return err
		}
		return e.elementWithText("float", value)
	case ical.ValueInt:
		if err := validateInteger(value); err != nil {
			return err
		}
		return e.elementWithText("integer", value)
	case ical.ValuePeriod:
		return e.encodePeriod(value)
	case ical.ValueText:
		text, err := unescapeText(value)
		if err != nil {
			return err
		}
		return e.elementWithText("text", text)
	case ical.ValueTime:
		v, err := convertTimeICAL(value)
		if err != nil {
			return err
		}
		return e.elementWithText("time", v)
	case ical.ValueURI:
		return e.elementWithText("uri", value)
	case ical.ValueUTCOffset:
		v, err := convertUTCOffsetICAL(value)
		if err != nil {
			return err
		}
		return e.elementWithText("utc-offset", v)
	case valueUnknown:
		return e.elementWithText("unknown", value)
	default:
		return &TypeError{Value: value, Type: t, msg: "unsupported value type"}
	}
}

func (e *Encoder) elementWithText(local, text string) error {
	if err := e.startElement(local); err != nil {
		return err
	}
	if err := e.charData(text); err != nil {
		return err
	}
	return e.endElement(local)
}

// encodePeriod emits a PERIOD value as start/end or start/duration
// child elements (RFC 6321 section 3.3.9).
func (e *Encoder) encodePeriod(value string) error {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return &TypeError{Value: value, Type: ical.ValuePeriod, msg: "period must contain a start and an end or duration"}
	}
	start, err := convertDateTimeICAL(parts[0])
	if err != nil {
		return fmt.Errorf("period start: %w", err)
	}

	if err := e.startElement("period"); err != nil {
		return err
	}
	if err := e.elementWithText("start", start); err != nil {
		return err
	}
	if dErr := validateDuration(parts[1]); dErr == nil {
		if err := e.elementWithText("duration", parts[1]); err != nil {
			return err
		}
	} else {
		end, err := convertDateTimeICAL(parts[1])
		if err != nil {
			return fmt.Errorf("period end: %w", err)
		}
		if err := e.elementWithText("end", end); err != nil {
			return err
		}
	}
	return e.endElement("period")
}

// encodeGeo emits the structured GEO value (RFC 6321 section
// 3.4.1.2). Escaped semicolons cannot occur in a FLOAT value.
func (e *Encoder) encodeGeo(value string) error {
	parts := strings.Split(value, ";")
	if len(parts) != 2 {
		return &TypeError{Value: value, Type: ical.ValueFloat, msg: "GEO must contain a latitude and a longitude"}
	}
	if err := validateFloat(parts[0]); err != nil {
		return fmt.Errorf("GEO latitude: %w", err)
	}
	if err := validateFloat(parts[1]); err != nil {
		return fmt.Errorf("GEO longitude: %w", err)
	}
	if err := e.elementWithText("latitude", parts[0]); err != nil {
		return err
	}
	return e.elementWithText("longitude", parts[1])
}

// encodeRequestStatus splits the semicolon-separated TEXT value while
// honoring TEXT escaping (RFC 6321 section 3.4.1.3).
func (e *Encoder) encodeRequestStatus(value string) error {
	parts, err := splitText(value, ';')
	if err != nil {
		return err
	}
	if len(parts) != 2 && len(parts) != 3 {
		return &TypeError{Value: value, Type: ical.ValueText, msg: "REQUEST-STATUS requires a code, description and optional data"}
	}
	if err := e.elementWithText("code", parts[0]); err != nil {
		return err
	}
	if err := e.elementWithText("description", parts[1]); err != nil {
		return err
	}
	if len(parts) == 3 {
		if err := e.elementWithText("data", parts[2]); err != nil {
			return err
		}
	}
	return nil
}

func validateBase64(s string) error {
	if _, err := base64.StdEncoding.DecodeString(s); err != nil {
		return fmt.Errorf("xcal: invalid base64 value: %w", err)
	}
	return nil
}
