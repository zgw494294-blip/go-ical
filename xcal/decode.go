package xcal

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/emersion/go-ical"
)

// Decoder reads an xCal XML stream (RFC 6321) and decodes its
// vcalendar elements one by one.
type Decoder struct {
	d       *xml.Decoder
	started bool
	done    bool
}

// NewDecoder creates a new xCal decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{d: xml.NewDecoder(r)}
}

// Unmarshal parses an xCal document containing exactly one
// vcalendar element. Streams with several calendars must be read with
// a Decoder.
func Unmarshal(data []byte) (*ical.Calendar, error) {
	dec := NewDecoder(bytes.NewReader(data))
	cal, err := dec.Decode()
	if err != nil {
		return nil, err
	}
	if _, err := dec.Decode(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("xcal: expected a single vcalendar element, got more than one")
		}
		return nil, err
	}
	return cal, nil
}

var reNameToken = regexp.MustCompile(`^[A-Z0-9-]+$`)

func validNameToken(local string) bool {
	return reNameToken.MatchString(local)
}

func isWhitespace(b []byte) bool {
	return len(bytes.TrimSpace(b)) == 0
}

// Decode returns the next calendar from the stream. io.EOF is returned
// when no more vcalendar elements are available.
func (d *Decoder) Decode() (*ical.Calendar, error) {
	if d.done {
		return nil, io.EOF
	}
	if !d.started {
		if err := d.readRoot(); err != nil {
			return nil, err
		}
		d.started = true
	}

	for {
		tok, err := d.d.Token()
		if err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace || t.Name.Local != "vcalendar" {
				return nil, fmt.Errorf("xcal: unexpected element <%s> inside icalendar", t.Name.Local)
			}
			comp, err := decodeComponent(d.d, t)
			if err != nil {
				return nil, err
			}
			return &ical.Calendar{Component: comp}, nil
		case xml.EndElement:
			if t.Name.Space != Namespace || t.Name.Local != "icalendar" {
				return nil, fmt.Errorf("xcal: unexpected closing element </%s>", t.Name.Local)
			}
			d.done = true
			if err := d.expectEOF(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		case xml.CharData:
			if !isWhitespace(t) {
				return nil, fmt.Errorf("xcal: unexpected text inside icalendar: %q", string(t))
			}
		case xml.Comment, xml.ProcInst:
			// Tolerated outside elements.
		case xml.Directive:
			return nil, fmt.Errorf("xcal: unexpected markup directive inside document")
		}
	}
}

func (d *Decoder) readRoot() error {
	for {
		tok, err := d.d.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace || t.Name.Local != "icalendar" {
				return fmt.Errorf("xcal: expected root element <icalendar> in namespace %s, got <%s>", Namespace, t.Name.Local)
			}
			return nil
		case xml.CharData:
			if !isWhitespace(t) {
				return fmt.Errorf("xcal: unexpected text before root element")
			}
		case xml.ProcInst, xml.Comment, xml.Directive:
			// Allow the XML declaration, DOCTYPE and comments.
		}
	}
}

func (d *Decoder) expectEOF() error {
	for {
		tok, err := d.d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch tok.(type) {
		case xml.CharData, xml.Comment, xml.ProcInst:
			// Trailing whitespace, comments and PIs are allowed.
		default:
			return fmt.Errorf("xcal: unexpected content after root element")
		}
	}
}

// drainElement discards an entire element subtree.
func drainElement(dec *xml.Decoder, start xml.StartElement) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

func ignoreContainerToken(tok xml.Token) error {
	switch t := tok.(type) {
	case xml.CharData:
		if !isWhitespace(t) {
			return fmt.Errorf("xcal: unexpected text: %q", string(t))
		}
	case xml.Comment, xml.ProcInst:
		// Allowed.
	case xml.Directive:
		return fmt.Errorf("xcal: unexpected markup directive")
	default:
		return fmt.Errorf("xcal: unexpected token")
	}
	return nil
}

