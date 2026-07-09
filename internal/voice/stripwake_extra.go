package voice

import (
	"strings"
)

// stripWakeWord checks whether the transcript opens with the wake word and, if
// so, returns the rest of the message. It also accepts the wake word at the end
// for short utterances like "reinicia Pepito", but does not allow arbitrary
// deep matches in the middle of long phrases.
func stripWakeWord(transcript, wake string) (string, bool) {
	wakeF := alnumFold(wake)
	if wakeF == "" {
		return "", false
	}
	toks := tokenizeFolded(transcript)
	if len(toks) == 0 {
		return "", false
	}

	leadLimit := wakeMaxLeadTokens - 1
	if leadLimit > len(toks)-2 {
		leadLimit = len(toks) - 2
	}
	for i := 0; i < len(toks) && i <= leadLimit; i++ {
		if wakeMatches(toks[i].folded, wakeF) {
			last := i
			for last+1 < len(toks) && wakeMatches(toks[last+1].folded, wakeF) {
				last++
			}
			rest := transcript[toks[last].byteEnd:]
			rest = strings.TrimLeft(rest, ` ,.:;!?¡¿-—'"*`)
			return strings.TrimSpace(rest), true
		}
	}

	if len(toks) > wakeMaxLeadTokens {
		return "", false
	}
	for i := len(toks) - 1; i >= 0; i-- {
		if wakeMatches(toks[i].folded, wakeF) {
			rest := strings.TrimSpace(transcript[:toks[i].byteStart])
			rest = strings.TrimRight(rest, ` ,.:;!?¡¿-—'"*`)
			return rest, true
		}
	}
	return "", false
}
