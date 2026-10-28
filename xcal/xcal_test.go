package xcal

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"io/ioutil"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

const simpleXCal = `<?xml version="1.0" encoding="UTF-8"?>
<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0">
  <vcalendar>
    <properties>
      <prodid>
        <text>-//Test//EN</text>
      </prodid>
      <version>
        <text>2.0</text>
      </version>
    </properties>
    <components>
      <vevent>
        <properties>
          <dtstamp>
            <date-time>2020-01-02T03:04:05Z</date-time>
          </dtstamp>
          <dtstart>
            <date>2020-01-06</date>
          </dtstart>
          <summary>
            <text>Hello</text>
          </summary>
          <uid>
            <text>uid1@example.com</text>
          </uid>
        </properties>
      </vevent>
    </components>
  </vcalendar>
</icalendar>
`

func simpleCalendar() *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//EN")
	ev := ical.NewComponent(ical.CompEvent)
	ev.Props.SetText(ical.PropUID, "uid1@example.com")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	ev.Props.SetDate(ical.PropDateTimeStart, time.Date(2020, 1, 6, 0, 0, 0, 0, time.UTC))
	ev.Props.SetText(ical.PropSummary, "Hello")
	cal.Children = append(cal.Children, ev)
	return cal
}

func propText(t *testing.T, comp *ical.Component, name string) string {
	t.Helper()
	p := comp.Props.Get(name)
	if p == nil {
		t.Fatalf("missing property %q", name)
	}
	s, err := p.Text()
	if err != nil {
		t.Fatalf("property %q: %v", name, err)
	}
	return s
}

func TestMarshalSimple(t *testing.T) {
	out, err := Marshal(simpleCalendar())
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != simpleXCal {
		t.Fatalf("unexpected xCal output:\n%s", string(out))
	}
}

func TestUnmarshalSimple(t *testing.T) {
	cal, err := Unmarshal([]byte(simpleXCal))
	if err != nil {
		t.Fatal(err)
	}
	if got := propText(t, cal.Component, ical.PropProductID); got != "-//Test//EN" {
		t.Errorf("PRODID = %q", got)
	}
	if len(cal.Children) != 1 || cal.Children[0].Name != ical.CompEvent {
		t.Fatalf("unexpected children: %+v", cal.Children)
	}
	ev := cal.Children[0]
	dt, err := ev.Props.Get(ical.PropDateTimeStart).DateTime(nil)
	if err != nil {
		t.Fatal(err)
	}
	if dt.Format("2006-01-02") != "2020-01-06" {
		t.Errorf("DTSTART = %v", dt)
	}
	// DTSTART defaults to DATE-TIME, so a DATE value must restore
	// the explicit VALUE parameter (RFC 6321 section 3.5.1).
	if got := ev.Props.Get(ical.PropDateTimeStart).Params.Get(ical.ParamValue); got != "DATE" {
		t.Errorf("DTSTART VALUE = %q, want DATE", got)
	}
}

func TestRoundTripAllTypes(t *testing.T) {
	cal := buildRichCalendar(t)

	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatalf("unmarshal failed: %v\n%s", err, string(out))
	}
	out2, err := Marshal(cal2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, out2) {
		t.Fatalf("re-marshalled XML differs\n--- first ---\n%s\n--- second ---\n%s", string(out), string(out2))
	}

	ev := cal2.Children[0]
	checks := map[string]string{
		ical.PropUID:            "uid1@example.com",
		ical.PropPriority:       "5",
		ical.PropSequence:       "2",
		ical.PropSummary:        "Hello\\, world\\; & more\\nnewline",
		ical.PropGeo:            "37.386013;-122.082932",
		ical.PropRequestStatus:  "2.0;Success;extra\\;data",
		ical.PropFreeBusy:       "20200102T150000Z/20200102T160000Z,20200103T150000Z/PT2H",
		ical.PropRecurrenceRule: "FREQ=YEARLY;COUNT=5;BYDAY=1SU,-1MO;BYMONTH=3",
		ical.PropCategories:     "WORK,RO\\,OM",
		ical.PropResources:      "Projector,Room A",
		ical.PropDuration:       "PT1H30M",
		"X-BOOL":                "TRUE",
		"X-OFFSET":              "+0130",
		"X-TIME":                "134500",
		"X-URI":                 "https://example.com/a?b=1&c=2",
	}
	for name, want := range checks {
		p := ev.Props.Get(name)
		if p == nil {
			t.Errorf("missing property %q", name)
			continue
		}
		if p.Value != want {
			t.Errorf("property %q = %q, want %q", name, p.Value, want)
		}
	}

	// Categories must stay two values, not one raw string.
	cats, err := ev.Props.Get(ical.PropCategories).TextList()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cats, []string{"WORK", "RO,OM"}) {
		t.Errorf("CATEGORIES = %#v", cats)
	}

	// Attendee parameters.
	att := ev.Props.Get(ical.PropAttendee)
	if att.Value != "mailto:a@example.com" {
		t.Errorf("ATTENDEE = %q", att.Value)
	}
	if att.Params.Get(ical.ParamRSVP) != "TRUE" {
		t.Errorf("RSVP = %q", att.Params.Get(ical.ParamRSVP))
	}
	if got := att.Params.Values(ical.ParamDelegatedTo); !reflect.DeepEqual(got, []string{"mailto:b@example.com", "mailto:c@example.com"}) {
		t.Errorf("DELEGATED-TO = %#v", got)
	}
	if att.Params.Get(ical.ParamCommonName) != "John Doe" {
		t.Errorf("CN = %q", att.Params.Get(ical.ParamCommonName))
	}

	// Explicit type on extension property is restored.
	xd := ev.Props.Get("X-DAY")
	if xd == nil || xd.Value != "20200101" || xd.Params.Get(ical.ParamValue) != "DATE" {
		t.Errorf("X-DAY = %#v", xd)
	}

	// Unknown extension with unknown parameter list.
	xw := ev.Props.Get("X-WEIRD")
	if xw == nil || xw.Value != "raw?value" {
		t.Errorf("X-WEIRD = %#v", xw)
	}
	if got := xw.Params.Values("X-PARAM"); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Errorf("X-PARAM = %#v", got)
	}

	// Binary attachment round-trips as base64.
	bin := ev.Props.Get(ical.PropAttach)
	if bin == nil {
		t.Fatal("missing ATTACH")
	}
	b, err := bin.Binary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, []byte{1, 2, 3, 250}) {
		t.Errorf("binary = %v", b)
	}
	if bin.Params.Get(ical.ParamValue) != "BINARY" || bin.Params.Get(ical.ParamEncoding) != "BASE64" {
		t.Errorf("binary params = %#v", bin.Params)
	}
}