func decodeComponent(dec *xml.Decoder, se xml.StartElement) (*ical.Component, error) {
	name := strings.ToUpper(se.Name.Local)
	if !validNameToken(name) {
		return nil, fmt.Errorf("xcal: invalid component name %q", se.Name.Local)
	}
	comp := &ical.Component{Name: name, Props: ical.Props{}}

	hasProps := false
	hasComponents := false
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				// Foreign elements other than direct children of
				// properties are ignored (RFC 6321 section 4.1).
				if err := drainElement(dec, t); err != nil {
					return nil, err
				}
				continue
			}
			switch t.Name.Local {
			case "properties":
				if hasProps {
					return nil, fmt.Errorf("xcal: component %q has multiple properties elements", name)
				}
				if hasComponents {
					return nil, fmt.Errorf("xcal: component %q puts properties after components", name)
				}
				hasProps = true
				props, err := decodeProperties(dec)
				if err != nil {
					return nil, err
				}
				for pname, values := range props {
					comp.Props[pname] = append(comp.Props[pname], values...)
				}
			case "components":
				if hasComponents {
					return nil, fmt.Errorf("xcal: component %q has multiple components elements", name)
				}
				hasComponents = true
				children, err := decodeComponents(dec)
				if err != nil {
					return nil, err
				}
				comp.Children = append(comp.Children, children...)
			default:
				return nil, fmt.Errorf("xcal: unexpected element <%s> in component %q", t.Name.Local, name)
			}
		case xml.EndElement:
			return comp, nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return nil, err
			}
		}
	}
}

func decodeComponents(dec *xml.Decoder) ([]*ical.Component, error) {
	var children []*ical.Component
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				if err := drainElement(dec, t); err != nil {
					return nil, err
				}
				continue
			}
			child, err := decodeComponent(dec, t)
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		case xml.EndElement:
			return children, nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return nil, err
			}
		}
	}
}

func decodeProperties(dec *xml.Decoder) (ical.Props, error) {
	props := ical.Props{}
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var prop *ical.Prop
			if t.Name.Space != Namespace {
				raw, err := captureForeignElement(dec, t)
				if err != nil {
					return nil, err
				}
				prop = ical.NewProp(XMLPropertyName)
				prop.Params.Set(ical.ParamEncoding, "BASE64")
				prop.Params.Set(ical.ParamValue, string(ical.ValueBinary))
				prop.Value = base64.StdEncoding.EncodeToString([]byte(raw))
			} else {
				prop, err = decodeProperty(dec, t)
				if err != nil {
					return nil, err
				}
			}
			props[prop.Name] = append(props[prop.Name], *prop)
		case xml.EndElement:
			return props, nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return nil, err
			}
		}
	}
}

func decodeProperty(dec *xml.Decoder, se xml.StartElement) (*ical.Prop, error) {
	name := strings.ToUpper(se.Name.Local)
	if !validNameToken(name) {
		return nil, fmt.Errorf("xcal: invalid property name %q", se.Name.Local)
	}
	prop := &ical.Prop{Name: name, Params: ical.Params{}}

	var err error
	switch name {
	case ical.PropGeo:
		err = decodeGeoValue(dec, prop)
	case ical.PropRequestStatus:
		err = decodeRequestStatusValue(dec, prop)
	case ical.PropRecurrenceRule:
		err = decodeRRuleValue(dec, prop)
	default:
		err = decodeGenericProperty(dec, prop)
	}
	if err != nil {
		return nil, err
	}
	return prop, nil
}

// readElementCharData reads all character data until the closing
// element. Child elements are rejected.
func readElementCharData(dec *xml.Decoder, local string) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return "", io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			if t.Name.Local != local {
				return "", fmt.Errorf("xcal: mismatched closing element </%s>", t.Name.Local)
			}
			return sb.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("xcal: unexpected child element <%s> in <%s>", t.Name.Local, local)
		case xml.Comment, xml.ProcInst:
			// Tolerated.
		case xml.Directive:
			return "", fmt.Errorf("xcal: unexpected markup directive in <%s>", local)
		}
	}
}

