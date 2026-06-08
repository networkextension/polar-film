package film

import "testing"

func TestStripCodeFences(t *testing.T) {
	cases := map[string]string{
		"```json\n[\"a\"]\n```": "[\"a\"]",
		"```\n[1,2]\n```":       "[1,2]",
		"[\"x\"]":               "[\"x\"]",
		"  plain  ":             "plain",
	}
	for in, want := range cases {
		if got := stripCodeFences(in); got != want {
			t.Errorf("stripCodeFences(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTagList(t *testing.T) {
	// fenced + prose around the array, plus dupes and blanks
	in := "好的,这是标签:\n```json\n[\"爱情\", \"悲剧\", \"爱情\", \"\", \"年代\"]\n```"
	got := parseTagList(in)
	want := []string{"爱情", "悲剧", "年代"}
	if len(got) != len(want) {
		t.Fatalf("parseTagList = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseTagList[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if len(parseTagList("not json at all")) != 0 {
		t.Errorf("non-JSON should yield no tags")
	}
}

func TestParseTimelineBeats(t *testing.T) {
	in := "```json\n[{\"start_ms\":1000,\"event_type\":\"开场\",\"description\":\"程蝶衣登场\"}," +
		"{\"start_ms\":5000,\"event_type\":\"\",\"description\":\"\"}]\n```"
	got := parseTimelineBeats(in)
	if len(got) != 1 { // the empty-description beat is dropped
		t.Fatalf("parseTimelineBeats len = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].StartMs != 1000 || got[0].Description != "程蝶衣登场" {
		t.Fatalf("beat = %+v", got[0])
	}
}

func TestExtractJSONArrayNoArray(t *testing.T) {
	if got := extractJSONArray("sorry, I cannot"); got != "[]" {
		t.Errorf("extractJSONArray(no array) = %q, want []", got)
	}
}

func TestIsAnalyzeStep(t *testing.T) {
	for _, s := range []string{"summary", "tags", "timeline"} {
		if !isAnalyzeStep(s) {
			t.Errorf("%q should be a valid step", s)
		}
	}
	if isAnalyzeStep("asr") {
		t.Errorf("asr should not be a valid step (yet)")
	}
}