func buildRichCalendar(t *testing.T) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//EN")

	ev := ical.NewComponent(ical.CompEvent)
	add := func(p *ical.Prop) { ev.Props.Add(p) }

	p := ical.NewProp(ical.PropUID)
	p.SetText("uid1@example.com")
	add(p)

	dt := ical.NewProp(ical.PropDateTimeStamp)
	dt.SetDateTime(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	add(dt)

	dts := ical.NewProp(ical.PropDateTimeStart)
	dts.SetDate(time.Date(2020, 1, 6, 0, 0, 0, 0, time.UTC))
	add(dts)

	sm := ical.NewProp(ical.PropSummary)
	sm.SetText("Hello, world; & more\nnewline")
	add(sm)

	pr := ical.NewProp(ical.PropPriority)
	pr.Value = "5"
	add(pr)

	sq := ical.NewProp(ical.PropSequence)
	sq.Value = "2"
	add(sq)

	cat := ical.NewProp(ical.PropCategories)
	cat.SetTextList([]string{"WORK", "RO,OM"})
	add(cat)

	res := ical.NewProp(ical.PropResources)
	res.SetTextList([]string{"Projector", "Room A"})
	add(res)

	geo := ical.NewProp(ical.PropGeo)
	geo.Value = "37.386013;-122.082932"
	add(geo)

	rst := ical.NewProp(ical.PropRequestStatus)
	rst.Value = "2.0;Success;extra\\;data"
	add(rst)

	fb := ical.NewProp(ical.PropFreeBusy)
	fb.Value = "20200102T150000Z/20200102T160000Z,20200103T150000Z/PT2H"
	add(fb)

	rr := ical.NewProp(ical.PropRecurrenceRule)
	rr.Value = "FREQ=YEARLY;BYDAY=1SU,-1MO;BYMONTH=3;COUNT=5"
	add(rr)

	dur := ical.NewProp(ical.PropDuration)
	dur.Value = "PT1H30M"
	add(dur)

	att := ical.NewProp(ical.PropAttendee)
	att.Value = "mailto:a@example.com"
	att.Params.Set(ical.ParamRSVP, "TRUE")
	att.Params.Set(ical.ParamCommonName, "John Doe")
	att.Params.Add(ical.ParamDelegatedTo, "mailto:b@example.com")
	att.Params.Add(ical.ParamDelegatedTo, "mailto:c@example.com")
	add(att)

	xw := ical.NewProp("X-WEIRD")
	xw.Value = "raw?value"
	xw.Params.Add("X-PARAM", "one")
	xw.Params.Add("X-PARAM", "two")
	add(xw)

	xd := ical.NewProp("X-DAY")
	xd.Params.Set(ical.ParamValue, "DATE")
	xd.Value = "20200101"
	add(xd)

	xb := ical.NewProp("X-BOOL")
	xb.Params.Set(ical.ParamValue, "BOOLEAN")
	xb.Value = "TRUE"
	add(xb)

	xo := ical.NewProp("X-OFFSET")
	xo.Params.Set(ical.ParamValue, "UTC-OFFSET")
	xo.Value = "+0130"
	add(xo)

	xt := ical.NewProp("X-TIME")
	xt.Params.Set(ical.ParamValue, "TIME")
	xt.Value = "134500"
	add(xt)

	xu := ical.NewProp("X-URI")
	xu.Params.Set(ical.ParamValue, "URI")
	xu.Value = "https://example.com/a?b=1&c=2"
	add(xu)

	bin := ical.NewProp(ical.PropAttach)
	bin.SetBinary([]byte{1, 2, 3, 250})
	bin.Params.Set(ical.ParamFormatType, "image/png")
	add(bin)

	cal.Children = append(cal.Children, ev)
	return cal
}