// scalarValue is one decoded property value in iCalendar lexical form.
type scalarValue struct {
	kind  ical.ValueType
	value string
}

// decodeScalarValue handles an xCal scalar element whose start token
// has already been read.
func decodeScalarValue(dec *xml.Decoder, se xml.StartElement) (scalarValue, error) {
	kind, ok := xmlScalarElements[se.Name.Local]
	if !ok {
		return scalarValue{}, fmt.Errorf("xcal: unknown value element <%s>", se.Name.Local)
	}
	text, err := readElementCharData(dec, se.Name.Local)
	if err != nil {
		return scalarValue{}, err
	}
	value, err := convertScalarXML(kind, text)
	if err != nil {
		return scalarValue{}, err
	}
	return scalarValue{kind: kind, value: value}, nil
}

func convertScalarXML(kind ical.ValueType, text string) (string, error) {
	switch kind {
	case ical.ValueText, ical.ValueURI, ical.ValueCalendarAddress, valueUnknown:
		return text, nil
	case ical.ValueBinary:
		compact := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, text)
		if err := validateBase64(compact); err != nil {
			return "", err
		}
		return compact, nil
	case ical.ValueBool:
		return convertBooleanXML(text)
	case ical.ValueDate:
		return convertDateXML(text)
	case ical.ValueDateTime:
		return convertDateTimeXML(text)
	case ical.ValueDuration:
		text = strings.TrimSpace(text)
		if err := validateDuration(text); err != nil {
			return "", err
		}
		return text, nil
	case ical.ValueFloat:
		text = strings.TrimSpace(text)
		if err := validateFloat(text); err != nil {
			return "", err
		}
		return text, nil
	case ical.ValueInt:
		text = strings.TrimSpace(text)
		if err := validateInteger(text); err != nil {
			return "", err
		}
		return text, nil
	case ical.ValueTime:
		return convertTimeXML(text)
	case ical.ValueUTCOffset:
		return convertUTCOffsetXML(text)
	default:
		return "", &TypeError{Value: text, Type: kind, msg: "unsupported value type"}
	}
}

func finalizeScalarValues(prop *ical.Prop, values []scalarValue) error {
	if len(values) == 0 {
		return fmt.Errorf("xcal: property %q has no value", prop.Name)
	}
	kind := values[0].kind
	for _, v := range values[1:] {
		if v.kind != kind {
			return fmt.Errorf("xcal: property %q mixes value types", prop.Name)
		}
	}

	switch kind {
	case valueUnknown:
		// Unprocessed values; RFC 6321 section 5. No VALUE parameter.
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = v.value
		}
		prop.Value = strings.Join(parts, ",")
	case ical.ValueText:
		parts := make([]string, len(values))
		for i, v := range values {
			escaped, err := escapeText(v.value)
			if err != nil {
				return err
			}
			parts[i] = escaped
		}
		prop.Value = strings.Join(parts, ",")
	default:
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = v.value
		}
		prop.Value = strings.Join(parts, ",")
		if kind == ical.ValueBinary {
			prop.Params.Set(ical.ParamEncoding, "BASE64")
			if defaultValueType(prop.Name) != ical.ValueBinary {
				prop.Params.Set(ical.ParamValue, string(kind))
			}
		} else if defaultValueType(prop.Name) != kind {
			prop.Params.Set(ical.ParamValue, string(kind))
		}
	}
	return nil
}

