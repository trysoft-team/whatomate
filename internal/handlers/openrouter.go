package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trysoft/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

var (
	openAIChatURL       = "https://api.openai.com/v1/chat/completions"
	openRouterChatURL   = "https://openrouter.ai/api/v1/chat/completions"
	openRouterModelsURL = "https://openrouter.ai/api/v1/models"
)

// openRouterAutoFreeModel routes each request to an available free model.
const openRouterAutoFreeModel = "openrouter/free"

const (
	openRouterModelsTTL     = time.Hour
	openRouterRecommendMax  = 6
	openRouterUnknownSizeB  = 25 // assumed size for models whose id has no parameter count
	openRouterSmallModelMax = 8  // models at or below this size (billions) rank last
)

// openAICompatibleEndpoint returns the chat-completions URL and error label
// for providers that speak the OpenAI API.
func openAICompatibleEndpoint(provider models.AIProvider) (string, string) {
	if provider == models.AIProviderOpenRouter {
		return openRouterChatURL, "OpenRouter API error"
	}
	return openAIChatURL, "OpenAI API error"
}

// openRouterMaxOutputTokens caps max_tokens sent to OpenRouter. It rejects any
// request whose output cap exceeds the model's context window, so larger values
// are dropped and the model's own limit applies.
const openRouterMaxOutputTokens = 32768

// setOpenAICompatibleMaxTokens sets max_tokens on an OpenAI-style payload.
func setOpenAICompatibleMaxTokens(payload map[string]any, provider models.AIProvider, maxTokens int) {
	if maxTokens <= 0 || (provider == models.AIProviderOpenRouter && maxTokens > openRouterMaxOutputTokens) {
		return
	}
	payload["max_tokens"] = maxTokens
}

func setOpenAICompatibleHeaders(req *http.Request, settings *models.ChatbotSettings) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+settings.AI.APIKey)
	if settings.AI.Provider == models.AIProviderOpenRouter {
		req.Header.Set("X-Title", "Whatomate")
	}
}

// OpenRouterModel is a free OpenRouter model offered in AI settings.
type OpenRouterModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	SupportsJSON  bool   `json:"supports_json"`
	SupportsAudio bool   `json:"supports_audio"`
	Recommended   bool   `json:"recommended"`
}

type openRouterCatalogModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
	Architecture  struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
}

var openRouterModelsCache struct {
	sync.Mutex
	models  []OpenRouterModel
	fetched time.Time
}

// ListOpenRouterFreeModels returns OpenRouter's free chat models, best first.
func (a *App) ListOpenRouterFreeModels(r *fastglue.Request) error {
	if _, err := a.getOrgID(r); err != nil {
		return r.SendErrorEnvelope(fasthttp.StatusUnauthorized, "Unauthorized", nil, "")
	}
	list, err := a.openRouterFreeModels()
	if err != nil {
		a.Log.Warn("failed to load OpenRouter models", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusBadGateway, "Could not load models from OpenRouter", nil, "")
	}
	return r.SendEnvelope(map[string]any{"models": list})
}

func (a *App) openRouterFreeModels() ([]OpenRouterModel, error) {
	c := &openRouterModelsCache
	c.Lock()
	defer c.Unlock()
	if c.models != nil && time.Since(c.fetched) < openRouterModelsTTL {
		return c.models, nil
	}
	req, err := http.NewRequest(http.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter models: HTTP %d", resp.StatusCode)
	}
	var catalog struct {
		Data []openRouterCatalogModel `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	c.models = rankOpenRouterFreeModels(catalog.Data)
	c.fetched = time.Now()
	return c.models, nil
}

var (
	openRouterSizeRe = regexp.MustCompile(`(?:^|[-_/])(\d+(?:\.\d+)?)b(?:$|[-_:])`)
	// Specialised or non-conversational models: kept in the list but ranked last.
	openRouterSpecialised = []string{"code", "coder", "safety", "guard", "math", "-fin", "-sante", "embed", "poolside/"}
	// Reduced variants of a model family: ranked below the full-size model.
	openRouterReduced = []string{"small", "mini", "nano", "lite"}
	// Excluded outright: anonymous "stealth" models may log prompts for training,
	// and Inkling's free tier rejects API calls outside OpenRouter's listed agent apps.
	openRouterExcludedPrefixes = []string{"stealth/", "thinkingmachines/inkling"}
)

// rankOpenRouterFreeModels keeps free, text-only-output models and orders them
// for customer chat: the auto router first, then general models by size, with
// JSON-mode support as a tiebreaker, and specialised/tiny models last.
func rankOpenRouterFreeModels(catalog []openRouterCatalogModel) []OpenRouterModel {
	type scored struct {
		m     OpenRouterModel
		score float64
	}
	var auto *OpenRouterModel
	var list []scored
	for _, cm := range catalog {
		if cm.Pricing.Prompt != "0" || cm.Pricing.Completion != "0" {
			continue
		}
		if len(cm.Architecture.OutputModalities) != 1 || cm.Architecture.OutputModalities[0] != "text" {
			continue
		}
		id := strings.ToLower(cm.ID)
		if hasAnyPrefix(id, openRouterExcludedPrefixes) {
			continue
		}
		m := OpenRouterModel{
			ID:            cm.ID,
			Name:          strings.TrimSpace(strings.TrimSuffix(cm.Name, "(free)")),
			ContextLength: cm.ContextLength,
			SupportsJSON:  containsString(cm.SupportedParameters, "response_format"),
			SupportsAudio: containsString(cm.Architecture.InputModalities, "audio"),
		}
		if cm.ID == openRouterAutoFreeModel {
			m.Name = "Auto (best available free model)"
			m.Recommended = true
			auto = &m
			continue
		}
		size := openRouterModelSizeB(id)
		score := size
		if m.SupportsJSON {
			score += 10
		}
		if size <= openRouterSmallModelMax {
			score -= 1000
		}
		for _, s := range openRouterReduced {
			if strings.Contains(id, s) {
				score -= 15
				break
			}
		}
		for _, s := range openRouterSpecialised {
			if strings.Contains(id, s) {
				score -= 2000
				break
			}
		}
		list = append(list, scored{m, score})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].m.ContextLength > list[j].m.ContextLength
	})
	out := make([]OpenRouterModel, 0, len(list)+1)
	if auto != nil {
		out = append(out, *auto)
	}
	for i, s := range list {
		s.m.Recommended = i < openRouterRecommendMax && s.score > 0
		out = append(out, s.m)
	}
	return out
}

// openRouterModelSizeB reads the largest "<N>b" parameter count from a model id,
// e.g. "nemotron-3-super-120b-a12b" -> 120.
func openRouterModelSizeB(id string) float64 {
	best := 0.0
	for _, m := range openRouterSizeRe.FindAllStringSubmatch(id, -1) {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > best {
			best = v
		}
	}
	if best == 0 {
		return openRouterUnknownSizeB
	}
	return best
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