func TestBooleanValues(t *testing.T) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//EN")
	ev := ical.NewComponent(ical.CompEvent)
	p := ical.NewProp("X-FLAG")
	p.Params.Set(ical.ParamValue, "BOOLEAN")
	p.Value = "FALSE"
	ev.Props.Add(p)
	cal.Children = append(cal.Children, ev)

	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("<boolean>false</boolean>")) {
		t.Errorf("expected lowercase boolean, got:\n%s", string(out))
	}
	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := cal2.Children[0].Props.Get("X-FLAG").Value; got != "FALSE" {
		t.Errorf("boolean = %q", got)
	}
}

func TestEncodingBase64Preprocessing(t *testing.T) {
	const icalText = `BEGIN:VCALENDAR
PRODID:-//Test//EN
VERSION:2.0
BEGIN:VEVENT
UID:u@example.com
DTSTAMP:20200102T030405Z
SUMMARY;ENCODING=BASE64:SGVsbG8sIFdvcmxk
X-RAW;ENCODING=BASE64:cmF3PXRleHQ=
END:VEVENT
END:VCALENDAR
`
	cal, err := ical.NewDecoder(strings.NewReader(icalText)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "<text>Hello, World</text>") {
		t.Errorf("non-binary ENCODING=BASE64 must be decoded:\n%s", s)
	}
	if strings.Contains(s, "<encoding>") {
		t.Errorf("ENCODING parameter must be removed after base64 preprocessing:\n%s", s)
	}
	if !strings.Contains(s, "<unknown>raw=text</unknown>") {
		t.Errorf("decoded unknown value not preserved:\n%s", s)
	}

	// Round-trip back: the decoded text stays text.
	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := propText(t, cal2.Children[0], ical.PropSummary); got != "Hello, World" {
		t.Errorf("SUMMARY = %q", got)
	}
}

