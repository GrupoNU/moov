package parser

import (
	"net/mail"
	"strings"
)

// Address-header parsing.
//
// The rule this file follows: an address header must never cost a message. A
// From line that Go's parser rejects still contains a human-readable address
// most of the time, and the search index wants it. So the standard parser gets
// first go, and a permissive splitter picks up whatever it refuses.
//
// Corpus ew-008 puts encoded-words in the display names of an address header,
// which is the ordinary shape of non-English mail and must round-trip.

// parseAddressList parses a header value into addresses, recovering what it can.
func parseAddressList(value string, out *ParsedMessage) []Address {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	// The standard parser handles the RFC-conforming majority, including the
	// group syntax and quoted display names, and it applies RFC 2047 decoding to
	// display names through its own WordDecoder.
	parser := mail.AddressParser{WordDecoder: &headerDecoder}
	if addrs, err := parser.ParseList(value); err == nil {
		result := make([]Address, 0, len(addrs))
		for _, a := range addrs {
			result = append(result, cleanAddress(a.Name, a.Address, out))
		}
		if len(result) > 0 {
			return result
		}
	}

	// The permissive path. Split on commas that are not inside quotes or angle
	// brackets, then pull an addr-spec out of each fragment.
	var result []Address
	for _, field := range splitAddressFields(value) {
		if a, ok := salvageAddress(field, out); ok {
			result = append(result, a)
		}
	}
	if len(result) == 0 {
		// Nothing resembling an address. Keep the raw text as a display name so
		// the value is still searchable rather than silently dropped.
		decoded, defects := decodeHeaderValue(value, -1)
		for _, d := range defects {
			out.addDefect(d)
		}
		if decoded != "" {
			result = append(result, Address{Name: decoded})
		}
	}
	return result
}

// splitAddressFields splits an address list on top-level commas.
func splitAddressFields(v string) []string {
	var (
		fields   []string
		current  strings.Builder
		inQuote  bool
		inAngle  bool
		inEscape bool
	)
	for _, r := range v {
		switch {
		case inEscape:
			inEscape = false
		case r == '\\' && inQuote:
			inEscape = true
		case r == '"':
			inQuote = !inQuote
		case r == '<' && !inQuote:
			inAngle = true
		case r == '>' && !inQuote:
			inAngle = false
		case r == ',' && !inQuote && !inAngle:
			if s := strings.TrimSpace(current.String()); s != "" {
				fields = append(fields, s)
			}
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		fields = append(fields, s)
	}
	return fields
}

// salvageAddress extracts a name and an addr-spec from one malformed field.
func salvageAddress(field string, out *ParsedMessage) (Address, bool) {
	field = strings.TrimSpace(field)
	if field == "" {
		return Address{}, false
	}

	name, addr := "", ""
	if lt := strings.LastIndexByte(field, '<'); lt >= 0 {
		if gt := strings.IndexByte(field[lt:], '>'); gt >= 0 {
			addr = strings.TrimSpace(field[lt+1 : lt+gt])
			name = strings.TrimSpace(field[:lt])
		} else {
			addr = strings.TrimSpace(field[lt+1:])
			name = strings.TrimSpace(field[:lt])
		}
	} else if strings.ContainsRune(field, '@') {
		// A bare addr-spec, possibly with a trailing comment.
		addr = strings.TrimSpace(field)
		if i := strings.IndexAny(addr, " \t"); i >= 0 {
			// "user@example.com (Display Name)" — the RFC 822 comment form.
			rest := strings.TrimSpace(addr[i:])
			addr = addr[:i]
			name = strings.Trim(rest, "()")
		}
	} else {
		name = field
	}

	return cleanAddress(name, addr, out), addr != "" || name != ""
}

// cleanAddress decodes and sanitizes the two halves of an address.
func cleanAddress(name, addr string, out *ParsedMessage) Address {
	name = strings.TrimSpace(strings.Trim(strings.TrimSpace(name), `"`))
	if name != "" {
		name = joinEncodedWordFolds(name)
		decoded, defects := decodeHeaderValue(name, -1)
		name = decoded
		for _, d := range defects {
			out.addDefect(d)
		}
	}
	if clean, stripped := sanitizeText(addr); stripped {
		addr = clean
		out.addDefect(Defect{
			Code:   DefectNULStripped,
			Part:   -1,
			Detail: "NUL removed from address",
		})
	}
	return Address{Name: name, Address: strings.TrimSpace(addr)}
}
