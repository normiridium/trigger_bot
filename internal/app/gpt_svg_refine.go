package app

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"
)

const defaultSVGRefinePrompt = "Ты художник-контролёр. Ниже SVG-код и его рендер (PNG). Найди визуальные недочёты (несимметричность, смещения, выход за края, нелогичное расположение элементов) и верни исправленный SVG-код целиком в одном блоке, начинающемся со строки " + "```svg" + " и заканчивающемся " + "```" + ". Если недочётов нет, ответь ровно: OK."

// svgRefineEnabled reports whether the vision feedback loop is active.
func svgRefineEnabled() bool { return envBool("GPT_SVG_REFINE_ENABLED", false) }

func svgRefineMaxRounds() int {
	n := envInt("GPT_SVG_REFINE_MAX_ROUNDS", 2)
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	return n
}

func svgRefineTimeout() time.Duration {
	return time.Duration(envInt("GPT_SVG_REFINE_TIMEOUT_SEC", 90)) * time.Second
}

func svgRefineEffort() string {
	e := strings.ToLower(strings.TrimSpace(os.Getenv("GPT_SVG_REFINE_EFFORT")))
	switch e {
	case "low", "high", "max":
		return e
	default:
		return "low"
	}
}

// refineGPTSVGPNG runs the vision feedback loop: render the SVG, show the
// render to the vision model and ask it either to confirm (OK) or return a
// corrected SVG, re-render, repeat. It never blocks the pipeline: on any
// failure the last good PNG is returned.
//
// The returned token count is the sum of usage.total_tokens over all vision
// rounds, so the caller can charge the user's quota for them.
func refineGPTSVGPNG(svgSource string) ([]byte, int, error) {
	png, err := renderGPTSVGResponseToPNG(svgSource)
	if err != nil {
		return nil, 0, err
	}
	if !svgRefineEnabled() {
		return png, 0, nil
	}
	key := deepseekAPIKey()
	if key == "" {
		return png, 0, nil
	}
	totalTokens := 0
	rounds := svgRefineMaxRounds()
	for round := 0; round < rounds; round++ {
		log.Printf("svg refine round=%d", round+1)
		status, body, err := postChatCompletion(key, deepseekChatEndpoint(), svgRefinePayload(svgSource, png), svgRefineTimeout())
		if err != nil {
			log.Printf("svg refine: vision call failed: %v", err)
			break
		}
		if status < 200 || status >= 300 {
			log.Printf("svg refine: vision status=%d body=%s", status, clipText(sanitizeSecretText(string(body)), 300))
			break
		}
		var resp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || len(resp.Choices) == 0 {
			log.Printf("svg refine: parse response: %v", err)
			break
		}
		if resp.Usage.TotalTokens > 0 {
			totalTokens += resp.Usage.TotalTokens
		}
		out := strings.TrimSpace(resp.Choices[0].Message.Content)
		if out == "" || strings.EqualFold(out, "OK") {
			log.Printf("svg refine: vision approved render (round=%d)", round+1)
			break
		}
		newSVG, ok := extractFirstSVGFromGPTResponse(out)
		if !ok {
			log.Printf("svg refine: no svg block in vision reply (round=%d), keeping render", round+1)
			break
		}
		newPNG, err := renderGPTSVGResponseToPNG(newSVG)
		if err != nil {
			log.Printf("svg refine: corrected svg render failed: %v", err)
			break
		}
		png = newPNG
		svgSource = newSVG
		log.Printf("svg refine: applied corrected svg (round=%d)", round+1)
	}
	return png, totalTokens, nil
}

// svgRefinePayload builds the vision payload: the current SVG source plus its
// render as a base64 data URL. The vision model may answer OK or return a
// corrected SVG.
func svgRefinePayload(svgSource string, png []byte) map[string]interface{} {
	prompt := envOr("GPT_SVG_REFINE_PROMPT", defaultSVGRefinePrompt)
	text := prompt + "\n\nSVG-код:\n" + "```svg\n" + svgSource + "\n" + "```"
	return map[string]interface{}{
		"model":      deepseekMultimodalModel(),
		"effort":     svgRefineEffort(),
		"max_tokens": envInt("GPT_SVG_REFINE_MAX_TOKENS", 6000),
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
					{"type": "image_url", "image_url": map[string]string{
						"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
					}},
				},
			},
		},
	}
}
