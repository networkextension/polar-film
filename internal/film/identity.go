package film

// identity.go — film as a feeder of polar-identity (the unified person/biometric
// store). Mirrors polar-lawyer's diarize_callback.go → /internal/v1/samples push.
// Film's pipeline derives, per movie: per-speaker voiceprints (from diarization)
// and per-face feature-prints (from keyframes). Each becomes an unattributed
// biometric_sample referencing its source clip; a human confirms it onto a person
// later. captured_from = "film:<media_id>" links the sample back to the movie.
// See doc/arch/identity-biometric-framework.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// identityAddSample writes one biometric sample (voice or face) to polar-identity
// via the server-to-server internal-token endpoint. Samples land unattributed
// (person_id nil) — recognition only suggests; a human confirms onto a person.
func (p *Plugin) identityAddSample(ctx context.Context, ws, modality, modelID string,
	vec []float32, sourceAssetID *int64, locator map[string]any, capturedFrom string) error {
	if p.identityBase == "" || p.identityToken == "" {
		return fmt.Errorf("identity not configured (POLAR_IDENTITY_BASE / _INTERNAL_TOKEN)")
	}
	loc, _ := json.Marshal(locator)
	body, _ := json.Marshal(map[string]any{
		"workspace_id":    ws,
		"modality":        modality, // "voice" | "face"
		"model_id":        modelID,
		"vector":          vec,
		"source_asset_id": sourceAssetID,
		"locator":         json.RawMessage(loc),
		"captured_from":   capturedFrom,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.identityBase+"/internal/v1/samples", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Polar-Internal-Token", p.identityToken)
	resp, err := p.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("identity HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// identityAddVoiceSample — one speaker segment's wespeaker voiceprint. locator
// references the recording asset span: {t0, t1, speaker}.
func (p *Plugin) identityAddVoiceSample(ctx context.Context, ws, model string,
	vec []float32, recordingAssetID *int64, t0, t1 int, speaker, mediaID string) error {
	return p.identityAddSample(ctx, ws, "voice", model, vec, recordingAssetID,
		map[string]any{"t0": t0, "t1": t1, "speaker": speaker}, "film:"+mediaID)
}

// identityAddFaceSample — one detected face's VNFeaturePrint. source_asset_id is
// the face crop; locator references the frame: {frame_ts, bbox:[x,y,w,h]}.
func (p *Plugin) identityAddFaceSample(ctx context.Context, ws, model string,
	vec []float32, cropAssetID *int64, frameTs int, bbox []float64, mediaID string) error {
	return p.identityAddSample(ctx, ws, "face", model, vec, cropAssetID,
		map[string]any{"frame_ts": frameTs, "bbox": bbox}, "film:"+mediaID)
}
