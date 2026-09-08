package assistant

import "strings"

// Consume control syntax separately from ID validation. A failed lookup must
// not turn an internal control into user-visible text.
func consumeOutgoingReplyControl(text string) (string, string, bool) {
	rest := strings.TrimLeft(text, " \t\r\n")
	var id string
	count := 0
	for strings.HasPrefix(rest, replyMarkerPrefix) {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			break
		}
		id = rest[len(replyMarkerPrefix):end]
		rest = strings.TrimLeft(rest[end+1:], " \t\r\n")
		count++
	}
	if count == 0 {
		return "", text, false
	}
	if count != 1 {
		id = "" // Multiple targets are ambiguous; do not choose one implicitly.
	}
	return id, rest, true
}
