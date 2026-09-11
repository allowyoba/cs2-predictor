package common

import "strings"

// TruncateForLog keeps a raw response body preview short and safe to embed
// in an error message or log line: bounded length, and any invalid UTF-8
// (an unexpectedly binary or corrupted response) replaced rather than
// passed through raw.
func TruncateForLog(body []byte) string {
	const max = 300
	if len(body) > max {
		body = body[:max]
	}
	return strings.ToValidUTF8(string(body), "�")
}