// decodeGenericProperty handles all properties except the structured
// GEO, REQUEST-STATUS and RRULE properties.
func decodeGenericProperty(dec *xml.Decoder, prop *ical.Prop) error {
	gotParameters := false
	var values []scalarValue

	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				return fmt.Errorf("xcal: unexpected foreign element <%s> in property %q", t.Name.Local, prop.Name)
			}
			if t.Name.Local == "parameters" {
				if gotParameters {
					return fmt.Errorf("xcal: property %q has multiple parameters elements", prop.Name)
				}
				if len(values) > 0 {
					return fmt.Errorf("xcal: property %q puts parameters after its value", prop.Name)
				}
				gotParameters = true
				if err := decodeParameters(dec, prop.Params); err != nil {
					return err
				}
				continue
			}

			if t.Name.Local == "period" {
				value, err := decodePeriodValue(dec, t)
				if err != nil {
					return err
				}
				values = append(values, value)
				continue
			}

			if t.Name.Local == "recur" {
				value, err := decodeRecur(dec, t)
				if err != nil {
					return err
				}
				values = append(values, scalarValue{kind: ical.ValueRecurrence, value: value})
				continue
			}

			v, err := decodeScalarValue(dec, t)
			if err != nil {
				return err
			}
			values = append(values, v)
		case xml.EndElement:
			return finalizeScalarValues(prop, values)
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return err
			}
		}
	}
}

func decodeParameters(dec *xml.Decoder, params ical.Params) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				if err := drainElement(dec, t); err != nil {
					return err
				}
				continue
			}
			pname, values, err := decodeOneParameter(dec, t)
			if err != nil {
				return err
			}
			if pname == ical.ParamValue {
				return fmt.Errorf("xcal: the VALUE parameter must not appear in xCal")
			}
			params[pname] = append(params[pname], values...)
		case xml.EndElement:
			return nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return err
			}
		}
	}
}

func decodeOneParameter(dec *xml.Decoder, se xml.StartElement) (string, []string, error) {
	pname := strings.ToUpper(se.Name.Local)
	if !validNameToken(pname) {
		return "", nil, fmt.Errorf("xcal: invalid parameter name %q", se.Name.Local)
	}
	var values []string
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return "", nil, io.ErrUnexpectedEOF
			}
			return "", nil, fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				return "", nil, fmt.Errorf("xcal: unexpected foreign element in parameter %q", pname)
			}
			v, err := decodeScalarValue(dec, t)
			if err != nil {
				return "", nil, err
			}
			values = append(values, v.value)
		case xml.EndElement:
			if t.Name.Local != se.Name.Local {
				return "", nil, fmt.Errorf("xcal: mismatched closing element </%s>", t.Name.Local)
			}
			if len(values) == 0 {
				return "", nil, fmt.Errorf("xcal: parameter %q has no value", pname)
			}
			return pname, values, nil
		case xml.CharData:
			if !isWhitespace(t) {
				return "", nil, fmt.Errorf("xcal: unexpected text in parameter %q", pname)
			}
		case xml.Comment, xml.ProcInst:
			// Tolerated.
		case xml.Directive:
			return "", nil, fmt.Errorf("xcal: unexpected markup directive in parameter %q", pname)
		}
	}
}

// decodePeriodValue reads a PERIOD value element.
func decodePeriodValue(dec *xml.Decoder, se xml.StartElement) (scalarValue, error) {
	var start, end, duration string
	haveEnd := false
	haveDuration := false

	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return scalarValue{}, io.ErrUnexpectedEOF
			}
			return scalarValue{}, fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				return scalarValue{}, fmt.Errorf("xcal: unexpected foreign element in period")
			}
			text, err := readElementCharData(dec, t.Name.Local)
			if err != nil {
				return scalarValue{}, err
			}
			text = strings.TrimSpace(text)
			switch t.Name.Local {
			case "start":
				if start != "" {
					return scalarValue{}, fmt.Errorf("xcal: period has multiple start elements")
				}
				start, err = convertDateTimeXML(text)
				if err != nil {
					return scalarValue{}, fmt.Errorf("period start: %w", err)
				}
			case "end":
				if haveEnd || haveDuration {
					return scalarValue{}, fmt.Errorf("xcal: period has more than one end/duration")
				}
				end, err = convertDateTimeXML(text)
				if err != nil {
					return scalarValue{}, fmt.Errorf("period end: %w", err)
				}
				haveEnd = true
			case "duration":
				if haveEnd || haveDuration {
					return scalarValue{}, fmt.Errorf("xcal: period has more than one end/duration")
				}
				if err := validateDuration(text); err != nil {
					return scalarValue{}, fmt.Errorf("period duration: %w", err)
				}
				duration = text
				haveDuration = true
			default:
				return scalarValue{}, fmt.Errorf("xcal: unexpected element <%s> in period", t.Name.Local)
			}
		case xml.EndElement:
			if t.Name.Local != se.Name.Local {
				return scalarValue{}, fmt.Errorf("xcal: mismatched closing element </%s>", t.Name.Local)
			}
			if start == "" || (!haveEnd && !haveDuration) {
				return scalarValue{}, fmt.Errorf("xcal: period requires a start and an end or duration")
			}
			if haveEnd {
				return scalarValue{kind: ical.ValuePeriod, value: start + "/" + end}, nil
			}
			return scalarValue{kind: ical.ValuePeriod, value: start + "/" + duration}, nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return scalarValue{}, err
			}
		}
	}
}

