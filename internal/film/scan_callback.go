package film

// scan_callback.go — P1a (task-processing v2). Receives dock's signed completion
// callback for the film fleet pipeline and drives scan_status + chains stages:
//
//   film.extract done  → scan_status=extracted → submit film.analyze (ANE)
//   film.analyze done  → fetch SRT artifact → store subtitles → scan_status=done
//   any failed/cancel  → scan_status=failed
//
// Mirrors lawyer's diarize_callback.go / ocr_callback.go (HMAC verify + async).
// See doc/arch/task-processing-v2.md.

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	sdk "github.com/networkextension/polar-sdk"

	"github.com/gin-gonic/gin"
)

// extractOutput is the result shape the agent's film.extract skill returns.
// The manifest itself rides back as a "manifest" artifact (asset + download_url),
// since the agent runner only uploads artifacts AFTER the skill returns — so the
// asset id can't be in the result. forward_token is echoed so we can hand it to
// the analyze stage (which must fetch the audio back from the music library).
type extractOutput struct {
	AudioTrackID string `json:"audio_track_id"`
	ForwardToken string `json:"forward_token"`
	KeyframeCount int   `json:"keyframe_count"`
}

// POST /internal/v1/film/scan-callback (signed by dock).
func (p *Plugin) handleScanCallback(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body"})
		return
	}
	if !sdk.VerifyDockCallback(p.hmacKey, c.Request.Method, c.Request.URL.RequestURI(),
		c.GetHeader("X-Polar-Plugin-Timestamp"), c.GetHeader("X-Polar-Plugin-Sig"), body) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad signature"})
		return
	}
	var payload sdk.TaskCallbackPayload
	if err := json.Unmarshal(body, &payload); err != nil || payload.RequesterRef == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad payload"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true}) // ack fast; work runs detached

	mediaID := payload.RequesterRef
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		ws, err := p.workspaceOfMedia(ctx, mediaID)
		if err != nil || ws == "" {
			log.Printf("film scan-callback: media=%s workspace lookup failed: %v", mediaID, err)
			return
		}
		if payload.Status != "done" {
			_, _ = p.setScanStatus(ctx, ws, mediaID, "failed", firstNonEmpty(payload.Error, payload.Status))
			return
		}

		switch payload.Skill {
		case "film.extract":
			var out extractOutput
			_ = json.Unmarshal(payload.Result, &out)
			// The manifest (with the music-lib audio track id) comes back as an
			// artifact; the analyze stage pulls it + the audio it points at.
			manifest := pickArtifact(payload.Artifacts, "manifest")
			if manifest == nil || manifest.DownloadURL == "" {
				_, _ = p.setScanStatus(ctx, ws, mediaID, "failed", "extract 无 manifest 产物")
				return
			}
			_, _ = p.setScanStatus(ctx, ws, mediaID, "extracted", "转写排队")
			// Chain the ANE analyze stage (arm64 + Neural Engine only). It fetches
			// the audio back from the music library, so it needs the user's token.
			input, _ := json.Marshal(map[string]any{
				"media_id":      mediaID,
				"manifest_url":  manifest.DownloadURL,
				"workspace_id":  ws,
				"forward_token": out.ForwardToken,
			})
			constraints, _ := json.Marshal(map[string]any{"required_arch": "arm64", "needs_ane": true})
			if _, err := p.Dock.SubmitComputeTask(sdk.SubmitComputeTaskRequest{
				WorkspaceID:  ws,
				Skill:        "film.analyze",
				Input:        input,
				Constraints:  constraints,
				CallbackPath: "/internal/v1/film/scan-callback",
				RequesterRef: mediaID,
				AutoStart:    true,
			}); err != nil {
				log.Printf("film scan-callback: submit film.analyze media=%s: %v", mediaID, err)
				_, _ = p.setScanStatus(ctx, ws, mediaID, "failed", "submit analyze: "+err.Error())
			}

			// Feed the identity modeling layer from the extract manifest (best-effort,
			// independent of the analyze/subtitle chain above):
			//   voice — diarize the extracted audio → per-speaker voiceprints (lawyer pattern)
			//   face  — each detected face print → identity, referencing its keyframe screenshot
			if mf, ferr := fetchText(ctx, manifest.DownloadURL); ferr == nil {
				var m struct {
					AudioAssetID int64 `json:"audioAssetID"`
					Faces        []struct {
						TimeMs    int                          `json:"timeMs"`
						Box       struct{ X, Y, W, H float64 } `json:"box"`
						Embedding []float32                    `json:"embedding"`
					} `json:"faces"`
				}
				_ = json.Unmarshal([]byte(mf), &m)

				if m.AudioAssetID > 0 {
					_ = p.setMediaAudioAsset(ctx, ws, mediaID, m.AudioAssetID)
					din, _ := json.Marshal(map[string]any{"asset_id": m.AudioAssetID, "model_folder": p.diarizeModelFolder})
					if _, derr := p.Dock.SubmitComputeTask(sdk.SubmitComputeTaskRequest{
						WorkspaceID: ws, Skill: "speech.diarize", Input: din,
						CallbackPath: "/internal/v1/film/diarize-callback", RequesterRef: mediaID, AutoStart: true,
					}); derr != nil {
						log.Printf("film scan-callback: submit speech.diarize media=%s: %v", mediaID, derr)
					}
				} else {
					log.Printf("film scan-callback: media=%s manifest has no audioAssetID — skipping diarize", mediaID)
				}

				// Face → identity. Keyframes were uploaded to film screenshots during
				// extract; match each face to its frame by timecode for the source ref.
				if len(m.Faces) > 0 {
					shots, _ := p.listScreenshots(ctx, ws, mediaID)
					pushed := 0
					for _, f := range m.Faces {
						if len(f.Embedding) == 0 {
							continue
						}
						aid := nearestScreenshotAsset(shots, f.TimeMs)
						if err := p.identityAddFaceSample(ctx, ws, "vision_fp_r2", f.Embedding, aid,
							f.TimeMs, []float64{f.Box.X, f.Box.Y, f.Box.W, f.Box.H}, mediaID); err != nil {
							continue
						}
						pushed++
					}
					log.Printf("film scan-callback: media=%s faces=%d → identity (%d pushed)", mediaID, len(m.Faces), pushed)
				}
			}

		case "film.analyze":
			_, _ = p.setScanStatus(ctx, ws, mediaID, "analyzing", "落字幕中")
			srt := pickArtifact(payload.Artifacts, "srt")
			if srt == nil || srt.DownloadURL == "" {
				_, _ = p.setScanStatus(ctx, ws, mediaID, "failed", "analyze 无字幕产物")
				return
			}
			content, err := fetchText(ctx, srt.DownloadURL)
			if err != nil {
				_, _ = p.setScanStatus(ctx, ws, mediaID, "failed", "拉取字幕失败: "+err.Error())
				return
			}
			cues := parseCues(content)
			if len(cues) == 0 {
				_, _ = p.setScanStatus(ctx, ws, mediaID, "failed", "字幕解析为空")
				return
			}
			s := Subtitle{ID: newID("sub_"), WorkspaceID: ws, MediaID: mediaID, Lang: "en", Format: "srt", Source: "fleet"}
			if err := p.insertSubtitleWithSegments(ctx, s, cues); err != nil {
				_, _ = p.setScanStatus(ctx, ws, mediaID, "failed", "字幕落库失败: "+err.Error())
				return
			}
			if _, eerr := p.embedSegments(ctx, ws, s.ID); eerr != nil {
				log.Printf("film scan-callback: embed segments %s failed (keyword search still works): %v", s.ID, eerr)
			}
			_, _ = p.setScanStatus(ctx, ws, mediaID, "done", "字幕已就绪")
			log.Printf("film scan-callback: media=%s analyze done → %d subtitle cues", mediaID, len(cues))

		default:
			log.Printf("film scan-callback: unknown skill %q media=%s", payload.Skill, mediaID)
		}
	}()
}

