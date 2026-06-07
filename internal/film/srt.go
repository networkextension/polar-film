package film

// srt.go — subtitle parsing into line-level cues. Handles both SRT
// (HH:MM:SS,mmm) and WebVTT (HH:MM:SS.mmm, optional cue ids + cue settings,
// WEBVTT header). Pure (no I/O) so it's unit-tested directly.

import (
	"strconv"
	"strings"
)

type parsedCue struct {
	Idx     int
	StartMs int
	EndMs   int
	Text    string
}

// parseTimecode parses "HH:MM:SS,mmm" / "HH:MM:SS.mmm" / "MM:SS.mmm" / "SS"
// into milliseconds. Returns ok=false on garbage.
func parseTimecode(s string) (int, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, false
	}
	ms := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		frac := s[i+1:]
		s = s[:i]
		for len(frac) < 3 {
			frac += "0"
		}
		frac = frac[:3]
		v, err := strconv.Atoi(frac)
		if err != nil {
			return 0, false
		}
		ms = v
	}
	parts := strings.Split(s, ":")
	var h, m, sec int
	var err error
	switch len(parts) {
	case 3:
		if h, err = strconv.Atoi(parts[0]); err != nil {
			return 0, false
		}
		if m, err = strconv.Atoi(parts[1]); err != nil {
			return 0, false
		}
		if sec, err = strconv.Atoi(parts[2]); err != nil {
			return 0, false
		}
	case 2:
		if m, err = strconv.Atoi(parts[0]); err != nil {
			return 0, false
		}
		if sec, err = strconv.Atoi(parts[1]); err != nil {
			return 0, false
		}
	case 1:
		if sec, err = strconv.Atoi(parts[0]); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return ((h*3600+m*60+sec)*1000 + ms), true
}

// parseCues splits SRT/VTT content into cues. Blocks are separated by blank
// lines; the cue's timing line contains "-->". A numeric line directly above
// the timing line is treated as the index. Everything after the timing line
// (until the blank) is the text (joined with "\n"). Blocks without a timing
// line (e.g. the WEBVTT header, NOTE blocks) are skipped. Cues are
// re-indexed sequentially regardless of file-provided numbers.
func parseCues(content string) []parsedCue {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	var cues []parsedCue
	for _, block := range splitBlocks(content) {
		lines := strings.Split(block, "\n")
		tIdx := -1
		for i, ln := range lines {
			if strings.Contains(ln, "-->") {
				tIdx = i
				break
			}
		}
		if tIdx < 0 {
			continue // header / note / non-cue block
		}
		startS, endS, ok := strings.Cut(lines[tIdx], "-->")
		if !ok {
			continue
		}
		start, ok1 := parseTimecode(startS)
		// end may carry VTT cue settings after the timecode → take first field
		endField := strings.Fields(strings.TrimSpace(endS))
		if len(endField) == 0 {
			continue
		}
		end, ok2 := parseTimecode(endField[0])
		if !ok1 || !ok2 {
			continue
		}
		text := strings.TrimSpace(strings.Join(lines[tIdx+1:], "\n"))
		if text == "" {
			continue
		}
		cues = append(cues, parsedCue{Idx: len(cues) + 1, StartMs: start, EndMs: end, Text: text})
	}
	return cues
}

// splitBlocks splits on runs of one-or-more blank lines.
func splitBlocks(content string) []string {
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, ln := range strings.Split(content, "\n") {
		if strings.TrimSpace(ln) == "" {
			flush()
			continue
		}
		cur = append(cur, ln)
	}
	flush()
	return blocks
}
