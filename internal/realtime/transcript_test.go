package realtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscriptAssembler_JoinsTokensIntoSentences(t *testing.T) {
	var a TranscriptAssembler
	assert.Empty(t, a.FeedAssistant("Hello", false))
	assert.Empty(t, a.FeedAssistant("there.", false), "a sentence at the end of the buffer waits for more")
	lines := a.FeedAssistant(" How can I help?", false)
	assert.Equal(t, []TranscriptLine{{Role: "assistant", Text: "Hello there."}}, lines)
	assert.Equal(t, []TranscriptLine{{Role: "assistant", Text: "How can I help?"}}, a.FlushAll())
}

func TestTranscriptAssembler_RoleSwitchClosesOtherRole(t *testing.T) {
	var a TranscriptAssembler
	a.FeedAssistant("What day works", false)
	lines := a.FeedUser("Monday", false)
	assert.Equal(t, []TranscriptLine{{Role: "assistant", Text: "What day works"}}, lines)
	assert.Equal(t, []TranscriptLine{{Role: "user", Text: "Monday"}}, a.FeedUser("", true))
}

func TestTranscriptAssembler_CumulativeRewrite(t *testing.T) {
	var a TranscriptAssembler
	a.FeedUser("I would", false)
	a.FeedUser("I would like a booking", false)
	assert.Equal(t, []TranscriptLine{{Role: "user", Text: "I would like a booking"}}, a.FlushAll())
}

func TestTranscriptAssembler_PunctuationJoinsWithoutSpace(t *testing.T) {
	var a TranscriptAssembler
	a.FeedUser("Monday", false)
	a.FeedUser(",", false)
	a.FeedUser("please", false)
	assert.Equal(t, "Monday, please", a.FlushAll()[0].Text)
}

func TestFarewellRE(t *testing.T) {
	for _, s := range []string{"Goodbye!", "Thanks, have a great day.", "Take care", "OK bye."} {
		assert.True(t, FarewellRE.MatchString(s), s)
	}
	for _, s := range []string{"Bye the way", "goodbyes", "I'd like to book", "maybe"} {
		assert.False(t, FarewellRE.MatchString(s), s)
	}
}

func TestTimeline_AlignsTracksOnWallClock(t *testing.T) {
	now := time.Unix(0, 0)
	tl := &Timeline{now: func() time.Time { return now }}
	tl.start = now

	tl.WriteAssistant([]int16{1, 1})
	now = now.Add(time.Second / RecordingRate * 10)
	tl.WriteUser([]int16{2})

	user, asst := tl.Finalize()
	require.Equal(t, len(user), len(asst))
	assert.Equal(t, []int16{1, 1}, asst[:2])
	assert.Equal(t, int16(2), user[10], "user audio lands at its wall-clock offset")
	assert.Equal(t, 11, len(user))
}
