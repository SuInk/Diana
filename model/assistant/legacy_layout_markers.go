package assistant

import (
	"regexp"
	"strings"
)

// Preserve code examples while consuming legacy layout controls in ordinary prose.
var legacyLayoutTokens = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~|`[^`\\n]*`|\\[[ \\t]*diana[ \\t]*-[ \\t]*(?:line|msg|br)[ \\t]*\\]")

func normalizeLegacyLayoutMarkers(reply string) string {
	return legacyLayoutTokens.ReplaceAllStringFunc(reply, func(token string) string {
		if !strings.HasPrefix(token, "[") {
			return token
		}
		if strings.Contains(token, "line") {
			return notificationLineMarker
		}
		return notificationSplitMarker
	})
}