// workspaceOfMedia resolves a media item's workspace (the callback is dock-signed,
// not user-scoped, so we look it up by id).
func (p *Plugin) workspaceOfMedia(ctx context.Context, id string) (string, error) {
	var ws string
	err := p.DB.QueryRowContext(ctx, `SELECT workspace_id FROM media_items WHERE id=$1`, id).Scan(&ws)
	return ws, err
}

// nearestScreenshotAsset returns the central-assets id (int64) of the screenshot
// whose ts_ms is closest to the face's timecode — the frame the face was seen in.
// nil if no screenshot has a usable numeric asset id.
func nearestScreenshotAsset(shots []Screenshot, tsMs int) *int64 {
	best, bestDiff := -1, 1<<62
	for i, s := range shots {
		if s.TsMs == nil {
			continue
		}
		d := *s.TsMs - tsMs
		if d < 0 {
			d = -d
		}
		if d < bestDiff {
			bestDiff, best = d, i
		}
	}
	if best < 0 {
		return nil
	}
	id, err := strconv.ParseInt(shots[best].AssetID, 10, 64)
	if err != nil {
		return nil
	}
	return &id
}

func pickArtifact(arts []sdk.ComputeTaskArtifact, kind string) *sdk.ComputeTaskArtifact {
	for i := range arts {
		if arts[i].Kind == kind {
			return &arts[i]
		}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// fetchText GETs a (signed) asset download URL and returns its body as text.
func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
