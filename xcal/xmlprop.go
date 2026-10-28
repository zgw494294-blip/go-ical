package xcal

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/emersion/go-ical"
)

// NewXMLProperty creates an iCalendar "XML" property (RFC 6321
// section 4.2) carrying a single well-formed XML element that belongs
// to a namespace other than the iCalendar namespace. The returned
// property stores the element using the base64 encoding allowed by the
// specification, so the data survives both xCal and text iCalendar
// round-trips.
func NewXMLProperty(rawXML string) (*ical.Prop, error) {
	if err := writeForeignXML(xml.NewEncoder(ioutil.Discard), rawXML, false); err != nil {
		return nil, err
	}
	prop := ical.NewProp(XMLPropertyName)
	prop.Params.Set(ical.ParamEncoding, "BASE64")
	prop.Params.Set(ical.ParamValue, string(ical.ValueBinary))
	prop.Value = base64.StdEncoding.EncodeToString([]byte(rawXML))
	return prop, nil
}

// XMLProperty returns the raw XML element carried by an "XML" property
// in its canonical textual form.
func XMLProperty(prop *ical.Prop) (string, bool) {
	if prop == nil || prop.Name != XMLPropertyName {
		return "", false
	}
	raw, err := xmlPropertyRaw(prop)
	if err != nil {
		return "", false
	}
	return raw, true
}

// xmlPropertyRaw extracts the foreign XML element text from an XML
// property, applying the ENCODING=BASE64 and TEXT pre-processing of
// RFC 6321 sections 3.1 and 4.2.
func xmlPropertyRaw(prop *ical.Prop) (string, error) {
	t := effectiveValueType(prop)
	value := prop.Value
	if strings.EqualFold(prop.Params.Get(ical.ParamEncoding), "BASE64") || t == ical.ValueBinary {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return "", fmt.Errorf("xcal: invalid base64 XML property value: %w", err)
		}
		value = string(decoded)
	} else {
		unescaped, err := unescapeText(value)
		if err != nil {
			return "", err
		}
		value = unescaped
	}
	return value, nil
}

// encodeXMLProperty inserts the foreign XML element into the xCal
// output. The markup is emitted as XML tokens, never escaped as plain
// text.
func (e *Encoder) encodeXMLProperty(prop *ical.Prop) error {
	raw, err := xmlPropertyRaw(prop)
	if err != nil {
		return err
	}
	return writeForeignXML(e.enc, raw, true)
}

const xmlnsNamespace = "http://www.w3.org/2000/xmlns/"

// sanitizeStart drops namespace declaration attributes: Go's XML
// decoder reports them in Attr, while the encoder re-derives xmlns
// bindings from Name.Space. Keeping both produces duplicate and
// malformed declarations.
func sanitizeStart(se xml.StartElement) xml.StartElement {
	attrs := make([]xml.Attr, 0, len(se.Attr))
	for _, a := range se.Attr {
		// Go's decoder reports xmlns declarations with Space "xmlns"
		// (or the formal xmlns namespace URL).
		if a.Name.Space == "xmlns" || a.Name.Space == xmlnsNamespace {
			continue
		}
		if a.Name.Space == "" && (a.Name.Local == "xmlns" || strings.HasPrefix(a.Name.Local, "xmlns:")) {
			continue
		}
		attrs = append(attrs, a)
	}
	se.Attr = attrs
	return se
}

func checkForeignName(name xml.Name) error {
	if name.Space == "" {
		return fmt.Errorf("xcal: XML property element must declare a namespace")
	}
	if name.Space == Namespace {
		return fmt.Errorf("xcal: XML property element must not be in the iCalendar namespace")
	}
	return nil
}

// writeForeignXML parses raw and copies exactly one top-level foreign
// element into out. When flush is true, the encoder is flushed after
// the copy (used for validation). Non-element content is only allowed
// as surrounding whitespace.
func writeForeignXML(out *xml.Encoder, raw string, flush bool) error {
	dec := xml.NewDecoder(strings.NewReader(raw))
	rootEmitted := false
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("xcal: malformed XML property value: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if rootEmitted {
					return fmt.Errorf("xcal: XML property value must contain a single element")
				}
				if err := checkForeignName(t.Name); err != nil {
					return err
				}
				rootEmitted = true
			}
			depth++
			if err := out.EncodeToken(sanitizeStart(t)); err != nil {
				return err
			}
		case xml.EndElement:
			depth--
			if depth < 0 {
				return fmt.Errorf("xcal: malformed XML property value: unmatched end element")
			}
			if err := out.EncodeToken(t); err != nil {
				return err
			}
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(t)) > 0 {
				return fmt.Errorf("xcal: XML property value must be a single element")
			}
			if err := out.EncodeToken(t); err != nil {
				return err
			}
		case xml.Comment:
			if depth == 0 {
				return fmt.Errorf("xcal: XML property value must be a single element")
			}
			if err := out.EncodeToken(t); err != nil {
				return err
			}
		case xml.ProcInst:
			return fmt.Errorf("xcal: XML property value must not contain processing instructions")
		case xml.Directive:
			return fmt.Errorf("xcal: XML property value must not contain markup directives")
		}
	}
	if !rootEmitted || depth != 0 {
		return fmt.Errorf("xcal: XML property value must contain a single complete element")
	}
	if flush {
		return out.Flush()
	}
	return nil
}

// captureForeignElement serializes the foreign element whose
// StartElement has just been read, consuming its whole subtree from
// dec. Expanded element/attribute names and content meaning are
// preserved; namespace prefixes may be normalized.
func captureForeignElement(dec *xml.Decoder, start xml.StartElement) (string, error) {
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)

	depth := 1
	if err := enc.EncodeToken(sanitizeStart(start)); err != nil {
		return "", err
	}
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return "", fmt.Errorf("xcal: truncated foreign XML element: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if err := enc.EncodeToken(sanitizeStart(t)); err != nil {
				return "", err
			}
		case xml.EndElement:
			if err := enc.EncodeToken(t); err != nil {
				return "", err
			}
			depth--
		case xml.CharData, xml.Comment:
			if err := enc.EncodeToken(t); err != nil {
				return "", err
			}
		case xml.ProcInst:
			if err := enc.EncodeToken(t); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("xcal: unexpected markup inside foreign XML element")
		}
	}
	if err := enc.Flush(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
