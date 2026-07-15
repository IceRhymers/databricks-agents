package jsonconfig

// stripJSONC removes JSONC extensions from data so the result is plain
// JSON that encoding/json can parse. It strips, while respecting string
// literals and backslash escapes:
//
//   - `//` line comments (to end of line)
//   - `/* ... */` block comments (may span lines)
//   - trailing commas: a comma followed — skipping any whitespace — by
//     `}` or `]` is removed.
//
// Comment markers and commas inside JSON string literals are never
// touched (e.g. `"https://x.com"` and `"a, b"` pass through unchanged).
// Removed bytes are deleted (not blanked) — nothing downstream reads byte
// offsets, and the json.Unmarshal result is identical either way.
//
// Implementation is two byte-scans: first strip comments (which can hide
// a `}`/`]` behind `,/*x*/}` or `, // note`), then remove trailing commas
// against the comment-free intermediate. Empty, whitespace-only, and
// comment-free input pass through cleanly.
func stripJSONC(data []byte) []byte {
	return removeTrailingCommas(stripComments(data))
}

// stripComments removes `//` line comments and `/* */` block comments,
// tracking in-string state so comment markers inside string literals are
// preserved. Newlines terminating line comments are kept.
func stripComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]

		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}

		if c == '/' && i+1 < len(data) {
			switch data[i+1] {
			case '/':
				// Line comment: skip to (but keep) the newline.
				i += 2
				for i < len(data) && data[i] != '\n' {
					i++
				}
				i-- // outer loop's i++ lands back on '\n' (or past end)
				continue
			case '*':
				// Block comment: skip through the closing '*/'.
				i += 2
				for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
					i++
				}
				i++ // outer loop's i++ moves past the closing '/'
				continue
			}
		}

		out = append(out, c)
	}
	return out
}

// removeTrailingCommas deletes any comma that is followed — skipping only
// whitespace — by a `}` or `]`. Assumes comments have already been
// stripped. Tracks in-string state so commas inside string literals are
// preserved.
func removeTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]

		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}

		if c == ',' {
			j := i + 1
			for j < len(data) && isJSONSpace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue // drop the trailing comma
			}
		}

		out = append(out, c)
	}
	return out
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