// decodeGeoValue reads the structured GEO value.
func decodeGeoValue(dec *xml.Decoder, prop *ical.Prop) error {
	var lat, long string
	state := 0 // 0=expect latitude, 1=expect longitude, 2=done
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				return fmt.Errorf("xcal: unexpected foreign element in GEO")
			}
			text, err := readElementCharData(dec, t.Name.Local)
			if err != nil {
				return err
			}
			text = strings.TrimSpace(text)
			if err := validateFloat(text); err != nil {
				return fmt.Errorf("GEO: %w", err)
			}
			switch t.Name.Local {
			case "latitude":
				if state != 0 {
					return fmt.Errorf("xcal: GEO has an unexpected or duplicate latitude")
				}
				lat = text
				state = 1
			case "longitude":
				if state != 1 {
					return fmt.Errorf("xcal: GEO has an unexpected or duplicate longitude")
				}
				long = text
				state = 2
			default:
				return fmt.Errorf("xcal: unexpected element <%s> in GEO", t.Name.Local)
			}
		case xml.EndElement:
			if state != 2 {
				return fmt.Errorf("xcal: GEO requires latitude and longitude")
			}
			prop.Value = lat + ";" + long
			return nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return err
			}
		}
	}
}

// decodeRequestStatusValue reads code/description/data elements.
func decodeRequestStatusValue(dec *xml.Decoder, prop *ical.Prop) error {
	var parts []string
	state := 0 // 0=code, 1=description, 2=data-or-end
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				return fmt.Errorf("xcal: unexpected foreign element in REQUEST-STATUS")
			}
			text, err := readElementCharData(dec, t.Name.Local)
			if err != nil {
				return err
			}
			switch t.Name.Local {
			case "code":
				if state != 0 {
					return fmt.Errorf("xcal: REQUEST-STATUS has an unexpected or duplicate code")
				}
				state = 1
			case "description":
				if state != 1 {
					return fmt.Errorf("xcal: REQUEST-STATUS has an unexpected or duplicate description")
				}
				state = 2
			case "data":
				if state != 2 {
					return fmt.Errorf("xcal: REQUEST-STATUS data must come after description")
				}
				state = 3
			default:
				return fmt.Errorf("xcal: unexpected element <%s> in REQUEST-STATUS", t.Name.Local)
			}
			escaped, err := escapeText(text)
			if err != nil {
				return err
			}
			parts = append(parts, escaped)
		case xml.EndElement:
			if state < 2 {
				return fmt.Errorf("xcal: REQUEST-STATUS requires code and description")
			}
			prop.Value = strings.Join(parts, ";")
			return nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return err
			}
		}
	}
}

// decodeRRuleValue reads parameters and the recur element of RRULE.
func decodeRRuleValue(dec *xml.Decoder, prop *ical.Prop) error {
	gotParameters := false
	gotRecur := false
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				return fmt.Errorf("xcal: unexpected foreign element in RRULE")
			}
			if t.Name.Local == "parameters" {
				if gotParameters {
					return fmt.Errorf("xcal: RRULE has multiple parameters elements")
				}
				if gotRecur {
					return fmt.Errorf("xcal: RRULE puts parameters after its value")
				}
				gotParameters = true
				if err := decodeParameters(dec, prop.Params); err != nil {
					return err
				}
				continue
			}
			if t.Name.Local != "recur" || gotRecur {
				return fmt.Errorf("xcal: RRULE must contain exactly one recur element")
			}
			gotRecur = true
			value, err := decodeRecur(dec, t)
			if err != nil {
				return err
			}
			prop.Value = value
		case xml.EndElement:
			if !gotRecur {
				return fmt.Errorf("xcal: RRULE is missing its recur element")
			}
			return nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return err
			}
		}
	}
}

