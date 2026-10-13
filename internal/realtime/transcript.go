package realtime

import (
	"regexp"
	"strings"
	"unicode"
)

// TranscriptLine is one sentence-level turn in an AI Call transcript.
type TranscriptLine struct {
	Role string // user | assistant
	Text string
}

var sentenceEnd = regexp.MustCompile(`[.!?…]["']?(?:\s+|$)`)

// FarewellRE matches a spoken goodbye. Live models often say farewell without
// calling end_call, so the caller would otherwise be left on an open line.
var FarewellRE = regexp.MustCompile(`(?i)\b(?:good\s*bye|goodbye|bye-?bye|` +
	`have a (?:great|good|nice|wonderful) (?:day|one|evening|night)|` +
	`take care)\b|\bbye[.!]`)

// TranscriptAssembler merges Live transcription fragments into sentences.
type TranscriptAssembler struct {
	user      string
	assistant string
}

// FeedUser adds caller speech; any open assistant sentence is closed first.
func (a *TranscriptAssembler) FeedUser(piece string, finished bool) []TranscriptLine {
	out := a.flush("assistant")
	a.user = mergeTranscriptPiece(a.user, piece)
	out = append(out, a.sentences("user")...)
	if finished {
		out = append(out, a.flush("user")...)
	}
	return out
}

// FeedAssistant adds model speech; any open caller sentence is closed first.
func (a *TranscriptAssembler) FeedAssistant(piece string, finished bool) []TranscriptLine {
	out := a.flush("user")
	a.assistant = mergeTranscriptPiece(a.assistant, piece)
	out = append(out, a.sentences("assistant")...)
	if finished {
		out = append(out, a.flush("assistant")...)
	}
	return out
}

// FlushAll closes both open sentences.
func (a *TranscriptAssembler) FlushAll() []TranscriptLine {
	return append(a.flush("user"), a.flush("assistant")...)
}

func (a *TranscriptAssembler) buf(role string) *string {
	if role == "user" {
		return &a.user
	}
	return &a.assistant
}

func (a *TranscriptAssembler) flush(role string) []TranscriptLine {
	b := a.buf(role)
	text := strings.TrimSpace(*b)
	*b = ""
	if text == "" {
		return nil
	}
	return []TranscriptLine{{Role: role, Text: text}}
}

func (a *TranscriptAssembler) sentences(role string) []TranscriptLine {
	b := a.buf(role)
	var done []TranscriptLine
	for {
		loc := sentenceEnd.FindStringIndex(*b)
		if loc == nil {
			break
		}
		// Wait for trailing whitespace so "UTC." at the end can still grow.
		if loc[1] >= len(*b) && !unicode.IsSpace(lastRune((*b)[loc[0]:loc[1]])) {
			break
		}
		if sent := strings.TrimSpace((*b)[:loc[1]]); sent != "" {
			done = append(done, TranscriptLine{Role: role, Text: sent})
		}
		*b = strings.TrimLeftFunc((*b)[loc[1]:], unicode.IsSpace)
	}
	return done
}

func mergeTranscriptPiece(buf, piece string) string {
	piece = strings.TrimSpace(piece)
	if piece == "" {
		return buf
	}
	if buf == "" {
		return piece
	}
	// Some models resend the whole growing utterance.
	if strings.HasPrefix(piece, buf) || strings.HasPrefix(buf, piece) {
		if len(piece) >= len(buf) {
			return piece
		}
		return buf
	}
	// Bare tokens ("is", "Monday,") need a word space.
	first := []rune(piece)[0]
	if (unicode.IsLetter(first) || unicode.IsDigit(first)) && !unicode.IsSpace(lastRune(buf)) {
		return buf + " " + piece
	}
	return buf + piece
}

func lastRune(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return 0
	}
	return r[len(r)-1]
}
