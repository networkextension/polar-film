package film

import "testing"

func TestParseTimecode(t *testing.T) {
	cases := map[string]int{
		"00:01:23,000": 83000,
		"00:01:23.000": 83000,
		"01:02:03,500": 3723500,
		"00:00:01,250": 1250,
		"01:23.000":    83000, // VTT MM:SS
		"00:01:26.5":   86500, // single-digit frac → padded
	}
	for in, want := range cases {
		got, ok := parseTimecode(in)
		if !ok || got != want {
			t.Errorf("parseTimecode(%q)=%d,%v want %d", in, got, ok, want)
		}
	}
	if _, ok := parseTimecode("garbage"); ok {
		t.Error("garbage should not parse")
	}
}

func TestParseCues_SRT(t *testing.T) {
	srt := "1\n00:01:23,000 --> 00:01:26,000\n说好了一辈子，少一年都不行。\n\n" +
		"2\n00:01:30,000 --> 00:01:32,500\nLine two\ncontinued\n"
	cues := parseCues(srt)
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d: %+v", len(cues), cues)
	}
	if cues[0].StartMs != 83000 || cues[0].EndMs != 86000 || cues[0].Text != "说好了一辈子，少一年都不行。" {
		t.Fatalf("cue0 wrong: %+v", cues[0])
	}
	if cues[1].Idx != 2 || cues[1].Text != "Line two\ncontinued" {
		t.Fatalf("cue1 wrong (multi-line/idx): %+v", cues[1])
	}
}

func TestParseCues_VTT(t *testing.T) {
	vtt := "WEBVTT\n\nNOTE this is a comment\n\n" +
		"cue-1\n00:00:01.000 --> 00:00:04.000 align:start position:50%\nHello\n\n" +
		"00:00:05.000 --> 00:00:06.000\nWorld\n"
	cues := parseCues(vtt)
	if len(cues) != 2 {
		t.Fatalf("want 2 cues (header+note skipped), got %d: %+v", len(cues), cues)
	}
	if cues[0].StartMs != 1000 || cues[0].EndMs != 4000 || cues[0].Text != "Hello" {
		t.Fatalf("cue0 wrong (cue-id + settings): %+v", cues[0])
	}
	if cues[1].Text != "World" || cues[1].Idx != 2 {
		t.Fatalf("cue1 wrong: %+v", cues[1])
	}
}

func TestExtractSpeaker(t *testing.T) {
	cases := []struct{ in, speaker, rest string }{
		{"[Darcy] You must allow me.", "Darcy", "You must allow me."},
		{"[spk0] hello", "spk0", "hello"},
		{"[ Elizabeth ]  spaced", "Elizabeth", "spaced"},
		{"no prefix here", "", "no prefix here"},
		{"[unterminated text", "", "[unterminated text"},
		{"[] empty tag", "", "[] empty tag"},
		{"[OnlyTag]", "", "[OnlyTag]"}, // tag with no text → not a prefix
	}
	for _, c := range cases {
		sp, rest := extractSpeaker(c.in)
		if sp != c.speaker || rest != c.rest {
			t.Errorf("extractSpeaker(%q)=(%q,%q) want (%q,%q)", c.in, sp, rest, c.speaker, c.rest)
		}
	}
}

func TestParseCues_Speaker(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:03,000\n[Darcy] You must allow me.\n\n" +
		"2\n00:00:03,200 --> 00:00:05,000\nI am all astonishment.\n"
	cues := parseCues(srt)
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d", len(cues))
	}
	if cues[0].Speaker != "Darcy" || cues[0].Text != "You must allow me." {
		t.Fatalf("cue0 speaker/text wrong: %+v", cues[0])
	}
	if cues[1].Speaker != "" || cues[1].Text != "I am all astonishment." {
		t.Fatalf("cue1 should have no speaker: %+v", cues[1])
	}
}

func TestParseCues_SkipsEmptyAndGarbage(t *testing.T) {
	if got := parseCues("WEBVTT\n\n"); len(got) != 0 {
		t.Fatalf("header-only should yield 0 cues, got %d", len(got))
	}
	if got := parseCues(""); len(got) != 0 {
		t.Fatalf("empty should yield 0 cues, got %d", len(got))
	}
}
