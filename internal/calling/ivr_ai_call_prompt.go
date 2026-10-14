package calling

// aiCallPrompt is everything a realtime AI Call session needs from the node
// config and the organization's AI settings.
type aiCallPrompt struct {
	welcome      string
	system       string
	transferTeam string
	tools        []aiCallToolDef
	apiKey       string
	provider     string
	model        string
	maxTokens    int
	voice        string // node/org voice before mapping to a provider voice
}

// prepareAICallPrompt resolves the prompt, tools and credentials for an AI Call
// node. ok is false when the organization has no AI key configured.
func (m *Manager) prepareAICallPrompt(session *CallSession, node *IVRNode, ctx *IVRContext) (p aiCallPrompt, ok bool) {
	p.welcome, _ = node.Config["welcome_message"].(string)
	p.system, _ = node.Config["system_instructions"].(string)
	if p.system == "" {
		p.system, _ = node.Config["system_prompt"].(string)
	}
	p.transferTeam, _ = node.Config["transfer_team_id"].(string)
	p.tools = parseAICallTools(node.Config["tools"])

	settings := m.loadOrgAISettings(session.OrganizationID)
	p.apiKey, p.provider, p.model, p.maxTokens = m.resolveAILLMCreds(settings, "")
	if p.apiKey == "" {
		return p, false
	}
	if p.system == "" {
		p.system = settings.AI.SystemPrompt
	}
	if p.system == "" {
		p.system = "You are a helpful phone IVR assistant. Keep replies under 2 short sentences. Use tools when needed."
	}
	p.system = interpolateTemplate(p.system, ctx.Variables)
	p.system = m.composeAICallSystemPrompt(session, ctx, p.system, "", node.Config)
	p.voice = m.resolveSynthOpts(session.OrganizationID, node.Config).Voice
	p.welcome = interpolateTemplate(p.welcome, ctx.Variables)
	p.system, p.welcome = m.applyAITransferBusy(session, ctx, p.system, p.welcome)
	return p, true
}
