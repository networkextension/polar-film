package film

// analyze_store.go — analyze_jobs persistence + the read/write helpers the
// M5 pipeline uses (segment text in, summary/tags/timeline out).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AnalyzeStep is one stage's result inside a job's steps_json.
type AnalyzeStep struct {
	Status string `json:"status"` // pending|running|done|failed|skipped
	Detail string `json:"detail,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// AnalyzeJob mirrors a row of analyze_jobs.
type AnalyzeJob struct {
	ID          string                  `json:"id"`
	WorkspaceID string                  `json:"workspace_id"`
	MediaID     string                  `json:"media_id"`
	Status      string                  `json:"status"` // queued|running|done|failed
	Steps       map[string]AnalyzeStep  `json:"steps"`
	Error       string                  `json:"error,omitempty"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
}

func (p *Plugin) createAnalyzeJob(ctx context.Context, wsID, mediaID string, steps map[string]AnalyzeStep) (AnalyzeJob, error) {
	j := AnalyzeJob{
		ID: newID("job_"), WorkspaceID: wsID, MediaID: mediaID,
		Status: "queued", Steps: steps,
	}
	stepsJSON, _ := json.Marshal(steps)
	err := p.DB.QueryRowContext(ctx, `
		INSERT INTO analyze_jobs (id, workspace_id, media_id, status, steps_json)
		VALUES ($1,$2,$3,'queued',$4) RETURNING created_at, updated_at`,
		j.ID, wsID, mediaID, stepsJSON).Scan(&j.CreatedAt, &j.UpdatedAt)
	return j, err
}

// hasActiveJob reports whether a queued/running job already exists for media.
func (p *Plugin) hasActiveJob(ctx context.Context, wsID, mediaID string) (string, bool) {
	var id string
	err := p.DB.QueryRowContext(ctx, `
		SELECT id FROM analyze_jobs
		WHERE workspace_id=$1 AND media_id=$2 AND status IN ('queued','running')
		ORDER BY created_at DESC LIMIT 1`, wsID, mediaID).Scan(&id)
	return id, err == nil
}

func (p *Plugin) updateJobStatus(ctx context.Context, id, status, errText string) error {
	_, err := p.DB.ExecContext(ctx,
		`UPDATE analyze_jobs SET status=$2, error=$3, updated_at=now() WHERE id=$1`,
		id, status, errText)
	return err
}

func (p *Plugin) updateJobSteps(ctx context.Context, id string, steps map[string]AnalyzeStep) error {
	stepsJSON, _ := json.Marshal(steps)
	_, err := p.DB.ExecContext(ctx,
		`UPDATE analyze_jobs SET steps_json=$2, updated_at=now() WHERE id=$1`, id, stepsJSON)
	return err
}

func (p *Plugin) getAnalyzeJob(ctx context.Context, wsID, id string) (AnalyzeJob, error) {
	var j AnalyzeJob
	var stepsJSON []byte
	err := p.DB.QueryRowContext(ctx, `
		SELECT id, workspace_id, media_id, status, steps_json, error, created_at, updated_at
		FROM analyze_jobs WHERE workspace_id=$1 AND id=$2`, wsID, id).
		Scan(&j.ID, &j.WorkspaceID, &j.MediaID, &j.Status, &stepsJSON, &j.Error, &j.CreatedAt, &j.UpdatedAt)
	if err != nil {
		return j, err
	}
	_ = json.Unmarshal(stepsJSON, &j.Steps)
	return j, nil
}

func (p *Plugin) latestJobForMedia(ctx context.Context, wsID, mediaID string) (AnalyzeJob, error) {
	var id string
	err := p.DB.QueryRowContext(ctx, `
		SELECT id FROM analyze_jobs WHERE workspace_id=$1 AND media_id=$2
		ORDER BY created_at DESC LIMIT 1`, wsID, mediaID).Scan(&id)
	if err != nil {
		return AnalyzeJob{}, err
	}
	return p.getAnalyzeJob(ctx, wsID, id)
}

// ── pipeline I/O ─────────────────────────────────────────────────────

// segmentLinesForLLM returns the movie's台词, optionally timestamped, capped
// at maxChars so we don't blow the model's context. Ordered by time.
func (p *Plugin) segmentLinesForLLM(ctx context.Context, wsID, mediaID string, withTime bool, maxChars int) (string, int, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT start_ms, text FROM subtitle_segments
		WHERE workspace_id=$1 AND media_id=$2 ORDER BY start_ms`, wsID, mediaID)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	var b strings.Builder
	n := 0
	for rows.Next() {
		var startMs int
		var text string
		if err := rows.Scan(&startMs, &text); err != nil {
			return "", 0, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		line := text
		if withTime {
			line = fmt.Sprintf("[%d] %s", startMs, text)
		}
		if b.Len()+len(line)+1 > maxChars {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		n++
	}
	return b.String(), n, rows.Err()
}

func (p *Plugin) updateMovieSummary(ctx context.Context, wsID, mediaID, summary string) error {
	_, err := p.DB.ExecContext(ctx,
		`UPDATE media_items SET summary=$3 WHERE workspace_id=$1 AND id=$2`, wsID, mediaID, summary)
	return err
}

// replaceAITimeline clears prior timeline rows for a movie and inserts the
// fresh beats in one tx (analyze regenerates the whole timeline).
func (p *Plugin) replaceAITimeline(ctx context.Context, wsID, mediaID string, beats []timelineBeat) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM media_timeline WHERE workspace_id=$1 AND media_id=$2`, wsID, mediaID); err != nil {
		return err
	}
	for _, b := range beats {
		et := strings.TrimSpace(b.EventType)
		if et == "" {
			et = "beat"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO media_timeline (id, workspace_id, media_id, start_ms, end_ms, event_type, description)
			VALUES ($1,$2,$3,$4,NULL,$5,$6)`,
			newID("tl_"), wsID, mediaID, b.StartMs, et, strings.TrimSpace(b.Description)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// listTimeline returns a movie's timeline beats ordered by time.
func (p *Plugin) listTimeline(ctx context.Context, wsID, mediaID string) ([]timelineRow, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, start_ms, event_type, description FROM media_timeline
		WHERE workspace_id=$1 AND media_id=$2 ORDER BY start_ms NULLS LAST`, wsID, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []timelineRow{}
	for rows.Next() {
		var r timelineRow
		var startMs sql.NullInt64
		if err := rows.Scan(&r.ID, &startMs, &r.EventType, &r.Description); err != nil {
			return nil, err
		}
		if startMs.Valid {
			v := int(startMs.Int64)
			r.StartMs = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type timelineRow struct {
	ID          string `json:"id"`
	StartMs     *int   `json:"start_ms,omitempty"`
	EventType   string `json:"event_type"`
	Description string `json:"description"`
}