func TestForeignXMLRoundTrip(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0">
  <vcalendar>
    <properties>
      <version><text>2.0</text></version>
      <prodid><text>-//Test//EN</text></prodid>
    </properties>
    <components>
      <vevent>
        <properties>
          <uid><text>u@example.com</text></uid>
          <dtstamp><date-time>2020-01-02T03:04:05Z</date-time></dtstamp>
          <k:place xmlns:k="urn:example:kml">
            <k:name>Home</k:name>
            <k:coords>1.5,-2.5</k:coords>
          </k:place>
        </properties>
      </vevent>
    </components>
  </vcalendar>
</icalendar>
`
	cal, err := Unmarshal([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	ev := cal.Children[0]
	xmlProps := ev.Props.Values(XMLPropertyName)
	if len(xmlProps) != 1 {
		t.Fatalf("expected one XML property, got %d", len(xmlProps))
	}
	raw, ok := XMLProperty(&xmlProps[0])
	if !ok {
		t.Fatal("XMLProperty extraction failed")
	}
	if !strings.Contains(raw, "urn:example:kml") || !strings.Contains(raw, "Home</name>") {
		t.Errorf("foreign XML not preserved: %s", raw)
	}
	// The expanded name must be retained.
	fd := xml.NewDecoder(strings.NewReader(raw))
	se, err := fd.Token()
	if err != nil {
		t.Fatalf("captured XML is not parseable: %v", err)
	}
	root := se.(xml.StartElement)
	if root.Name.Space != "urn:example:kml" || root.Name.Local != "place" {
		t.Errorf("captured root = {%s}%s", root.Name.Space, root.Name.Local)
	}

	// Re-encode: markup must be inserted as XML, not escaped.
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "&lt;k:place") || !strings.Contains(s, "urn:example:kml") {
		t.Errorf("foreign XML must be emitted unescaped:\n%s", s)
	}
	// The document must still be well-formed XML.
	if _, err := xml.NewDecoder(strings.NewReader(s)).Token(); err != nil {
		t.Fatalf("re-encoded document is not well-formed XML: %v", err)
	}
	// Full parse of the document.
	if _, err := Unmarshal(out); err != nil {
		t.Fatal(err)
	}

	// Construct the same property via the public constructor.
	p, err := NewXMLProperty(`<place xmlns="urn:example:kml"><name>Home</name></place>`)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != XMLPropertyName || p.Params.Get(ical.ParamEncoding) != "BASE64" {
		t.Errorf("unexpected XML property: %#v", p)
	}
	raw2, ok := XMLProperty(p)
	if !ok || !strings.Contains(raw2, "urn:example:kml") {
		t.Errorf("constructor roundtrip failed: %q", raw2)
	}
}

func TestNewXMLPropertyRejects(t *testing.T) {
	for _, raw := range []string{
		`<vevent xmlns="urn:ietf:params:xml:ns:icalendar-2.0"/>`,
		`<no-ns/>`,
		`not xml`,
		`a<b xmlns="urn:x"/>`,
		`<a xmlns="urn:x"/><b xmlns="urn:x"/>`,
		``,
	} {
		if _, err := NewXMLProperty(raw); err == nil {
			t.Errorf("expected error for %q", raw)
		}
	}
}

func TestTimezoneAndAlarmNesting(t *testing.T) {
	const icalText = `BEGIN:VCALENDAR
PRODID:-//Test//EN
VERSION:2.0
BEGIN:VTIMEZONE
TZID:America/New_York
BEGIN:DAYLIGHT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
TZNAME:EDT
DTSTART:19700308T020000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU
END:DAYLIGHT
BEGIN:STANDARD
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
TZNAME:EST
DTSTART:19701101T020000
RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:u@example.com
DTSTAMP:20200102T030405Z
DTSTART;TZID=America/New_York:20200308T090000
SUMMARY:Meeting
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
DESCRIPTION:Reminder
END:VALARM
END:VEVENT
END:VCALENDAR
`
	cal, err := ical.NewDecoder(strings.NewReader(icalText)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"<vtimezone>",
		"<daylight>",
		"<standard>",
		"<utc-offset>-05:00</utc-offset>",
		"<utc-offset>-04:00</utc-offset>",
		"<tzid>",
		"<text>America/New_York</text>",
		"<date-time>2020-03-08T09:00:00</date-time>",
		"<valarm>",
		"<duration>-PT15M</duration>",
		"<byday>2SU</byday>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("xCal output missing %q:\n%s", want, s)
		}
	}
	// No Z normalization for the local date-time and no RRULE expansion.
	if strings.Contains(s, "<date-time>2020-03-08T09:00:00Z</date-time>") {
		t.Errorf("floating/TZID date-time must not gain a Z")
	}

	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(cal2.Children) != 2 {
		t.Fatalf("expected VTIMEZONE then VEVENT, got %d children", len(cal2.Children))
	}
	tz := cal2.Children[0]
	if tz.Name != ical.CompTimezone || len(tz.Children) != 2 {
		t.Fatalf("bad timezone: %#v", tz)
	}
	if tz.Children[0].Name != ical.CompTimezoneDaylight || tz.Children[1].Name != ical.CompTimezoneStandard {
		t.Errorf("timezone child order changed: %s, %s", tz.Children[0].Name, tz.Children[1].Name)
	}
	if got := tz.Children[0].Props.Get(ical.PropTimezoneOffsetFrom).Value; got != "-0500" {
		t.Errorf("TZOFFSETFROM = %q", got)
	}
	ev := cal2.Children[1]
	if ev.Props.Get(ical.PropDateTimeStart).Params.Get(ical.ParamTimezoneID) != "America/New_York" {
		t.Errorf("TZID not preserved")
	}
	if len(ev.Children) != 1 || ev.Children[0].Name != ical.CompAlarm {
		t.Fatalf("alarm nesting lost: %#v", ev.Children)
	}
	// TZID is data only: the local value stays unconverted.
	if got := ev.Props.Get(ical.PropDateTimeStart).Value; got != "20200308T090000" {
		t.Errorf("local date-time changed: %q", got)
	}
}

func TestMultipleVCalendarsStream(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(simpleCalendar()); err != nil {
		t.Fatal(err)
	}
	second := simpleCalendar()
	second.Children[0].Props.SetText(ical.PropUID, "uid2@example.com")
	if err := enc.Encode(second); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(second); err == nil {
		t.Error("Encode after Close must fail")
	}

	dec := NewDecoder(&buf)
	var uids []string
	for {
		cal, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		uids = append(uids, propText(t, cal.Children[0], ical.PropUID))
	}
	if !reflect.DeepEqual(uids, []string{"uid1@example.com", "uid2@example.com"}) {
		t.Errorf("uids = %#v", uids)
	}
}

func TestUnmarshalRejectsMultipleCalendars(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	_ = enc.Encode(simpleCalendar())
	_ = enc.Encode(simpleCalendar())
	_ = enc.Close()
	if _, err := Unmarshal(buf.Bytes()); err == nil {
		t.Fatal("expected error for a stream with multiple calendars")
	}
}

type chunkReader struct {
	data []byte
	n    int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.n
	if n > len(r.data) {
		n = len(r.data)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func TestChunkedDecoding(t *testing.T) {
	data, err := Marshal(simpleCalendar())
	if err != nil {
		t.Fatal(err)
	}
	cal1, err := NewDecoder(bytes.NewReader(data)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	cal2, err := NewDecoder(&chunkReader{data: data, n: 1}).Decode()
	if err != nil {
		t.Fatal(err)
	}
	out1, _ := Marshal(cal1)
	out2, _ := Marshal(cal2)
	if !bytes.Equal(out1, out2) {
		t.Errorf("chunked decoding differs from contiguous decoding")
	}
}

type failingWriter struct{ failAfter int }

func (w *failingWriter) Write(p []byte) (int, error) {
	w.failAfter -= len(p)
	if w.failAfter <= 0 {
		return 0, errors.New("write failure")
	}
	return len(p), nil
}

func TestWriterError(t *testing.T) {
	enc := NewEncoder(&failingWriter{failAfter: 10})
	err := enc.Encode(simpleCalendar())
	if err == nil {
		err = enc.Close()
	}
	if err == nil {
		t.Fatal("expected a write error to be surfaced")
	}
}

func TestDoesNotMutateInput(t *testing.T) {
	cal := buildRichCalendar(t)
	before, err := ioutil.ReadAll(mustTextEncoder(cal))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Marshal(cal); err != nil {
		t.Fatal(err)
	}
	after, err := ioutil.ReadAll(mustTextEncoder(cal))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("calendar tree was mutated:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// Explicit VALUE params must still be present.
	if got := cal.Children[0].Props.Get("X-DAY").Params.Get(ical.ParamValue); got != "DATE" {
		t.Errorf("VALUE parameter removed from input tree: %q", got)
	}
}

func mustTextEncoder(cal *ical.Calendar) io.Reader {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		panic(err)
	}
	return &buf
}

func TestIndependentOperations(t *testing.T) {
	// A rejected document must not affect later valid decodes.
	for _, bad := range invalidInputs {
		if _, err := Unmarshal([]byte(bad)); err == nil {
			t.Errorf("expected error for invalid input: %q", bad)
		}
	}
	cal, err := Unmarshal([]byte(simpleXCal))
	if err != nil {
		t.Fatalf("valid decode after failures: %v", err)
	}
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, []byte(simpleXCal)) {
		t.Errorf("valid encode after failures differs")
	}
}

var invalidInputs = []string{
	``,
	`not xml at all`,
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0">`,
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties>`,
	`<?xml version="1.0"?><foo xmlns="urn:ietf:params:xml:ns:icalendar-2.0"/>`,
	`<icalendar/>`,
	`<icalendar xmlns="wrong-ns"><vcalendar/></icalendar>`,
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><unknown/></vcalendar></icalendar>`,
	// Property without value.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><uid></uid></properties></vcalendar></icalendar>`,
	// VALUE parameter forbidden in xCal.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-day><parameters><value><text>DATE</text></value></parameters><date>2020-01-01</date></x-day></properties></vcalendar></icalendar>`,
	// Bad boolean.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-b><boolean>yes</boolean></x-b></properties></vcalendar></icalendar>`,
	// Bad date.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><date>2020-13-40</date></x-d></properties></vcalendar></icalendar>`,
	// Bad date-time.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><date-time>2020-01-02T25:04:05Z</date-time></x-d></properties></vcalendar></icalendar>`,
	// Bad duration.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><duration>not-a-duration</duration></x-d></properties></vcalendar></icalendar>`,
	// Bad integer.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><integer>1.5</integer></x-d></properties></vcalendar></icalendar>`,
	// Bad float.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><float>nope</float></x-d></properties></vcalendar></icalendar>`,
	// Bad UTC offset.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><utc-offset>05:00</utc-offset></x-d></properties></vcalendar></icalendar>`,
	// Out-of-range UTC offset.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><utc-offset>+25:00</utc-offset></x-d></properties></vcalendar></icalendar>`,
	// Bad time.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><x-d><time>25:00:00</time></x-d></properties></vcalendar></icalendar>`,
	// Bad binary.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><attach><binary>!!!not-base64!!!</binary></attach></properties></vcalendar></icalendar>`,
	// GEO missing longitude.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><geo><latitude>1</latitude></geo></properties></vcalendar></icalendar>`,
	// REQUEST-STATUS missing description.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><request-status><code>2.0</code></request-status></properties></vcalendar></icalendar>`,
	// Period missing end/duration.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><freebusy><period><start>2020-01-02T15:00:00Z</start></period></freebusy></properties></vcalendar></icalendar>`,
	// Period with both end and duration.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><freebusy><period><start>2020-01-02T15:00:00Z</start><end>2020-01-02T16:00:00Z</end><duration>PT1H</duration></period></freebusy></properties></vcalendar></icalendar>`,
	// RECUR without FREQ.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><rrule><recur><count>5</count></recur></rrule></properties></vcalendar></icalendar>`,
	// RECUR with UNTIL and COUNT.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><rrule><recur><freq>DAILY</freq><until><date-time>2020-01-01T00:00:00Z</date-time></until><count>5</count></recur></rrule></properties></vcalendar></icalendar>`,
	// RECUR with bad BYDAY.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><rrule><recur><freq>WEEKLY</freq><byday>XYZ</byday></recur></rrule></properties></vcalendar></icalendar>`,
	// RECUR with unknown part.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><rrule><recur><freq>WEEKLY</freq><x-foo>bar</x-foo></recur></rrule></properties></vcalendar></icalendar>`,
	// Mixed value types.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><exdate><date-time>2020-01-02T03:04:05Z</date-time><date>2020-01-03</date></exdate></properties></vcalendar></icalendar>`,
	// Unexpected text in a container.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar>stray text</vcalendar></icalendar>`,
	// Duplicate properties containers.
	`<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties/><properties/></vcalendar></icalendar>`,
}

