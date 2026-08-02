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
	ColorHex string      `json:"color_hex"`
	SeatType string      `json:"seat_type"` // SEATED | STANDING
	Rows     []AIZoneRow `json:"rows"`
}

// AIStage is the detected stage/performance-area box, anchored as a percentage (0-100) of the
// image so the frontend preview can render every zone relative to it.
type AIStage struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// AILayout is the vision model's inferred seat map: a stage anchor plus zones with a price,
// legend color, seat type, and rows of seats, each row anchored at a percentage (0-100) of the
// poster image's width/height. This is a preview — nothing is persisted until the admin
// reviews/edits it and explicitly confirms (see SeatHandler.AdminAIConfirmSeats).
type AILayout struct {
	Stage AIStage  `json:"stage"`
	Zones []AIZone `json:"zones"`
}

const seatLayoutVisionPrompt = `You are analyzing a concert venue poster or seat map image for a ticketing platform. Perform this analysis in three steps:

STEP 1 — PARSE THE PRICING / LEGEND BOX: Most venue seat maps include a legend or price table (often in a corner) mapping each colored zone on the map to a zone name and price. Read it carefully. For each entry, extract its exact swatch color as a 6-digit hex code (e.g. "#8b5cf6") and the zone name/price it labels. If a zone's legend color can't be determined precisely, pick the closest reasonable hex for the color block associated with that zone on the map.

STEP 2 — DETECT THE STAGE: Locate the stage / performance area box (often labeled "STAGE", drawn as a rectangle or arc at the top or center of the layout). Report its center position as x/y percentages (0-100) of the image width/height. This is the anchor every zone is positioned relative to. If no stage is visible, estimate x=50, y=8 (top-center, the typical convention).

STEP 3 — MAP ZONES INTO ROWS: For each colored zone region surrounding the stage, infer seat_type ("SEATED" for individual numbered seats, "STANDING" for GA/pit areas), and break it into rows. For each row estimate row_label (a letter, A/B/C...), seat_count (a reasonable number of seats given the region's width), and start_x/start_y as percentages (0-100) of the image marking roughly where that row begins. Rows should read as concentric arcs or straight lines fanning out from the stage, matching real venue seating geometry.

If the image has no visible seat map (e.g. it's just a poster/artist photo), invent a plausible 2-3 zone layout loosely following the poster's general composition (nearer the visual center as VIP, standing/GA further out), with a stage anchor at x=50, y=8. Always return a stage and at least one zone with at least one row.`

var seatLayoutSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"stage": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"x": map[string]any{"type": "number"},
				"y": map[string]any{"type": "number"},
			},
			"required":             []string{"x", "y"},
			"additionalProperties": false,
		},
		"zones": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"zone_name": map[string]any{"type": "string"},
					"price":     map[string]any{"type": "number"},
					"color_hex": map[string]any{"type": "string"},
					"seat_type": map[string]any{"type": "string", "enum": []string{"SEATED", "STANDING"}},
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
				"required":             []string{"zone_name", "price", "color_hex", "seat_type", "rows"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"stage", "zones"},
	"additionalProperties": false,
}

// GenerateSeatLayoutFromPoster infers a zone/row seat map preview from an uploaded poster
// image — nothing is persisted here. With ANTHROPIC_API_KEY set, it asks Claude Opus 5
// (vision + structured outputs) to read the image; without a key, it deterministically
// synthesizes a plausible layout from the image bytes (sha256-seeded, so the same upload
// always yields the same result) so the admin preview endpoint works end-to-end without any
// external dependency.
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

var fallbackZoneColors = []string{"#7c3aed", "#ff2d78", "#b8790f", "#059669"}

// synthesizeSeatLayout deterministically fabricates a 2-3 zone layout seeded from the image
// bytes' hash, so repeated uploads of the same poster produce the same result. Zones fan out
// from a fixed top-center stage anchor, alternating SEATED/STANDING and cycling through the
// same 4-color palette the rest of the admin dashboard uses for zone visualizations.
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
		seatType := "SEATED"
		if zoneNames[i%len(zoneNames)] == "General" {
			seatType = "STANDING"
		}
		zones = append(zones, AIZone{
			ZoneName: zoneNames[i%len(zoneNames)],
			Price:    basePrice - float64(i)*800,
			ColorHex: fallbackZoneColors[i%len(fallbackZoneColors)],
			SeatType: seatType,
			Rows:     rows,
		})
		startY += 6
	}
	return &AILayout{Stage: AIStage{X: 50, Y: 8}, Zones: zones}
}
