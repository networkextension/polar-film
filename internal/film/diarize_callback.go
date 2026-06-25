package film

// diarize_callback.go — film as a voice feeder of polar-identity. Receives dock's
// signed completion callback for the speech.diarize fleet task submitted in
// scan_callback (extract done), and pushes each speaker segment's wespeaker
// voiceprint to identity (unattributed; a human confirms onto a person later).
// Mirrors polar-lawyer's diarize_callback.go. captured_from="film:<media_id>";
// the movie's audio_asset_id (m11) is the recording reference.
// See doc/arch/identity-biometric-framework.md.

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	sdk "github.com/networkextension/polar-sdk"

	"github.com/gin-gonic/gin"
)

type filmDiarSegment struct {
	Speaker   string    `json:"speaker"`
	StartMs   int       `json:"start_ms"`
	EndMs     int       `json:"end_ms"`
	Embedding []float32 `json:"embedding"`
}
type filmDiarizeOutput struct {
	Model        string            `json:"model"`
	SpeakerCount int               `json:"speaker_count"`
	Segments     []filmDiarSegment `json:"segments"`
}

// POST /internal/v1/film/diarize-callback (signed by dock)
func (p *Plugin) handleDiarizeCallback(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 32<<20))
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if payload.Status != "done" {
			log.Printf("film diarize-callback: media=%s status=%s err=%s", mediaID, payload.Status, payload.Error)
			return
		}
		ws, assetID, err := p.mediaAudioAsset(ctx, mediaID)
		if err != nil || ws == "" {
			log.Printf("film diarize-callback: media=%s lookup failed: %v", mediaID, err)
			return
		}
		var out filmDiarizeOutput
		if err := json.Unmarshal(payload.Result, &out); err != nil {
			log.Printf("film diarize-callback: media=%s bad result: %v", mediaID, err)
			return
		}
		model := out.Model
		if model == "" {
			model = "wespeaker_v2"
		}
		var aid *int64
		if assetID > 0 {
			aid = &assetID
		}
		pushed := 0
		for _, seg := range out.Segments {
			if len(seg.Embedding) == 0 {
				continue
			}
			if err := p.identityAddVoiceSample(ctx, ws, model, seg.Embedding, aid,
				seg.StartMs, seg.EndMs, seg.Speaker, mediaID); err != nil {
				log.Printf("film diarize-callback: media=%s identity push failed: %v", mediaID, err)
				continue
			}
			pushed++
		}
		log.Printf("film diarize-callback: media=%s ws=%s speakers=%d voiceprints=%d → identity",
			mediaID, ws, out.SpeakerCount, pushed)
	}()
}