func TestInvalidInputs(t *testing.T) {
	for i, in := range invalidInputs {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Errorf("case %d: expected error for %q", i, in)
		}
	}
}

func TestInvalidEncodeInputs(t *testing.T) {
	if _, err := Marshal(nil); err == nil {
		t.Error("nil calendar must fail")
	}
	if _, err := Marshal(&ical.Calendar{Component: ical.NewComponent(ical.CompEvent)}); err == nil {
		t.Error("non-VCALENDAR root must fail")
	}

	cases := []struct {
		name  string
		value string
		setup func(p *ical.Prop)
	}{
		{"X-B", "maybe", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "BOOLEAN") }},
		{"X-D", "20201301", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "DATE") }},
		{"X-D", "2020-13-01T99:00:00Z", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "DATE-TIME") }},
		{"X-I", "1.2", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "INTEGER") }},
		{"X-F", "abc", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "FLOAT") }},
		{"X-DUR", "abc", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "DURATION") }},
		{"X-O", "0500", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "UTC-OFFSET") }},
		{"X-O2", "+2500", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "UTC-OFFSET") }},
		{"X-T", "250000", func(p *ical.Prop) { p.Params.Set(ical.ParamValue, "TIME") }},
		{ical.PropGeo, "1", func(p *ical.Prop) {}},
		{ical.PropRequestStatus, "2.0", func(p *ical.Prop) {}},
		{ical.PropFreeBusy, "20200102T150000Z", func(p *ical.Prop) {}},
		{ical.PropRecurrenceRule, "COUNT=5", func(p *ical.Prop) {}},
		{ical.PropRecurrenceRule, "FREQ=DAILY;FREQ=WEEKLY", func(p *ical.Prop) {}},
		{ical.PropRecurrenceRule, "FREQ=DAILY;X-FOO=bar", func(p *ical.Prop) {}},
		{ical.PropCategories, `a\`, func(p *ical.Prop) {}},
	}
	for _, c := range cases {
		cal := simpleCalendar()
		addBad := func() {
			p := ical.NewProp(c.name)
			p.Value = c.value
			c.setup(p)
			cal.Children[0].Props.Add(p)
		}
		addBad()
		if _, err := Marshal(cal); err == nil {
			t.Errorf("expected encode error for %s=%q", c.name, c.value)
		}
	}
}

func TestCrossCodecRoundTrip(t *testing.T) {
	const icalText = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example Corp.//Example Client//EN
BEGIN:VTIMEZONE
LAST-MODIFIED:20040110T032845Z
TZID:US/Eastern
BEGIN:DAYLIGHT
DTSTART:20000404T020000
RRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=4
TZNAME:EDT
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:20001026T020000
RRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10
TZNAME:EST
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
DTSTAMP:20060206T001121Z
DTSTART;TZID=US/Eastern:20060102T120000
DURATION:PT1H
RRULE:FREQ=DAILY;COUNT=5
RDATE;TZID=US/Eastern;VALUE=PERIOD:20060102T150000/PT2H
SUMMARY:Event #2
UID:00959BC664CA650E933C892C@example.com
END:VEVENT
END:VCALENDAR
`
	original, err := ical.NewDecoder(strings.NewReader(icalText)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	xmlData, err := Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	fromXML, err := Unmarshal(xmlData)
	if err != nil {
		t.Fatal(err)
	}
	var textOut bytes.Buffer
	if err := ical.NewEncoder(&textOut).Encode(fromXML); err != nil {
		t.Fatal(err)
	}
	reparsed, err := ical.NewDecoder(&textOut).Decode()
	if err != nil {
		t.Fatal(err)
	}

	if len(reparsed.Children) != len(original.Children) {
		t.Fatalf("children count = %d, want %d", len(reparsed.Children), len(original.Children))
	}
	for i, orig := range original.Children {
		got := reparsed.Children[i]
		if got.Name != orig.Name {
			t.Errorf("child %d name = %q, want %q", i, got.Name, orig.Name)
		}
		if len(got.Children) != len(orig.Children) {
			t.Errorf("component %s nested children = %d, want %d", got.Name, len(got.Children), len(orig.Children))
		}
	}
	ev := reparsed.Children[1]
	if ev.Props.Get(ical.PropRecurrenceRule).Value != "FREQ=DAILY;COUNT=5" {
		t.Errorf("RRULE = %q", ev.Props.Get(ical.PropRecurrenceRule).Value)
	}
	rdate := ev.Props.Get(ical.PropRecurrenceDates)
	if rdate.Params.Get(ical.ParamValue) != "PERIOD" || rdate.Value != "20060102T150000/PT2H" {
		t.Errorf("RDATE = %#v", rdate)
	}
	if rdate.Params.Get(ical.ParamTimezoneID) != "US/Eastern" {
		t.Errorf("RDATE TZID = %q", rdate.Params.Get(ical.ParamTimezoneID))
	}
	if ev.Props.Get(ical.PropDateTimeStart).Value != "20060102T120000" {
		t.Errorf("DTSTART = %q", ev.Props.Get(ical.PropDateTimeStart).Value)
	}
	daylight := reparsed.Children[0].Children[0]
	if daylight.Props.Get(ical.PropRecurrenceRule).Value != "FREQ=YEARLY;BYDAY=1SU;BYMONTH=4" {
		t.Errorf("daylight RRULE = %q", daylight.Props.Get(ical.PropRecurrenceRule).Value)
	}
}

func TestTriggerDateTime(t *testing.T) {
	const icalText = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//EN
BEGIN:VEVENT
UID:u@example.com
DTSTAMP:20200102T030405Z
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER;VALUE=DATE-TIME:20200101T000000Z
DESCRIPTION:Reminder
END:VALARM
END:VEVENT
END:VCALENDAR
`
	cal, err := ical.NewDecoder(strings.NewReader(icalText)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`<date-time>2020-01-01T00:00:00Z</date-time>`)) {
		t.Errorf("TRIGGER date-time not rendered:\n%s", string(out))
	}
	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	trigger := cal2.Children[0].Children[0].Props.Get(ical.PropTrigger)
	if trigger.Params.Get(ical.ParamValue) != "DATE-TIME" || trigger.Value != "20200101T000000Z" {
		t.Errorf("TRIGGER = %#v", trigger)
	}
}

func TestExdateRdateLists(t *testing.T) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//EN")
	ev := ical.NewComponent(ical.CompEvent)
	cal.Children = append(cal.Children, ev)

	ex := ical.NewProp(ical.PropExceptionDates)
	ex.Params.Set(ical.ParamValue, "DATE")
	ex.Value = "20200101,20200102"
	ev.Props.Add(ex)

	rd := ical.NewProp(ical.PropRecurrenceDates)
	rd.Value = "20200201T120000Z,20200202T120000Z"
	ev.Props.Add(rd)

	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Count(s, "<date>") != 2 || strings.Count(s, "<date-time>") != 2 {
		t.Errorf("list values not expanded:\n%s", s)
	}
	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	ev2 := cal2.Children[0]
	if got := ev2.Props.Get(ical.PropExceptionDates); got.Value != "20200101,20200102" || got.Params.Get(ical.ParamValue) != "DATE" {
		t.Errorf("EXDATE = %#v", got)
	}
	if got := ev2.Props.Get(ical.PropRecurrenceDates); got.Value != "20200201T120000Z,20200202T120000Z" {
		t.Errorf("RDATE = %#v", got)
	}
}

func TestPeriodWithEnd(t *testing.T) {
	cal := simpleCalendar()
	p := ical.NewProp(ical.PropFreeBusy)
	p.Value = "20200102T150000Z/20200102T160000Z"
	cal.Children[0].Props.Add(p)
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("<end>2020-01-02T16:00:00Z</end>")) {
		t.Errorf("period end missing:\n%s", string(out))
	}
	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := cal2.Children[0].Props.Get(ical.PropFreeBusy).Value; got != "20200102T150000Z/20200102T160000Z" {
		t.Errorf("FREEBUSY = %q", got)
	}
}

func TestRepeatedProperties(t *testing.T) {
	cal := simpleCalendar()
	ev := cal.Children[0]
	c1 := ical.NewProp(ical.PropComment)
	c1.SetText("first comment")
	ev.Props.Add(c1)
	c2 := ical.NewProp(ical.PropComment)
	c2.SetText("second comment")
	ev.Props.Add(c2)
	ex1 := ical.NewProp(ical.PropExceptionDates)
	ex1.Params.Set(ical.ParamValue, "DATE")
	ex1.Value = "20200101,20200102"
	ev.Props.Add(ex1)
	ex2 := ical.NewProp(ical.PropExceptionDates)
	ex2.Params.Set(ical.ParamValue, "DATE")
	ex2.Value = "20200301"
	ev.Props.Add(ex2)

	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Count(s, "<comment>") != 2 {
		t.Errorf("repeated COMMENT properties not preserved:\n%s", s)
	}
	if strings.Count(s, "<exdate>") != 2 {
		t.Errorf("expected two EXDATE properties, got:\n%s", s)
	}
	cal2, err := Unmarshal(out)
	if err != nil {
		t.Fatal(err)
	}
	exdates := cal2.Children[0].Props.Values(ical.PropExceptionDates)
	if len(exdates) != 2 {
		t.Fatalf("EXDATE property count = %d", len(exdates))
	}
	if exdates[0].Value != "20200101,20200102" || exdates[1].Value != "20200301" {
		t.Errorf("EXDATE values = %q, %q", exdates[0].Value, exdates[1].Value)
	}
}

func TestRecurUntil(t *testing.T) {
	cases := []struct {
		ical string
		xml  string
	}{
		{"FREQ=MONTHLY;UNTIL=20201231T235959Z;BYMONTHDAY=31", "<date-time>2020-12-31T23:59:59Z</date-time>"},
		{"FREQ=YEARLY;UNTIL=20201231", "<date>2020-12-31</date>"},
	}
	for _, c := range cases {
		cal := simpleCalendar()
		p := ical.NewProp(ical.PropRecurrenceRule)
		p.Value = c.ical
		cal.Children[0].Props.Add(p)
		out, err := Marshal(cal)
		if err != nil {
			t.Fatalf("encode %q: %v", c.ical, err)
		}
		if !bytes.Contains(out, []byte(c.xml)) {
			t.Errorf("xCal for %q missing %s:\n%s", c.ical, c.xml, string(out))
		}
		cal2, err := Unmarshal(out)
		if err != nil {
			t.Fatalf("decode %q: %v", c.ical, err)
		}
		if got := cal2.Children[0].Props.Get(ical.PropRecurrenceRule).Value; got != c.ical {
			t.Errorf("UNTIL roundtrip = %q, want %q", got, c.ical)
		}
	}
}

func TestExplicitRecurExtension(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><version><text>2.0</text></version><prodid><text>x</text></prodid></properties><components><vevent><properties><uid><text>u</text></uid><dtstamp><date-time>2020-01-01T00:00:00Z</date-time></dtstamp><x-rule><recur><freq>WEEKLY</freq><interval>2</interval><byhour>9</byhour></recur></x-rule></properties></vevent></components></vcalendar></icalendar>`
	cal, err := Unmarshal([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	p := cal.Children[0].Props.Get("X-RULE")
	if p == nil {
		t.Fatal("X-RULE missing")
	}
	if p.Params.Get(ical.ParamValue) != "RECUR" {
		t.Errorf("VALUE = %q, want RECUR", p.Params.Get(ical.ParamValue))
	}
	if p.Value != "FREQ=WEEKLY;INTERVAL=2;BYHOUR=9" {
		t.Errorf("recur value = %q", p.Value)
	}
	// Re-encode structurally.
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("<recur>")) {
		t.Errorf("explicit RECUR extension not structured:\n%s", string(out))
	}
}

func TestTextXMLProperty(t *testing.T) {
	const icalText = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:u@example.com\r\nDTSTAMP:20200101T000000Z\r\n" +
		"XML:<place xmlns=\"urn:example:place\"><name>Caf\\, n</name></place>\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, err := ical.NewDecoder(strings.NewReader(icalText)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "&lt;place") {
		t.Errorf("XML property must be inserted as markup:\n%s", s)
	}
	if !strings.Contains(s, "Caf, n") {
		t.Errorf("TEXT escaping must be removed:\n%s", s)
	}
}

func TestWrappedBinaryWhitespace(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0"><vcalendar><properties><version><text>2.0</text></version><prodid><text>x</text></prodid></properties><components><vevent><properties><uid><text>u</text></uid><dtstamp><date-time>2020-01-01T00:00:00Z</date-time></dtstamp><attach><binary>
SGVsbG8g
V29ybGQh
</binary></attach></properties></vevent></components></vcalendar></icalendar>`
	cal, err := Unmarshal([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	b, err := cal.Children[0].Props.Get(ical.PropAttach).Binary()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "Hello World!" {
		t.Errorf("binary = %q", string(b))
	}
}

func TestLexicalPreservation(t *testing.T) {
	// Numeric lexemes and durations are validated, not normalized.
	cal := simpleCalendar()
	ev := cal.Children[0]
	p := ical.NewProp("X-N")
	p.Params.Set(ical.ParamValue, "INTEGER")
	p.Value = "+007"
	ev.Props.Add(p)
	p2 := ical.NewProp("X-F")
	p2.Params.Set(ical.ParamValue, "FLOAT")
	p2.Value = "1.50"
	ev.Props.Add(p2)
	p3 := ical.NewProp("X-W")
	p3.Params.Set(ical.ParamValue, "DURATION")
	p3.Value = "P2W"
	ev.Props.Add(p3)
	p4 := ical.NewProp("X-D2")
	p4.Params.Set(ical.ParamValue, "DURATION")
	p4.Value = "+P1DT2H30M"
	ev.Props.Add(p4)

	out, err := Marshal(cal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"<integer>+007</integer>", "<float>1.50</float>", "<duration>P2W</duration>", "<duration>+P1DT2H30M</duration>"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s:\n%s", want, s)
		}
	}
}

func TestRFC6321ExampleB2XML(t *testing.T) {
	// Abridged version of RFC 6321 Appendix B.2.2.
	const doc = `<?xml version="1.0" encoding="utf-8" ?>
<icalendar xmlns="urn:ietf:params:xml:ns:icalendar-2.0">
  <vcalendar>
    <properties>
      <prodid><text>-//Example Inc.//Example Client//EN</text></prodid>
      <version><text>2.0</text></version>
    </properties>
    <components>
      <vtimezone>
        <properties>
          <tzid><text>US/Eastern</text></tzid>
        </properties>
        <components>
          <daylight>
            <properties>
              <dtstart><date-time>2000-04-04T02:00:00</date-time></dtstart>
              <rrule><recur><freq>YEARLY</freq><byday>1SU</byday><bymonth>4</bymonth></recur></rrule>
              <tzname><text>EDT</text></tzname>
              <tzoffsetfrom><utc-offset>-05:00</utc-offset></tzoffsetfrom>
              <tzoffsetto><utc-offset>-04:00</utc-offset></tzoffsetto>
            </properties>
          </daylight>
        </components>
      </vtimezone>
      <vevent>
        <properties>
          <dtstamp><date-time>2006-02-06T00:11:21Z</date-time></dtstamp>
          <dtstart>
            <parameters><tzid><text>US/Eastern</text></tzid></parameters>
            <date-time>2006-01-02T12:00:00</date-time>
          </dtstart>
          <duration><duration>PT1H</duration></duration>
          <rrule><recur><freq>DAILY</freq><count>5</count></recur></rrule>
          <rdate>
            <parameters><tzid><text>US/Eastern</text></tzid></parameters>
            <period>
              <start>2006-01-02T15:00:00</start>
              <duration>PT2H</duration>
            </period>
          </rdate>
          <uid><text>00959BC664CA650E933C892C@example.com</text></uid>
        </properties>
      </vevent>
    </components>
  </vcalendar>
</icalendar>
`
	cal, err := Unmarshal([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Children) != 2 || cal.Children[0].Name != ical.CompTimezone {
		t.Fatalf("unexpected children: %#v", cal.Children)
	}
	ev := cal.Children[1]
	if got := ev.Props.Get(ical.PropRecurrenceDates); got == nil || got.Value != "20060102T150000/PT2H" {
		t.Errorf("RDATE = %#v", ev.Props.Get(ical.PropRecurrenceDates))
	}
	if got := ev.Props.Get(ical.PropRecurrenceDates).Params.Get(ical.ParamValue); got != "PERIOD" {
		t.Errorf("RDATE VALUE = %q", got)
	}
	if got := ev.Props.Get(ical.PropDateTimeStart).Value; got != "20060102T120000" {
		t.Errorf("DTSTART = %q", got)
	}
}
