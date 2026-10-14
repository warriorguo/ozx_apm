package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
)

var errNoEventsArray = errors.New(`no "events" array`)

// decodeEventBatch returns the raw events of {"events":[...]}.
//
// A single malformed event (for example "fps":59,50 written by an old SDK
// under a German locale) makes the whole document invalid JSON. Instead of
// rejecting every event in the batch, fall back to a tolerant scan that
// splits the array into its top-level objects; each one is then parsed on
// its own and only the broken ones are rejected. salvaged reports that the
// fallback was used. An error means not even that worked: the payload is not
// an event batch at all.
func decodeEventBatch(body []byte) (events []json.RawMessage, salvaged bool, err error) {
	var req EventRequest
	err = json.Unmarshal(body, &req)
	if err == nil {
		return req.Events, false, nil
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		// Valid JSON with the wrong shape (e.g. "events" is not an array).
		return nil, false, err
	}

	events, scanErr := scanEventObjects(body)
	if scanErr != nil {
		return nil, false, err
	}
	return events, true, nil
}

// scanEventObjects finds the "events" array and returns the byte range of
// each top-level {...} element, honouring strings and escapes. Elements are
// not validated here.
func scanEventObjects(body []byte) ([]json.RawMessage, error) {
	key := bytes.Index(body, []byte(`"events"`))
	if key < 0 {
		return nil, errNoEventsArray
	}
	i := key + len(`"events"`)
	i = skipSpace(body, i)
	if i >= len(body) || body[i] != ':' {
		return nil, errNoEventsArray
	}
	i = skipSpace(body, i+1)
	if i >= len(body) || body[i] != '[' {
		return nil, errNoEventsArray
	}
	i++

	var events []json.RawMessage
	for {
		i = skipSpace(body, i)
		if i >= len(body) {
			return nil, errors.New("unterminated events array")
		}
		switch body[i] {
		case ']':
			return events, nil
		case ',':
			i++
			continue
		case '{':
			end, ok := matchObject(body, i)
			if !ok {
				return nil, errors.New("unterminated event object")
			}
			events = append(events, json.RawMessage(body[i:end]))
			i = end
		default:
			return nil, errors.New("unexpected data in events array")
		}
	}
}

// matchObject returns the index just past the '}' matching body[start] == '{'.
func matchObject(body []byte, start int) (int, bool) {
	depth := 0
	inString := false
	for i := start; i < len(body); i++ {
		c := body[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	return i
}
