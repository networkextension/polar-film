package film

// appearances_handlers.go — per-person navigation + timecode/EDL export (PF-13).
//
//   GET /api/film/movies/:id/people/:pid/appearances        {person, frames, lines}
//   GET /api/film/movies/:id/people/:pid/export?format=edl|txt&fps=25  (download)
//
// "Give me all of person X's shots." Face frames + their台词 are merged into
// appearance ranges and emitted as a CMX3600 EDL or a plain timecode list an
// editor can drop into Premiere/FCP/Resolve. No fps is stored, so EDL frame
// counts use the ?fps param (default 25). See doc/face-curation.md.

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// appearance-range tuning: a single face is a point in time → give it a small
// default clip; ranges closer than the merge gap fold into one "shot".
const (
	frameClipMs   = 2000
	mergeGapMs    = 3000
	defaultEDLFps = 25
)

// appearanceRange is a merged on-screen span with any overlapping台词.
type appearanceRange struct {
	StartMs int
	EndMs   int
	Texts   []string
}

func (p *Plugin) handlePersonAppearances(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))
	pid := strings.TrimSpace(c.Param("personId"))
	if !p.movieOK(c, wsID, mediaID) {
		return
	}
	name, ok, err := p.getPersonName(ctx, wsID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "person not found"})
		return
	}
	frames, err := p.listPersonFrames(ctx, wsID, mediaID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	lines, err := p.listPersonLines(ctx, wsID, mediaID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"person": gin.H{"id": pid, "name": name},
		"frames": frames,
		"lines":  lines,
	})
}

func (p *Plugin) handlePersonExport(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))
	pid := strings.TrimSpace(c.Param("personId"))
	if !p.movieOK(c, wsID, mediaID) {
		return
	}
	movie, err := p.getMovie(ctx, wsID, mediaID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	name, ok, err := p.getPersonName(ctx, wsID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "person not found"})
		return
	}
	frames, err := p.listPersonFrames(ctx, wsID, mediaID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	lines, err := p.listPersonLines(ctx, wsID, mediaID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ranges := mergeAppearances(frames, lines)

	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	if format == "" {
		format = "txt"
	}
	fps := defaultEDLFps
	if v := strings.TrimSpace(c.Query("fps")); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 120 {
			fps = n
		}
	}

	var body, ext string
	switch format {
	case "edl":
		body, ext = buildEDL(movie.Title, name, ranges, fps), "edl"
	case "txt":
		body, ext = buildTimecodeTxt(movie.Title, name, ranges), "txt"
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "format must be edl or txt"})
		return
	}

	fname := sanitizeFilename(movie.Title+"-"+name) + "." + ext
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		fname, url.PathEscape(fname)))
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(body))
}

// mergeAppearances folds face points (each a small clip) and台词 ranges into
// time-ordered appearance spans, merging spans within mergeGapMs of each other.
func mergeAppearances(frames []PersonFrame, lines []PersonLine) []appearanceRange {
	type iv struct {
		start, end int
		text       string
	}
	ivs := make([]iv, 0, len(frames)+len(lines))
	for _, f := range frames {
		if f.TsMs == nil {
			continue
		}
		ivs = append(ivs, iv{start: *f.TsMs, end: *f.TsMs + frameClipMs})
	}
	for _, l := range lines {
		end := l.EndMs
		if end < l.StartMs {
			end = l.StartMs
		}
		ivs = append(ivs, iv{start: l.StartMs, end: end, text: l.Text})
	}
	if len(ivs) == 0 {
		return []appearanceRange{}
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].start < ivs[j].start })

	out := []appearanceRange{}
	cur := appearanceRange{StartMs: ivs[0].start, EndMs: ivs[0].end}
	if ivs[0].text != "" {
		cur.Texts = append(cur.Texts, ivs[0].text)
	}
	for _, v := range ivs[1:] {
		if v.start <= cur.EndMs+mergeGapMs {
			if v.end > cur.EndMs {
				cur.EndMs = v.end
			}
			if v.text != "" {
				cur.Texts = append(cur.Texts, v.text)
			}
			continue
		}
		out = append(out, cur)
		cur = appearanceRange{StartMs: v.start, EndMs: v.end}
		if v.text != "" {
			cur.Texts = append(cur.Texts, v.text)
		}
	}
	out = append(out, cur)
	return out
}

// buildTimecodeTxt renders a human-readable appearance list (SRT-style stamps).
func buildTimecodeTxt(title, person string, ranges []appearanceRange) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s 出场时间码\n", title, person)
	fmt.Fprintf(&b, "# 共 %d 段\n\n", len(ranges))
	for i, r := range ranges {
		fmt.Fprintf(&b, "%d\t%s --> %s", i+1, msToSRT(r.StartMs), msToSRT(r.EndMs))
		if len(r.Texts) > 0 {
			fmt.Fprintf(&b, "\t%s", strings.Join(r.Texts, " / "))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// buildEDL renders a CMX3600 EDL: one event per appearance range, source
// timecodes = the range, record timecodes laid end-to-end on the timeline.
func buildEDL(title, person string, ranges []appearanceRange, fps int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TITLE: %s — %s\n", edlClean(title), edlClean(person))
	b.WriteString("FCM: NON-DROP FRAME\n")
	rec := 0
	for i, r := range ranges {
		dur := r.EndMs - r.StartMs
		if dur <= 0 {
			dur = frameClipMs
		}
		fmt.Fprintf(&b, "%03d  AX       V     C        %s %s %s %s\n",
			i+1,
			msToTC(r.StartMs, fps), msToTC(r.StartMs+dur, fps),
			msToTC(rec, fps), msToTC(rec+dur, fps))
		fmt.Fprintf(&b, "* FROM CLIP NAME: %s\n", edlClean(title))
		rec += dur
	}
	return b.String()
}

// msToSRT formats ms as HH:MM:SS,mmm (SRT timestamp).
func msToSRT(ms int) string {
	if ms < 0 {
		ms = 0
	}
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// msToTC formats ms as HH:MM:SS:FF at the given fps (CMX3600 timecode).
func msToTC(ms, fps int) string {
	if ms < 0 {
		ms = 0
	}
	if fps <= 0 {
		fps = defaultEDLFps
	}
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	s := ms / 1000
	ms -= s * 1000
	ff := ms * fps / 1000
	if ff >= fps {
		ff = fps - 1
	}
	return fmt.Sprintf("%02d:%02d:%02d:%02d", h, m, s, ff)
}

// edlClean strips characters that would break a single-line EDL field.
func edlClean(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// sanitizeFilename keeps a download name shell/header-safe (ascii-ish core; the
// Content-Disposition filename* carries the full UTF-8 name separately).
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '/' || r == '\\':
			b.WriteByte('-')
		default:
			// keep non-ascii (e.g. CJK names) — modern targets handle UTF-8
			if r > 127 {
				b.WriteRune(r)
			} else {
				b.WriteByte('-')
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "export"
	}
	return out
}
