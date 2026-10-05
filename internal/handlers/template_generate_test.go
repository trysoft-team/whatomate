package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAITemplateDraft(t *testing.T) {
	t.Parallel()
	raw := "```json\n" + `{
		"name": "Order Shipped Update!",
		"display_name": "Order shipped",
		"category": "utility",
		"language": "en-US",
		"header_type": "text",
		"header_content": "Order {{order}} shipped",
		"body_content": "Hi {{name}}, your order {{order}} ships by {{date}}",
		"body_examples": ["Maria", "#48213"],
		"header_examples": ["#48213"],
		"footer_content": "Thanks for shopping with us today and always, we appreciate it!!",
		"buttons": [
			{"type": "URL", "text": "Track my order right now please", "url": "https://shop.example.com/track/{{id}}", "example": "https://shop.example.com/track/48213"},
			{"type": "URL", "text": "Bad", "url": "https://shop.example.com/{{a}}/x"},
			{"type": "PHONE_NUMBER", "text": "Call us", "phone_number": "+1 555 010 0000"},
			{"type": "COPY_CODE", "example": "SAVE20"},
			{"type": "QUICK_REPLY", "text": "Thanks"},
			{"type": "QUICK_REPLY", "text": "Too many"}
		],
		"notes": "Replace the tracking URL."
	}` + "\n```"

	d, err := parseAITemplateDraft(raw)
	require.NoError(t, err)
	assert.Equal(t, "order_shipped_update", d.Name)
	assert.Equal(t, "UTILITY", d.Category)
	assert.Equal(t, "en_US", d.Language)
	assert.Equal(t, "TEXT", d.HeaderType)
	assert.Equal(t, "Order {{1}} shipped", d.HeaderContent)
	assert.Equal(t, "Hi {{1}}, your order {{2}} ships by {{3}}.", d.BodyContent, "renumbered; trailing variable gets a full stop")
	assert.LessOrEqual(t, len([]rune(d.FooterContent)), aiTemplateMaxFooter)
	assert.Equal(t, []map[string]any{
		{"component": "body", "index": 1, "value": "Maria"},
		{"component": "body", "index": 2, "value": "#48213"},
		{"component": "body", "index": 3, "value": "sample 3"},
		{"component": "header", "index": 1, "value": "#48213"},
	}, d.SampleValues)
	assert.Equal(t, []map[string]any{
		{"type": "URL", "text": "Track my order right now", "url": "https://shop.example.com/track/{{1}}", "example": "https://shop.example.com/track/48213"},
		{"type": "PHONE_NUMBER", "text": "Call us", "phone_number": "+1 555 010 0000"},
		{"type": "QUICK_REPLY", "text": "Thanks"},
	}, d.Buttons, "mid-URL variable dropped; copy code is marketing-only; max 3")
	assert.Equal(t, "Replace the tracking URL.", d.Notes)
}

func TestParseAITemplateDraft_CategoriesAndFallbacks(t *testing.T) {
	t.Parallel()

	d, err := parseAITemplateDraft(`{"template":{"display_name":"Weekend Sale","category":"MARKETING","language":"pt-PT","header_type":"IMAGE","body_content":"Big weekend sale on everything!","buttons":[{"type":"COPY_CODE","example":"WEEKEND20"},{"type":"QUICK_REPLY","text":"Stop promotions"}]}}`)
	require.NoError(t, err)
	assert.Equal(t, "weekend_sale", d.Name, "name falls back to display name")
	assert.Equal(t, "pt_PT", d.Language)
	assert.Equal(t, "IMAGE", d.HeaderType, "media header kept; admin uploads the sample")
	assert.Empty(t, d.SampleValues)
	require.Len(t, d.Buttons, 2)
	assert.Equal(t, "COPY_CODE", d.Buttons[0]["type"])

	d, err = parseAITemplateDraft(`{"name":"otp","category":"AUTHENTICATION","language":"xx","code_expiration_minutes":10,"add_security_recommendation":true,"body_content":"ignored"}`)
	require.NoError(t, err)
	assert.Equal(t, "en", d.Language)
	assert.Empty(t, d.BodyContent)
	assert.Equal(t, 10, d.CodeExpirationMinutes)
	assert.True(t, d.AddSecurityRecommendation)
	assert.Equal(t, "OTP", d.Buttons[0]["type"])

	d, err = parseAITemplateDraft(`{"name":"x","category":"weird","header_type":"TEXT","header_content":"{{a}} and {{b}}","body_content":"Hello there"}`)
	require.NoError(t, err)
	assert.Equal(t, "UTILITY", d.Category)
	assert.Equal(t, "NONE", d.HeaderType, "TEXT header with two variables is dropped")

	for _, bad := range []string{
		"no json",
		`{"name":"x","body_content":"  "}`,
		`{"name":"x","body_content":"{{1}} is your order"}`,
		`{"name":"x","body_content":"Hi {{1}} and {{1}} again"}`,
		`{"name":"x","body_content":"Hi {{1}} {{2}} there"}`,
	} {
		_, err := parseAITemplateDraft(bad)
		assert.Error(t, err, bad)
	}
}

func TestUniqueTemplateName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "welcome", uniqueTemplateName("welcome", []string{"other"}))
	assert.Equal(t, "welcome_3", uniqueTemplateName("welcome", []string{"welcome", "welcome_2"}))
}