func decodeRecur(dec *xml.Decoder, se xml.StartElement) (string, error) {
	b := newRecurBuilder()
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return "", io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				return "", fmt.Errorf("xcal: unexpected foreign element in recur")
			}
			local := t.Name.Local
			switch local {
			case "until":
				compact, err := decodeRecurUntil(dec, t)
				if err != nil {
					return "", err
				}
				if err := b.setUntil(compact); err != nil {
					return "", err
				}
				continue
			}
			text, err := readElementCharData(dec, local)
			if err != nil {
				return "", err
			}
			text = strings.TrimSpace(text)
			switch local {
			case "freq":
				if !recurFreqs[strings.ToUpper(text)] {
					return "", &TypeError{Value: text, Type: ical.ValueRecurrence, msg: "invalid FREQ value"}
				}
				if err := b.add("FREQ", strings.ToUpper(text)); err != nil {
					return "", err
				}
			case "count", "interval":
				if err := validateInteger(text); err != nil {
					return "", fmt.Errorf("xcal: recur %s: %w", local, err)
				}
				if err := b.add(strings.ToUpper(local), text); err != nil {
					return "", err
				}
			case "wkst":
				if !isWeekday(text) {
					return "", &TypeError{Value: text, Type: ical.ValueRecurrence, msg: "invalid WKST value"}
				}
				if err := b.add("WKST", strings.ToUpper(text)); err != nil {
					return "", err
				}
			default:
				icalName, ok := recurListPart(local)
				if !ok {
					return "", fmt.Errorf("xcal: unknown recurrence rule element <%s>", local)
				}
				if icalName == "BYDAY" {
					if !isByDayToken(text) {
						return "", &TypeError{Value: text, Type: ical.ValueRecurrence, msg: "invalid BYDAY value"}
					}
					b.addListItem(icalName, strings.ToUpper(text))
				} else {
					if err := validateInteger(text); err != nil {
						return "", fmt.Errorf("xcal: recur %s: %w", icalName, err)
					}
					b.addListItem(icalName, text)
				}
			}
		case xml.EndElement:
			return b.build()
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return "", err
			}
		}
	}
}

// decodeRecurUntil reads the nested date/date-time element of an
// until part and returns the compact iCalendar lexical value.
func decodeRecurUntil(dec *xml.Decoder, se xml.StartElement) (string, error) {
	var compact string
	found := false
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return "", io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("xcal: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if found {
				return "", fmt.Errorf("xcal: until must contain a single value element")
			}
			if t.Name.Space != Namespace {
				return "", fmt.Errorf("xcal: unexpected foreign element in until")
			}
			text, err := readElementCharData(dec, t.Name.Local)
			if err != nil {
				return "", err
			}
			text = strings.TrimSpace(text)
			switch t.Name.Local {
			case "date":
				compact, err = convertDateXML(text)
			case "date-time":
				compact, err = convertDateTimeXML(text)
			default:
				return "", fmt.Errorf("xcal: until allows date or date-time, got <%s>", t.Name.Local)
			}
			if err != nil {
				return "", err
			}
			found = true
		case xml.EndElement:
			if t.Name.Local != se.Name.Local {
				return "", fmt.Errorf("xcal: mismatched closing element </%s>", t.Name.Local)
			}
			if !found {
				return "", fmt.Errorf("xcal: until is missing its date or date-time element")
			}
			return compact, nil
		default:
			if err := ignoreContainerToken(tok); err != nil {
				return "", err
			}
		}
	}
}
