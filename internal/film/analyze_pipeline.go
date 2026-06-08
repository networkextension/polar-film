package film

// analyze_pipeline.go — the M5 AI pipeline. Given a movie's already-ingested
// 台词 (subtitle segments), it asks the dock LLM proxy to produce a summary,
// genre/theme tags, and a plot timeline, writing each back into the knowledge
// base. Runs async in a goroutine; progress lands in analyze_jobs.steps_json.
//
// Each step is best-effort and isolated: one step failing (or no subtitles)
// marks that step failed/skipped without aborting the others. ASR/OCR (filling
// subtitles/OCR text from media we don't have) stays out of scope — the
// pipeline operates on text already in the DB.

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

const (
	analyzeMaxChars   = 8000 //台词 budget fed to the model per step
	analyzeStepTokens = 800
)

// analyzeStepNames is the canonical step set + run order.
var analyzeStepNames = []string{"summary", "tags", "timeline"}

// runAnalyzeJob executes the requested steps for a job. Detached context with
// its own deadline — the HTTP handler has already returned.
func (p *Plugin) runAnalyzeJob(job AnalyzeJob, cfgID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	steps := job.Steps
	_ = p.updateJobStatus(ctx, job.ID, "running", "")

	// 台词 corpus (plain + timestamped) — shared across steps.
	plain, nPlain, _ := p.segmentLinesForLLM(ctx, job.WorkspaceID, job.MediaID, false, analyzeMaxChars)
	timed, _, _ := p.segmentLinesForLLM(ctx, job.WorkspaceID, job.MediaID, true, analyzeMaxChars)

	run := func(name string, fn func() (AnalyzeStep, error)) {
		if _, want := steps[name]; !want {
			return
		}
		steps[name] = AnalyzeStep{Status: "running"}
		_ = p.updateJobSteps(ctx, job.ID, steps)
		st, err := fn()
		if err != nil {
			st.Status = "failed"
			if st.Detail == "" {
				st.Detail = err.Error()
			}
			log.Printf("film: analyze %s step %s failed: %v", job.ID, name, err)
		}
		steps[name] = st
		_ = p.updateJobSteps(ctx, job.ID, steps)
	}

	noSubs := nPlain == 0

	run("summary", func() (AnalyzeStep, error) {
		if noSubs {
			return AnalyzeStep{Status: "skipped", Detail: "no subtitles"}, nil
		}
		sys := "你是专业的电影内容分析助手。根据提供的电影台词片段,用简体中文写一段150字以内的剧情简介。只输出简介正文,不要任何前后缀或标题。"
		out, err := p.llmComplete(ctx, job.WorkspaceID, cfgID, sys, plain, "film.analyze.summary", analyzeStepTokens)
		if err != nil {
			return AnalyzeStep{}, err
		}
		summary := strings.TrimSpace(out)
		if err := p.updateMovieSummary(ctx, job.WorkspaceID, job.MediaID, summary); err != nil {
			return AnalyzeStep{}, err
		}
		// summary changes the movie's embeddable text → refresh its vector.
		p.embedMovieBestEffort(ctx, job.WorkspaceID, job.MediaID)
		return AnalyzeStep{Status: "done", Count: len([]rune(summary))}, nil
	})

	run("tags", func() (AnalyzeStep, error) {
		if noSubs {
			return AnalyzeStep{Status: "skipped", Detail: "no subtitles"}, nil
		}
		sys := "你是电影标签助手。根据台词提取3到6个能体现类型或主题的中文标签。只输出一个 JSON 字符串数组,例如 [\"爱情\",\"悲剧\",\"年代\"],不要输出其它任何文字。"
		// Same budget as the other steps: a thinking model can spend the
		// token allowance on reasoning before emitting the (tiny) JSON, so
		// a low cap returns empty content.
		out, err := p.llmComplete(ctx, job.WorkspaceID, cfgID, sys, plain, "film.analyze.tags", analyzeStepTokens)
		if err != nil {
			return AnalyzeStep{}, err
		}
		tags := parseTagList(out)
		if len(tags) == 0 {
			return AnalyzeStep{Status: "failed", Detail: "no tags parsed from model output"}, errors.New("empty tag list")
		}
		applied := 0
		for _, name := range tags {
			tagID, err := p.ensureTag(ctx, job.WorkspaceID, name, "ai")
			if err != nil {
				continue
			}
			if err := p.attachTag(ctx, job.WorkspaceID, job.MediaID, tagID, "ai"); err == nil {
				applied++
			}
		}
		// tags enrich the movie's embeddable text → refresh its vector.
		p.embedMovieBestEffort(ctx, job.WorkspaceID, job.MediaID)
		return AnalyzeStep{Status: "done", Count: applied}, nil
	})

	run("timeline", func() (AnalyzeStep, error) {
		if noSubs {
			return AnalyzeStep{Status: "skipped", Detail: "no subtitles"}, nil
		}
		sys := "你是剧情时间轴助手。下面是带毫秒时间戳的电影台词,格式为 [毫秒] 台词。提取3到8个关键剧情节点。只输出一个 JSON 数组,每个元素为 {\"start_ms\":整数毫秒,\"event_type\":\"简短类型\",\"description\":\"一句话中文描述\"},按时间升序。不要输出其它任何文字。"
		out, err := p.llmComplete(ctx, job.WorkspaceID, cfgID, sys, timed, "film.analyze.timeline", analyzeStepTokens)
		if err != nil {
			return AnalyzeStep{}, err
		}
		beats := parseTimelineBeats(out)
		if len(beats) == 0 {
			return AnalyzeStep{Status: "failed", Detail: "no beats parsed from model output"}, errors.New("empty timeline")
		}
		if err := p.replaceAITimeline(ctx, job.WorkspaceID, job.MediaID, beats); err != nil {
			return AnalyzeStep{}, err
		}
		return AnalyzeStep{Status: "done", Count: len(beats)}, nil
	})

	// Terminal status: failed only if every requested step failed.
	done, failed := 0, 0
	for _, name := range analyzeStepNames {
		st, ok := steps[name]
		if !ok {
			continue
		}
		switch st.Status {
		case "done", "skipped":
			done++
		case "failed":
			failed++
		}
	}
	final, errText := "done", ""
	if done == 0 && failed > 0 {
		final, errText = "failed", "all requested steps failed"
	}
	_ = p.updateJobStatus(ctx, job.ID, final, errText)
}
