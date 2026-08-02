package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AIZoneRow struct {
	RowLabel  string  `json:"row_label"`
	SeatCount int     `json:"seat_count"`
	StartX    float64 `json:"start_x"`
	StartY    float64 `json:"start_y"`
}

type AIZone struct {
	ZoneName string      `json:"zone_name"`
	Price    float64     `json:"price"`
	Rows     []AIZoneRow `json:"rows"`
}

// AILayout is the vision model's inferred seat map: zones with a price and rows of seats,
// each row anchored at a percentage (0-100) of the poster image's width/height.
type AILayout struct {
	Zones []AIZone `json:"zones"`
}

const seatLayoutVisionPrompt = `You are analyzing a concert venue poster or seat map image for a ticketing platform. Identify the seating zones (e.g. VIP, CAT 1, General Admission) suggested by the image's layout, color blocks, or any legend/labels present.

For each zone, infer a plausible ticket price in Thai Baht (higher for zones that look closer to the stage or labeled VIP/premium) and a small number of rows. For each row, estimate row_label (a letter, A/B/C...), seat_count (a reasonable number of seats), and start_x/start_y as percentages (0-100) of the image width/height marking roughly where that row begins.

If the image has no visible seat map (e.g. it's just a poster/artist photo), invent a plausible 2-3 zone layout loosely following the poster's general composition (e.g. a zone nearer the visual center as VIP). Always return at least one zone with at least one row.`

var seatLayoutSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"zones": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"zone_name": map[string]any{"type": "string"},
					"price":     map[string]any{"type": "number"},
					"rows": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"row_label":  map[string]any{"type": "string"},
								"seat_count": map[string]any{"type": "integer"},
								"start_x":    map[string]any{"type": "number"},
								"start_y":    map[string]any{"type": "number"},
							},
							"required":             []string{"row_label", "seat_count", "start_x", "start_y"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"zone_name", "price", "rows"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"zones"},
	"additionalProperties": false,
}

// GenerateSeatLayoutFromPoster infers a zone/row seat map from an uploaded poster image.
// With ANTHROPIC_API_KEY set, it asks Claude Opus 5 (vision + structured outputs) to read
// the image; without a key, it deterministically synthesizes a plausible layout from the
// image bytes (sha256-seeded, so the same upload always yields the same result) so the
// admin endpoint works end-to-end without any external dependency.
func GenerateSeatLayoutFromPoster(ctx context.Context, imageBytes []byte, contentType string) (*AILayout, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return synthesizeSeatLayout(imageBytes), nil
	}
	return requestSeatLayoutFromVision(ctx, apiKey, imageBytes, contentType)
}

func requestSeatLayoutFromVision(ctx context.Context, apiKey string, imageBytes []byte, contentType string) (*AILayout, error) {
	mediaType := contentType
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		// supported as-is
	default:
		mediaType = "image/png"
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	encoded := base64.StdEncoding.EncodeToString(imageBytes)

	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     "claude-opus-5",
		MaxTokens: 2048,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.NewImageBlockBase64(mediaType, encoded),
				anthropic.NewTextBlock(seatLayoutVisionPrompt),
			),
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortMedium,
			Format: anthropic.JSONOutputFormatParam{Schema: seatLayoutSchema},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("vision request failed: %w", err)
	}

	var raw string
	for _, block := range message.Content {
		if block.Type == "text" {
			raw += block.Text
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("vision model returned no output (stop_reason=%s)", message.StopReason)
	}

	var layout AILayout
	if err := json.Unmarshal([]byte(raw), &layout); err != nil {
		return nil, fmt.Errorf("failed to parse vision model output: %w", err)
	}
	return &layout, nil
}

// synthesizeSeatLayout deterministically fabricates a 2-3 zone layout seeded from the
// image bytes' hash, so repeated uploads of the same poster produce the same result.
func synthesizeSeatLayout(imageBytes []byte) *AILayout {
	sum := sha256.Sum256(imageBytes)
	rng := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(sum[:8]))))

	zoneNames := []string{"VIP Zone A", "CAT 1", "CAT 2", "General"}
	zoneCount := 2 + rng.Intn(2) // 2-3 zones
	basePrice := 3000.0 + float64(rng.Intn(5))*1000

	zones := make([]AIZone, 0, zoneCount)
	startY := 15.0
	for i := range zoneCount {
		rowCount := 2 + rng.Intn(3)
		rows := make([]AIZoneRow, 0, rowCount)
		for r := range rowCount {
			rows = append(rows, AIZoneRow{
				RowLabel:  string(rune('A' + r)),
				SeatCount: 8 + rng.Intn(8),
				StartX:    12,
				StartY:    startY,
			})
			startY += 8
		}
		zones = append(zones, AIZone{
			ZoneName: zoneNames[i%len(zoneNames)],
			Price:    basePrice - float64(i)*800,
			Rows:     rows,
		})
		startY += 6
	}
	return &AILayout{Zones: zones}
}
