package llm

import "strings"

// VisibleAssistantText removes leading inline reasoning envelopes used by some
// compatible endpoints. Structured reasoning fields are handled separately.
// Tags quoted inside an answer or code block are ordinary visible text.
func VisibleAssistantText(text string) string {
	var filter VisibleTextFilter
	return filter.Push(text) + filter.Finish()
}

// VisibleTextFilter buffers only an undecided prefix or a possible closing
// tag, so neither a split opening tag nor an unfinished thinking block leaks.
type VisibleTextFilter struct {
	pending string
	closing string
	visible bool
}

func (f *VisibleTextFilter) Push(text string) string {
	if f.visible {
		return text
	}
	f.pending += text
	for {
		if f.closing != "" {
			end := strings.Index(lowerASCII(f.pending), f.closing)
			if end < 0 {
				if keep := len(f.closing) - 1; len(f.pending) > keep {
					f.pending = f.pending[len(f.pending)-keep:]
				}
				return ""
			}
			f.pending = f.pending[end+len(f.closing):]
			f.closing = ""
		}
		prefix := strings.TrimLeft(f.pending, " \t\r\n\ufeff")
		lower := lowerASCII(prefix)
		waiting := lower == ""
		for _, tag := range []string{"think", "thinking"} {
			opening := "<" + tag + ">"
			if strings.HasPrefix(lower, opening) {
				f.pending = prefix[len(opening):]
				f.closing = "</" + tag + ">"
				break
			}
			waiting = waiting || strings.HasPrefix(opening, lower)
		}
		if f.closing != "" {
			continue
		}
		if waiting {
			return ""
		}
		f.visible = true
		out := f.pending
		f.pending = ""
		return out
	}
}

func (f *VisibleTextFilter) Finish() string {
	// An incomplete reasoning envelope is not a final answer.
	f.pending = ""
	return ""
}

// Preserve byte offsets, including UTF-8 split across stream chunks.
func lowerASCII(text string) string {
	buf := []byte(text)
	for i, b := range buf {
		if b >= 'A' && b <= 'Z' {
			buf[i] = b + ('a' - 'A')
		}
	}
	return string(buf)
}
