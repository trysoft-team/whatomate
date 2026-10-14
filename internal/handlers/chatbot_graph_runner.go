package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/google/uuid"
	"github.com/trysoft/whatomate/internal/contactutil"
	"github.com/trysoft/whatomate/internal/crypto"
	wmgoogle "github.com/trysoft/whatomate/internal/google"
	"github.com/trysoft/whatomate/internal/mail"
	"github.com/trysoft/whatomate/internal/models"
	"github.com/trysoft/whatomate/pkg/whatsapp"
	"gorm.io/gorm"
)

// maxChatGraphIterations bounds non-blocking node chains within a single
// inbound message. A graph that loops through non-blocking nodes forever
// returns errChatGraphRunaway instead of wedging the webhook goroutine.
const maxChatGraphIterations = 100

var errChatGraphRunaway = errors.New("chat graph: too many non-blocking nodes in a single inbound (cycle?)")

// chatRunOpts carries optional inbound metadata for a single graph run.
// Tests may pass nil; the processor fills message type / media / location.
type chatRunOpts struct {
	MessageType       string
	MetaType          string // original WhatsApp message type (text, interactive, image, …)
	WhatsAppMessageID string
	Timestamp         string
	From              string
	ButtonID          string
	InteractiveType   string // button_reply, list_reply, nfm_reply
	FlowResponseData  map[string]any
	Media             *MediaInfo
	Latitude          float64
	Longitude         float64
	LocationName      string
	LocationAddress   string
	ForceEntry        bool
	FromFallback      bool
	// SystemResume is set for non-user continuations (e.g. delay timer) so
	// KeepSessionActive=false does not force a restart from the entry node.
	SystemResume bool
	// RawInbound is the Meta Cloud API webhook body (object/entry/changes/…)
	// scoped to this message. When set, {{{inbound}}} uses it as-is.
	RawInbound map[string]any
}

// chatNodeCtx carries the per-inbound execution state for a single
// runChatGraph call. userInput is the message text; buttonID is the
// payload of an interactive button reply (empty for text messages).
// consumed flips to true after the first blocking node treats the input
// as its outcome, so a later blocking node in the same run doesn't see
// stale input.
type chatNodeCtx struct {
	account          *models.WhatsAppAccount
	contact          *models.Contact
	session          *models.ChatbotSession
	userInput        string
	buttonID         string
	messageType      string
	flowResponseData map[string]any // form fields from a WhatsApp Flow submission
	consumed         bool
	sentOutbound     bool // true when a Message-category node sent this turn
	fromFallback     bool
}

// nodeOutcome is the return value of a node executor.
//   - outcome is the edge condition used to pick the next node ("default",
//     "button:foo", "http:2xx", ...). Empty means "no edge needed", and
//     when paired with yield=false collapses the session as terminal.
//   - yield=true means "stay at this node; persist state and return so the
//     next inbound resumes here." Used by blocking nodes that haven't
//     received their input yet (e.g. buttons sent, awaiting click).
//   - yield=false means "advance via resolveEdge(node, outcome)". Used by
//     non-blocking nodes (message, set_variable, ...) AND by blocking
//     nodes that have just consumed their input (e.g. buttons + buttonID).
type nodeOutcome struct {
	outcome string
	yield   bool
}

// runChatGraph executes the v2 graph for a session against a single inbound
// message. It chains through non-blocking nodes and stops at the first
// blocking node (or at a terminal node with no outgoing edges).
//
// On entry:
//   - When KeepSessionActive is false, every user inbound resets to the entry
//     node (SystemResume / FromFallback skip this). Session variables are cleared.
//   - If session.CurrentStep is empty (or opts.ForceEntry), execution starts
//     at graph.EntryNode; the inbound is treated as a flow trigger only (not
//     an answer to the first prompt/buttons).
//   - Otherwise, execution resumes at session.CurrentStep with the input
//     applied to that node.
func (a *App) runChatGraph(
	account *models.WhatsAppAccount,
	contact *models.Contact,
	session *models.ChatbotSession,
	flow *models.ChatbotFlow,
	userInput string,
	buttonID string,
	flowResponseData map[string]any,
	opts *chatRunOpts,
) error {
	graph, err := parseChatGraph(flow.Graph)
	if err != nil {
		return fmt.Errorf("parse chat graph: %w", err)
	}
	if graph == nil {
		return errors.New("flow has no v2 graph; legacy executor should have run")
	}

	msgType := ""
	fromFallback := false
	forceEntry := false
	systemResume := false
	if opts != nil {
		msgType = opts.MessageType
		fromFallback = opts.FromFallback
		forceEntry = opts.ForceEntry
		systemResume = opts.SystemResume
	}

	ctx := &chatNodeCtx{
		account:          account,
		contact:          contact,
		session:          session,
		userInput:        userInput,
		buttonID:         buttonID,
		messageType:      msgType,
		flowResponseData: flowResponseData,
		fromFallback:     fromFallback,
	}

	if session.CurrentFlowID == nil {
		session.CurrentFlowID = &flow.ID
	}

	// Keep session off: every user inbound starts from the entry node.
	// System resumes (delay timers) continue from the parked step.
	if !flow.KeepSessionActive && !systemResume && !fromFallback {
		session.CurrentStep = ""
		session.StepRetries = 0
		session.SessionData = models.JSONB{
			"_flow_id":   flow.ID.String(),
			"_flow_name": flow.Name,
		}
	}

	seedChatSessionBuiltins(session, contact, userInput, opts)

	if forceEntry || session.CurrentStep == "" {
		session.CurrentStep = graph.EntryNode
		session.StepRetries = 0
		// Inbound only triggered the flow (keyword / Keep-off restart / new
		// session) — don't treat it as an answer to the first prompt/buttons.
		ctx.userInput = ""
		ctx.buttonID = ""
		ctx.messageType = ""
		ctx.flowResponseData = nil
		if forceEntry {
			a.sendStartFallbackMessage(graph, ctx)
		}
	}

	for range maxChatGraphIterations {
		node := graph.Node(session.CurrentStep)
		if node == nil {
			a.Log.Error("chat graph node not found",
				"session", session.ID, "node_id", session.CurrentStep, "flow", flow.ID)
			return fmt.Errorf("node %q not found", session.CurrentStep)
		}

		if expr := stringFromConfig(node.Config, "skip_condition"); expr != "" {
			matched, err := evaluateConditionExpression(expr, session.SessionData)
			if err != nil {
				a.Log.Warn("skip_condition failed; ignoring",
					"node", node.ID, "session", session.ID, "expression", expr, "error", err)
			} else if matched {
				appendChatPath(session, node, "skipped")
				next := graph.ResolveEdge(node.ID, "default")
				if next == "" {
					return a.finishChatGraphSession(session, flow)
				}
				session.CurrentStep = next
				continue
			}
		}

		res, err := a.executeChatNode(node, ctx)
		if err != nil {
			_ = a.persistChatSession(session)
			if !ctx.fromFallback && chatInboundPresent(ctx) {
				return a.redirectChatGraphToEntry(account, contact, session, flow, userInput, buttonID, flowResponseData, opts)
			}
			return err
		}

		appendChatPath(session, node, res.outcome)

		if res.yield {
			if !ctx.fromFallback && !ctx.sentOutbound && chatInboundPresent(ctx) {
				return a.redirectChatGraphToEntry(account, contact, session, flow, userInput, buttonID, flowResponseData, opts)
			}
			// Transfer (and similar) may mark Completed before yielding — wipe
			// flow state when Keep session is off so nothing is retained.
			if session.Status == models.SessionStatusCompleted && (flow == nil || !flow.KeepSessionActive) {
				session.CurrentStep = ""
				session.StepRetries = 0
				session.CurrentFlowID = nil
				var path any
				if session.SessionData != nil {
					path = session.SessionData["__path__"]
				}
				session.SessionData = models.JSONB{}
				if path != nil {
					session.SessionData["__path__"] = path
				}
				a.ClearContactChatbotTracking(session.ContactID)
			}
			return a.persistChatSession(session)
		}

		if session.CurrentFlowID != nil && *session.CurrentFlowID != flow.ID {
			newFlow, err := a.getChatbotFlowByIDCached(account.OrganizationID, *session.CurrentFlowID)
			if err != nil {
				_ = a.persistChatSession(session)
				return fmt.Errorf("goto_flow: load target: %w", err)
			}
			if newFlow.Graph == nil {
				_ = a.persistChatSession(session)
				return errors.New("goto_flow: target flow has no v2 graph")
			}
			newGraph, err := parseChatGraph(newFlow.Graph)
			if err != nil {
				_ = a.persistChatSession(session)
				return fmt.Errorf("goto_flow: parse target graph: %w", err)
			}
			flow = newFlow
			graph = newGraph
			session.CurrentStep = newGraph.EntryNode
			continue
		}

		next := graph.ResolveEdge(node.ID, res.outcome)
		if next == "" {
			if isChatUserDrivenOutcome(res.outcome) && !ctx.fromFallback {
				return a.redirectChatGraphToEntry(account, contact, session, flow, userInput, buttonID, flowResponseData, opts)
			}
			return a.finishChatGraphSession(session, flow)
		}
		session.CurrentStep = next
	}

	_ = a.persistChatSession(session)
	return errChatGraphRunaway
}

// applyChatGraphTerminal marks the session completed, or — when the flow has
// KeepSessionActive — leaves it Active with CurrentFlowID + SessionData kept
// and CurrentStep cleared so the next inbound re-enters from the start node
// without wiping variables (list/buttons/WA Flow can then advance normally).
//
// When KeepSessionActive is false, every user inbound already restarts from
// entry; End also clears SessionData / CurrentFlowID so nothing is retained.
func (a *App) applyChatGraphTerminal(session *models.ChatbotSession, flow *models.ChatbotFlow) {
	if flow != nil && flow.KeepSessionActive {
		session.Status = models.SessionStatusActive
		session.CompletedAt = nil
		session.CurrentStep = ""
		session.StepRetries = 0
		if session.CurrentFlowID == nil {
			id := flow.ID
			session.CurrentFlowID = &id
		}
		return
	}
	session.Status = models.SessionStatusCompleted
	session.CurrentStep = ""
	session.StepRetries = 0
	session.CurrentFlowID = nil
	// Drop variables so the next inbound cannot reuse them, but keep the
	// audit trail (__path__) for session history / tests.
	var path any
	if session.SessionData != nil {
		path = session.SessionData["__path__"]
	}
	session.SessionData = models.JSONB{}
	if path != nil {
		session.SessionData["__path__"] = path
	}
}

// finishChatGraphSession applies terminal Keep-session rules and persists.
func (a *App) finishChatGraphSession(session *models.ChatbotSession, flow *models.ChatbotFlow) error {
	a.applyChatGraphTerminal(session, flow)
	if flow == nil || !flow.KeepSessionActive {
		a.ClearContactChatbotTracking(session.ContactID)
	}
	return a.persistChatSession(session)
}

func chatInboundPresent(ctx *chatNodeCtx) bool {
	if ctx == nil {
		return false
	}
	if ctx.userInput != "" || ctx.buttonID != "" {
		return true
	}
	switch ctx.messageType {
	case "image", "document", "video", "audio", "sticker", "location", "contacts":
		return true
	}
	return false
}

func isChatUserDrivenOutcome(outcome string) bool {
	if outcome == "unmatched" || outcome == "max_retries" {
		return true
	}
	return strings.HasPrefix(outcome, "button:")
}

func (a *App) redirectChatGraphToEntry(
	account *models.WhatsAppAccount,
	contact *models.Contact,
	session *models.ChatbotSession,
	flow *models.ChatbotFlow,
	userInput, buttonID string,
	flowResponseData map[string]any,
	opts *chatRunOpts,
) error {
	next := &chatRunOpts{FromFallback: true, ForceEntry: true}
	if opts != nil {
		next.MessageType = opts.MessageType
		next.MetaType = opts.MetaType
		next.WhatsAppMessageID = opts.WhatsAppMessageID
		next.Timestamp = opts.Timestamp
		next.From = opts.From
		next.ButtonID = opts.ButtonID
		next.InteractiveType = opts.InteractiveType
		next.FlowResponseData = opts.FlowResponseData
		next.Media = opts.Media
		next.Latitude = opts.Latitude
		next.Longitude = opts.Longitude
		next.LocationName = opts.LocationName
		next.LocationAddress = opts.LocationAddress
		next.RawInbound = opts.RawInbound
	}
	a.Log.Info("Chat graph fallback to entry",
		"session", session.ID, "flow", flow.ID, "from_step", session.CurrentStep)
	return a.runChatGraph(account, contact, session, flow, userInput, buttonID, flowResponseData, next)
}

func (a *App) sendStartFallbackMessage(graph *ChatGraph, ctx *chatNodeCtx) {
	if graph == nil || ctx == nil {
		return
	}
	entry := graph.Node(graph.EntryNode)
	if entry == nil || entry.Type != ChatNodeStart {
		return
	}
	msg := stringFromConfig(entry.Config, "fallback_message")
	if msg == "" {
		return
	}
	msg = processTemplate(msg, ctx.session.SessionData)
	if err := a.sendAndSaveTextMessage(ctx.account, ctx.contact, msg); err != nil {
		a.Log.Warn("Failed to send start fallback_message", "error", err, "session", ctx.session.ID)
		return
	}
	ctx.sentOutbound = true
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, msg, entry.ID)
}

// startNodeEscapeKeywords returns escape keywords configured on the Start node.
func startNodeEscapeKeywords(graph *ChatGraph) []string {
	if graph == nil {
		return nil
	}
	entry := graph.Node(graph.EntryNode)
	if entry == nil || entry.Config == nil {
		return nil
	}
	raw, ok := entry.Config["escape_keywords"]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func matchesEscapeKeyword(text string, keywords []string) bool {
	if text == "" || len(keywords) == 0 {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, kw := range keywords {
		if strings.ToLower(strings.TrimSpace(kw)) == lower {
			return true
		}
	}
	return false
}

// seedChatSessionBuiltins writes the per-turn template variables into
// SessionData. Whatomate canonical names (phone_number, contact_name) and
// whatsCRM aliases (senderMobile, senderName, senderMessage) are kept in
// sync so authors can use either family — including {{{triple}}} braces.
//
// inbound is the Meta Cloud API webhook body for the latest message (usable as
// {{{inbound}}} for the full JSON envelope, or nested paths like
// {{inbound.entry[0].changes[0].value.messages[0].text.body}}).
func seedChatSessionBuiltins(session *models.ChatbotSession, contact *models.Contact, userInput string, opts *chatRunOpts) {
	if session.SessionData == nil {
		session.SessionData = models.JSONB{}
	}
	session.SessionData["phone_number"] = session.PhoneNumber
	session.SessionData["senderMobile"] = session.PhoneNumber
	if contact != nil {
		session.SessionData["contact_name"] = contact.ProfileName
		session.SessionData["senderName"] = contact.ProfileName
	}
	if userInput != "" {
		session.SessionData["senderMessage"] = userInput
	}

	msgType := "text"
	if opts != nil && opts.MessageType != "" {
		msgType = opts.MessageType
	}
	metaType := msgType
	if opts != nil && opts.MetaType != "" {
		metaType = opts.MetaType
	}
	session.SessionData["inbound_message_type"] = msgType
	session.SessionData["senderMessageType"] = msgType

	if opts != nil && opts.Media != nil {
		if opts.Media.MediaURL != "" {
			session.SessionData["inbound_media_url"] = opts.Media.MediaURL
		}
		if opts.Media.MediaMimeType != "" {
			session.SessionData["inbound_media_mime"] = opts.Media.MediaMimeType
		}
		if opts.Media.MediaFilename != "" {
			session.SessionData["inbound_media_filename"] = opts.Media.MediaFilename
		}
	}
	if opts != nil && (msgType == "location" || metaType == "location") {
		session.SessionData["inbound_latitude"] = opts.Latitude
		session.SessionData["inbound_longitude"] = opts.Longitude
		if opts.LocationName != "" {
			session.SessionData["inbound_location_name"] = opts.LocationName
		}
		if opts.LocationAddress != "" {
			session.SessionData["inbound_location_address"] = opts.LocationAddress
		}
	}

	inbound := buildInboundMessageVar(session, contact, userInput, opts, metaType, msgType)
	session.SessionData["inbound"] = inbound
	// Keep __inbound__ for set_variable path lookups (whatsCRM Response Saver style).
	session.SessionData["__inbound__"] = inbound
}

// buildInboundMessageVar returns the Meta webhook envelope for {{{inbound}}}.
// Prefer opts.RawInbound (exact envelope from webhook intake). Otherwise
// synthesize a minimal envelope for tests and system resumes.
func buildInboundMessageVar(
	session *models.ChatbotSession,
	contact *models.Contact,
	userInput string,
	opts *chatRunOpts,
	metaType, msgType string,
) map[string]any {
	if opts != nil && len(opts.RawInbound) > 0 {
		return opts.RawInbound
	}

	from := session.PhoneNumber
	if opts != nil && opts.From != "" {
		from = opts.From
	}
	msg := map[string]any{
		"from": from,
		"type": metaType,
	}
	if opts != nil {
		if opts.WhatsAppMessageID != "" {
			msg["id"] = opts.WhatsAppMessageID
		}
		if opts.Timestamp != "" {
			msg["timestamp"] = opts.Timestamp
		}
	}

	switch metaType {
	case "text":
		body := userInput
		if body == "" {
			if s, ok := session.SessionData["senderMessage"].(string); ok {
				body = s
			}
		}
		msg["text"] = map[string]any{"body": body}
	case "image", "video", "audio", "document", "sticker":
		mediaObj := map[string]any{}
		if opts != nil && opts.Media != nil {
			if opts.Media.MediaMimeType != "" {
				mediaObj["mime_type"] = opts.Media.MediaMimeType
			}
			if opts.Media.MediaID != "" {
				mediaObj["id"] = opts.Media.MediaID
			}
			if opts.Media.MediaFilename != "" {
				mediaObj["filename"] = opts.Media.MediaFilename
			}
		}
		if userInput != "" && metaType != "audio" && metaType != "sticker" {
			mediaObj["caption"] = userInput
		}
		msg[metaType] = mediaObj
	case "location":
		loc := map[string]any{}
		if opts != nil {
			loc["latitude"] = opts.Latitude
			loc["longitude"] = opts.Longitude
			if opts.LocationName != "" {
				loc["name"] = opts.LocationName
			}
			if opts.LocationAddress != "" {
				loc["address"] = opts.LocationAddress
			}
		}
		msg["location"] = loc
	case "interactive", "button":
		interactive := map[string]any{}
		itype := "button_reply"
		if opts != nil && opts.InteractiveType != "" {
			itype = opts.InteractiveType
		} else if msgType == "list_reply" || msgType == "nfm_reply" {
			itype = msgType
		}
		interactive["type"] = itype
		switch itype {
		case "list_reply":
			row := map[string]any{"title": userInput}
			if opts != nil && opts.ButtonID != "" {
				row["id"] = opts.ButtonID
			}
			interactive["list_reply"] = row
		case "nfm_reply":
			nfm := map[string]any{"body": userInput}
			if opts != nil && opts.FlowResponseData != nil {
				if b, err := json.Marshal(opts.FlowResponseData); err == nil {
					nfm["response_json"] = string(b)
				}
			}
			interactive["nfm_reply"] = nfm
		default:
			btn := map[string]any{"title": userInput}
			if opts != nil && opts.ButtonID != "" {
				btn["id"] = opts.ButtonID
			}
			interactive["button_reply"] = btn
		}
		msg["type"] = "interactive"
		msg["interactive"] = interactive
	default:
		if userInput != "" {
			msg["text"] = map[string]any{"body": userInput}
		}
	}

	phoneID := ""
	displayPhone := ""
	entryID := ""
	profileName := ""
	if contact != nil {
		profileName = contact.ProfileName
	}
	contacts := []any{}
	if from != "" || profileName != "" {
		c := map[string]any{"wa_id": from}
		if profileName != "" {
			c["profile"] = map[string]any{"name": profileName}
		}
		contacts = append(contacts, c)
	}

	return map[string]any{
		"object": "whatsapp_business_account",
		"entry": []any{
			map[string]any{
				"id": entryID,
				"changes": []any{
					map[string]any{
						"value": map[string]any{
							"messaging_product": "whatsapp",
							"metadata": map[string]any{
								"display_phone_number": displayPhone,
								"phone_number_id":      phoneID,
							},
							"contacts": contacts,
							"messages": []any{msg},
						},
						"field": "messages",
					},
				},
			},
		},
	}
}

// executeChatNode dispatches by node type. Phase 1 implements only
// message, buttons, and end. Other types return an error until their
// PR lands.
func (a *App) executeChatNode(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	switch node.Type {
	case ChatNodeStart:
		// Always-present entry sentinel. No side effect — falls through.
		_ = node
		_ = ctx
		return nodeOutcome{outcome: "default"}, nil
	case ChatNodeMessage:
		return a.execChatMessage(node, ctx)
	case ChatNodeButtons:
		return a.execChatButtons(node, ctx)
	case ChatNodeList:
		return a.execChatList(node, ctx)
	case ChatNodePrompt:
		return a.execChatPrompt(node, ctx)
	case ChatNodeAPICall:
		return a.execChatAPICall(node, ctx)
	case ChatNodeCondition:
		return a.execChatCondition(node, ctx)
	case ChatNodeTiming:
		return a.execChatTiming(node, ctx)
	case ChatNodeSetVariable:
		return a.execChatSetVariable(node, ctx)
	case ChatNodeAIResponse:
		return a.execChatAIResponse(node, ctx)
	case ChatNodeTransfer:
		return a.execChatTransfer(node, ctx)
	case ChatNodeWebhook:
		return a.execChatWebhook(node, ctx)
	case ChatNodeGotoFlow:
		return a.execChatGotoFlow(node, ctx)
	case ChatNodeWhatsAppFlow:
		return a.execChatWhatsAppFlow(node, ctx)
	case ChatNodeWATemplate:
		return a.execChatWATemplate(node, ctx)
	case ChatNodeDelay:
		return a.execChatDelay(node, ctx)
	case ChatNodeResetSession:
		return a.execChatResetSession(node, ctx)
	case ChatNodeSetLabels:
		return a.execChatSetLabels(node, ctx)
	case ChatNodeDisableBot:
		return a.execChatDisableBot(node, ctx)
	case ChatNodeSendEmail:
		return a.execChatSendEmail(node, ctx)
	case ChatNodePhonebook:
		return a.execChatPhonebook(node, ctx)
	case ChatNodeGoogleService:
		return a.execChatGoogleService(node, ctx)
	case ChatNodeEnd:
		return a.execChatEnd(node, ctx)
	default:
		return nodeOutcome{outcome: "", yield: true},
			fmt.Errorf("chat node type %q not implemented in this phase", node.Type)
	}
}

// parseChatSendToPhones splits a templated send_to string into unique phones.
// Empty / whitespace-only input yields nil (caller should use session contact).
func parseChatSendToPhones(raw string, sessionData models.JSONB) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	rendered := strings.TrimSpace(processTemplate(raw, sessionData))
	if rendered == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, part := range strings.Split(rendered, ",") {
		phone := strings.TrimSpace(part)
		if phone == "" {
			continue
		}
		phone = strings.TrimPrefix(phone, "+")
		if _, ok := seen[phone]; ok {
			continue
		}
		seen[phone] = struct{}{}
		out = append(out, phone)
	}
	return out
}

// resolveChatSendContacts returns who a Message-group node should deliver to.
// Config key: "send_to" (aliases "to", "recipients") — optional, comma-separated,
// supports {{session}} templates. Empty → {{senderMobile}} (session contact).
func (a *App) resolveChatSendContacts(ctx *chatNodeCtx, node *ChatNode) ([]*models.Contact, error) {
	raw := strings.TrimSpace(stringFromConfig(node.Config, "send_to", "to", "recipients"))
	if raw == "" {
		raw = "{{senderMobile}}"
	}
	phones := parseChatSendToPhones(raw, ctx.session.SessionData)
	if len(phones) == 0 {
		// senderMobile missing from session — fall back to the active contact.
		if ctx.contact != nil && ctx.contact.PhoneNumber != "" {
			return []*models.Contact{ctx.contact}, nil
		}
		return nil, fmt.Errorf("send_to resolved to no phone numbers")
	}
	out := make([]*models.Contact, 0, len(phones))
	for _, phone := range phones {
		c, _, err := contactutil.GetOrCreateContact(a.DB, ctx.account.OrganizationID, phone, "")
		if err != nil {
			return nil, fmt.Errorf("resolve send_to %q: %w", phone, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// forEachChatSendContact runs fn for every resolved recipient of a Message-group node.
func (a *App) forEachChatSendContact(ctx *chatNodeCtx, node *ChatNode, fn func(*models.Contact) error) error {
	contacts, err := a.resolveChatSendContacts(ctx, node)
	if err != nil {
		return err
	}
	for _, c := range contacts {
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

// execChatMessage sends a text/media/location message. By default it
// falls through (fire-and-forget). Config:
//
//	{
//	  "media_type": "text"|"image"|"audio"|"video"|"document"|"location", // default text
//	  "message"|"text": "...",          // text body (text type)
//	  "preview_url": true,              // Meta link preview for text
//	  "media_url": "https://...",       // public link for media types
//	  "caption": "...",                 // image/video/document caption
//	  "filename": "...",                // document filename hint
//	  "latitude": -1.2, "longitude": 36.8, "name": "...", "address": "...",
//	  "send_to": "1555...,{{manager}}", // optional; comma-separated override recipients
//	  "wait_for_reply": true,           // yield until next inbound (moveToNextNode:false)
//	  "store_as": "senderMessage"       // when wait_for_reply; default senderMessage
//	}
func (a *App) execChatMessage(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	wait := boolFromConfig(node.Config, "wait_for_reply")
	if wait && !ctx.consumed && chatInboundPresent(ctx) {
		ctx.consumed = true
		storeAs := stringFromConfig(node.Config, "store_as")
		if storeAs == "" {
			storeAs = "senderMessage"
		}
		if ctx.session.SessionData == nil {
			ctx.session.SessionData = models.JSONB{}
		}
		value := ctx.userInput
		if value == "" {
			value = ctx.buttonID
		}
		if value == "" {
			value = ctx.messageType
		}
		ctx.session.SessionData[storeAs] = value
		return nodeOutcome{outcome: "default"}, nil
	}

	mediaType := stringFromConfig(node.Config, "media_type")
	if mediaType == "" {
		mediaType = "text"
	}

	logPreview := ""
	switch mediaType {
	case "text", "":
		text := stringFromConfig(node.Config, "message", "text")
		if text == "" && !wait {
			return nodeOutcome{outcome: "default"}, nil
		}
		if text == "" {
			return nodeOutcome{}, fmt.Errorf("message node %q has no text configured", node.ID)
		}
		text = processTemplate(text, ctx.session.SessionData)
		preview := boolFromConfig(node.Config, "preview_url")
		if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
			return a.sendAndSaveTextMessageOpts(ctx.account, c, text, preview)
		}); err != nil {
			return nodeOutcome{}, fmt.Errorf("send message: %w", err)
		}
		logPreview = text

	case "image", "audio", "video", "document":
		link := processTemplate(stringFromConfig(node.Config, "media_url", "link"), ctx.session.SessionData)
		if link == "" {
			return nodeOutcome{}, fmt.Errorf("message node %q missing media_url", node.ID)
		}
		caption := processTemplate(stringFromConfig(node.Config, "caption"), ctx.session.SessionData)
		filename := processTemplate(stringFromConfig(node.Config, "filename"), ctx.session.SessionData)
		if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
			return a.sendAndSaveMediaByLink(ctx.account, c, mediaType, link, caption, filename)
		}); err != nil {
			return nodeOutcome{}, fmt.Errorf("send %s: %w", mediaType, err)
		}
		logPreview = caption
		if logPreview == "" {
			logPreview = link
		}

	case "location":
		lat := floatFromConfig(node.Config, "latitude")
		lng := floatFromConfig(node.Config, "longitude")
		name := processTemplate(stringFromConfig(node.Config, "name", "location_name"), ctx.session.SessionData)
		address := processTemplate(stringFromConfig(node.Config, "address", "location_address"), ctx.session.SessionData)
		if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
			return a.sendAndSaveLocationMessage(ctx.account, c, lat, lng, name, address)
		}); err != nil {
			return nodeOutcome{}, fmt.Errorf("send location: %w", err)
		}
		logPreview = name
		if logPreview == "" {
			logPreview = fmt.Sprintf("%g,%g", lat, lng)
		}

	default:
		return nodeOutcome{}, fmt.Errorf("message node %q has unsupported media_type %q", node.ID, mediaType)
	}

	ctx.sentOutbound = true
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, logPreview, node.ID)
	if wait {
		return nodeOutcome{yield: true}, nil
	}
	return nodeOutcome{outcome: "default"}, nil
}

// execChatButtons sends interactive buttons on first entry (yielding to
// wait for a click); on a later inbound that carries a buttonID, consumes
// the selection and returns "button:<id>" so the runner can resolve the
// next edge and advance.
// Config: { "body": "...", "buttons": [{ "id": "...", "title": "..." }, ...] }
func (a *App) execChatButtons(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if !ctx.consumed && ctx.buttonID != "" {
		ctx.consumed = true
		if storeAs := stringFromConfig(node.Config, "store_as"); storeAs != "" {
			if ctx.session.SessionData == nil {
				ctx.session.SessionData = models.JSONB{}
			}
			value := ctx.userInput
			if value == "" {
				value = ctx.buttonID
			}
			ctx.session.SessionData[storeAs] = value
		}
		return nodeOutcome{outcome: "button:" + ctx.buttonID}, nil
	}

	// Free text or non-text inbound while parked → unmatched (entry fallback if no edge).
	if !ctx.consumed && chatInboundPresent(ctx) {
		ctx.consumed = true
		return nodeOutcome{outcome: "unmatched"}, nil
	}

	body := stringFromConfig(node.Config, "body", "message", "text")
	if body == "" {
		body = node.Label
	}
	body = processTemplate(body, ctx.session.SessionData)
	buttons := buttonsFromConfig(node.Config)
	if len(buttons) == 0 {
		return nodeOutcome{}, fmt.Errorf("buttons node %q has no buttons configured", node.ID)
	}
	for _, b := range buttons {
		for _, key := range []string{"title", "url", "phone_number"} {
			if s, ok := b[key].(string); ok && s != "" {
				b[key] = processTemplate(s, ctx.session.SessionData)
			}
		}
	}
	if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
		return a.sendAndSaveInteractiveButtons(ctx.account, c, body, buttons)
	}); err != nil {
		return nodeOutcome{}, fmt.Errorf("send buttons: %w", err)
	}
	ctx.sentOutbound = true
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, body, node.ID)
	return nodeOutcome{yield: true}, nil
}

// execChatList sends an interactive list message and waits for a row tap.
// Config:
//
//	{
//	  "body": "...", "header": "...", "footer": "...", "button": "Menu",
//	  "store_as": "choice",
//	  "sections": [{ "title": "...", "rows": [{ "id": "...", "title": "...", "description": "..." }] }]
//	}
func (a *App) execChatList(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if !ctx.consumed && ctx.buttonID != "" {
		ctx.consumed = true
		if storeAs := stringFromConfig(node.Config, "store_as"); storeAs != "" {
			if ctx.session.SessionData == nil {
				ctx.session.SessionData = models.JSONB{}
			}
			value := ctx.userInput
			if value == "" {
				value = ctx.buttonID
			}
			ctx.session.SessionData[storeAs] = value
		}
		return nodeOutcome{outcome: "button:" + ctx.buttonID}, nil
	}

	if !ctx.consumed && chatInboundPresent(ctx) {
		ctx.consumed = true
		return nodeOutcome{outcome: "unmatched"}, nil
	}

	body := stringFromConfig(node.Config, "body", "message", "text")
	if body == "" {
		body = node.Label
	}
	body = processTemplate(body, ctx.session.SessionData)
	header := processTemplate(stringFromConfig(node.Config, "header"), ctx.session.SessionData)
	footer := processTemplate(stringFromConfig(node.Config, "footer"), ctx.session.SessionData)
	button := processTemplate(stringFromConfig(node.Config, "button", "button_text"), ctx.session.SessionData)
	sections := listSectionsFromConfig(node.Config)
	if len(sections) == 0 {
		return nodeOutcome{}, fmt.Errorf("list node %q has no sections/rows configured", node.ID)
	}
	for i := range sections {
		sections[i].Title = processTemplate(sections[i].Title, ctx.session.SessionData)
		for j := range sections[i].Rows {
			sections[i].Rows[j].Title = processTemplate(sections[i].Rows[j].Title, ctx.session.SessionData)
			sections[i].Rows[j].Description = processTemplate(sections[i].Rows[j].Description, ctx.session.SessionData)
		}
	}
	if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
		return a.sendAndSaveInteractiveList(ctx.account, c, body, button, header, footer, sections)
	}); err != nil {
		return nodeOutcome{}, fmt.Errorf("send list: %w", err)
	}
	ctx.sentOutbound = true
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, body, node.ID)
	return nodeOutcome{yield: true}, nil
}

func (a *App) execChatPrompt(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	body := stringFromConfig(node.Config, "body", "message", "text")

	// No input yet → send prompt and wait.
	if !ctx.consumed && ctx.userInput == "" {
		if body == "" {
			return nodeOutcome{}, fmt.Errorf("prompt node %q has no body configured", node.ID)
		}
		rendered := processTemplate(body, ctx.session.SessionData)
		if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
			return a.sendAndSaveTextMessage(ctx.account, c, rendered)
		}); err != nil {
			return nodeOutcome{}, fmt.Errorf("send prompt: %w", err)
		}
		a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, rendered, node.ID)
		ctx.sentOutbound = true
		return nodeOutcome{yield: true}, nil
	}

	if ctx.consumed {
		// Input was already consumed by an earlier blocking node in this
		// run — defensive guard. Treat as fresh entry.
		return nodeOutcome{yield: true}, nil
	}

	ctx.consumed = true
	input := ctx.userInput

	validationRegex := stringFromConfig(node.Config, "validation_regex")
	if validationRegex != "" {
		re, err := regexp.Compile(validationRegex)
		if err != nil {
			a.Log.Error("prompt node has invalid regex",
				"node", node.ID, "regex", validationRegex, "error", err)
			// Skip validation rather than failing the user-facing flow.
		} else if !re.MatchString(input) {
			return a.handleChatPromptInvalid(node, ctx)
		}
	}

	// Valid → persist + advance.
	if storeAs := stringFromConfig(node.Config, "store_as"); storeAs != "" {
		if ctx.session.SessionData == nil {
			ctx.session.SessionData = models.JSONB{}
		}
		ctx.session.SessionData[storeAs] = input
	}
	ctx.session.StepRetries = 0
	return nodeOutcome{outcome: "default"}, nil
}

func (a *App) handleChatPromptInvalid(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	ctx.session.StepRetries++

	maxRetries := intFromConfig(node.Config, "max_retries", 3)
	if ctx.session.StepRetries >= maxRetries {
		ctx.session.StepRetries = 0
		return nodeOutcome{outcome: "max_retries"}, nil
	}

	errorMsg := stringFromConfig(node.Config, "validation_error")
	if errorMsg == "" {
		errorMsg = "Invalid input. Please try again."
	}
	errorMsg = processTemplate(errorMsg, ctx.session.SessionData)
	if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
		return a.sendAndSaveTextMessage(ctx.account, c, errorMsg)
	}); err != nil {
		return nodeOutcome{}, fmt.Errorf("send validation error: %w", err)
	}
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, errorMsg, node.ID)
	ctx.sentOutbound = true
	return nodeOutcome{yield: true}, nil
}

// execChatAPICall fires an HTTP request defined in node.Config and routes
// via "http:2xx" / "http:non2xx" outcomes. Mirrors fetchApiResponse's
// approach to template interpolation (seeds {{phone_number}}) and
// response_mapping (extracted keys are merged into SessionData so later
// nodes can reference them through processTemplate).
//
// Non-blocking — the runner immediately advances via resolveEdge after
// this returns. Network errors are mapped to "http:non2xx" so the graph
// can route to a fallback path; logged for visibility.
//
// Config:
//
//	{
//	  "url":     "https://api.example.com/lookup?phone={{phone_number}}",
//	  "method":  "POST",
//	  "headers": { "Authorization": "Bearer {{token}}" },
//	  "body":    "{\"phone\":\"{{phone_number}}\"}",
//	  "response_mapping": { "customer_id": "data.id", "status": "data.status" },
//	  // Optional. If set, a 2xx response renders this template against
//	  // SessionData (post-response_mapping) and sends it to the user.
//	  // Lets the same node act as v1's "fetch + send templated message"
//	  // pattern without forcing authors to chain a separate message node.
//	  "message_template": "Hello {{customer_id}}!"
//	}
func (a *App) execChatAPICall(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	cfgJSONB := models.JSONB(node.Config)

	if ctx.session.SessionData == nil {
		ctx.session.SessionData = models.JSONB{}
	}
	sessionData := ctx.session.SessionData
	sessionData["phone_number"] = ctx.session.PhoneNumber

	replaceVar := func(s string) string { return processTemplate(s, sessionData) }
	respBody, statusCode, err := a.executeConfiguredAPI(cfgJSONB, replaceVar)
	if err != nil {
		a.Log.Error("api_call node request failed",
			"node", node.ID, "session", ctx.session.ID, "error", err)
		return nodeOutcome{outcome: "http:non2xx"}, nil
	}

	if statusCode < 200 || statusCode >= 300 {
		return nodeOutcome{outcome: "http:non2xx"}, nil
	}

	// 2xx: optionally extract response_mapping → SessionData.
	if mapping, ok := node.Config["response_mapping"].(map[string]any); ok && len(mapping) > 0 {
		var jsonResp map[string]any
		if err := json.Unmarshal(respBody, &jsonResp); err == nil {
			mappingStrings := make(map[string]string, len(mapping))
			for varName, path := range mapping {
				if pathStr, ok := path.(string); ok {
					mappingStrings[varName] = pathStr
				}
			}
			extracted := extractResponseMapping(jsonResp, mappingStrings)
			maps.Copy(sessionData, extracted)
		}
	}

	// Optionally render and send a message after the fetch. Mirrors v1
	// api_fetch's bundled "fetch + send" behavior so the converter can
	// keep collapsing api_fetch steps onto a single api_call node.
	if tmpl := stringFromConfig(node.Config, "message_template"); tmpl != "" {
		rendered := processTemplate(tmpl, sessionData)
		if rendered != "" {
			if err := a.sendAndSaveTextMessage(ctx.account, ctx.contact, rendered); err != nil {
				a.Log.Error("api_call node failed to send message_template",
					"node", node.ID, "session", ctx.session.ID, "error", err)
				// Still advance via http:2xx — the data fetch succeeded.
			} else {
				a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, rendered, node.ID)
			}
		}
	}

	return nodeOutcome{outcome: "http:2xx"}, nil
}

// execChatCondition branches the graph. Two modes:
//
//  1. Expression (legacy / power users):
//     { "mode": "expression", "expression": "tier == \"premium\"" }
//     Outcomes: "true" / "false".
//
//  2. Rules (whatsCRM-style named branches):
//     {
//     "mode": "rules",
//     "variable": "senderMessage",   // optional; empty = senderMessage
//     "rules": [
//     { "name": "Yes", "operator": "text_exact", "value": "yes",
//     "case_sensitive": false, "handle": "yes" },
//     { "name": "Range", "operator": "number_between",
//     "value": "1", "value_to": "10", "handle": "mid" }
//     ]
//     }
//     Outcomes: first matching rule's handle, else "else".
//
// Compile/runtime errors in expression mode map to "false". Empty rules
// with no match → "else".
func (a *App) execChatCondition(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	mode := stringFromConfig(node.Config, "mode")
	rules := conditionRulesFromConfig(node.Config)
	if mode == "rules" || (mode == "" && len(rules) > 0) {
		left := conditionLeftValue(node.Config, ctx)
		for _, rule := range rules {
			if matchConditionRule(rule, left) {
				handle := rule.Handle
				if handle == "" {
					handle = slugHandle(rule.Name)
				}
				if handle == "" {
					handle = "matched"
				}
				return nodeOutcome{outcome: handle}, nil
			}
		}
		return nodeOutcome{outcome: "else"}, nil
	}

	expression := stringFromConfig(node.Config, "expression")
	if expression == "" {
		a.Log.Warn("condition node missing expression",
			"node", node.ID, "session", ctx.session.ID)
		return nodeOutcome{outcome: "false"}, nil
	}

	matched, err := evaluateConditionExpression(expression, ctx.session.SessionData)
	if err != nil {
		a.Log.Warn("condition node expression failed",
			"node", node.ID, "session", ctx.session.ID, "expression", expression, "error", err)
		return nodeOutcome{outcome: "false"}, nil
	}
	if matched {
		return nodeOutcome{outcome: "true"}, nil
	}
	return nodeOutcome{outcome: "false"}, nil
}

type conditionRule struct {
	Name          string
	Operator      string
	Value         string
	ValueTo       string
	CaseSensitive bool
	Handle        string
}

func conditionRulesFromConfig(cfg map[string]any) []conditionRule {
	raw, ok := cfg["rules"].([]any)
	if !ok {
		return nil
	}
	out := make([]conditionRule, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, conditionRule{
			Name:          stringFromConfig(m, "name"),
			Operator:      stringFromConfig(m, "operator", "type"),
			Value:         stringFromConfig(m, "value"),
			ValueTo:       stringFromConfig(m, "value_to", "to"),
			CaseSensitive: boolFromConfig(m, "case_sensitive") || boolFromConfig(m, "caseSensitive"),
			Handle:        stringFromConfig(m, "handle"),
		})
	}
	return out
}

func conditionLeftValue(cfg map[string]any, ctx *chatNodeCtx) string {
	varName := stringFromConfig(cfg, "variable", "input_variable")
	if varName == "" {
		varName = "senderMessage"
	}
	if ctx.session.SessionData != nil {
		if v, ok := ctx.session.SessionData[varName]; ok && v != nil {
			return fmt.Sprint(v)
		}
	}
	// Fall back to this turn's inbound text when the named var isn't set.
	if varName == "senderMessage" {
		return ctx.userInput
	}
	return ""
}

func matchConditionRule(rule conditionRule, left string) bool {
	op := strings.ToLower(strings.TrimSpace(rule.Operator))
	right := rule.Value
	leftCmp, rightCmp := left, right
	if !rule.CaseSensitive {
		leftCmp = strings.ToLower(left)
		rightCmp = strings.ToLower(right)
	}

	switch op {
	case "text_exact", "exact", "eq", "equals":
		return leftCmp == rightCmp
	case "text_contains", "contains":
		return strings.Contains(leftCmp, rightCmp)
	case "text_starts_with", "starts_with", "startswith":
		return strings.HasPrefix(leftCmp, rightCmp)
	case "text_ends_with", "ends_with", "endswith":
		return strings.HasSuffix(leftCmp, rightCmp)
	case "number_equals", "number_eq":
		ln, lok := parseConditionNumber(left)
		rn, rok := parseConditionNumber(right)
		return lok && rok && ln == rn
	case "number_greater", "number_gt", "gt":
		ln, lok := parseConditionNumber(left)
		rn, rok := parseConditionNumber(right)
		return lok && rok && ln > rn
	case "number_less", "number_lt", "lt":
		ln, lok := parseConditionNumber(left)
		rn, rok := parseConditionNumber(right)
		return lok && rok && ln < rn
	case "number_between", "between":
		ln, lok := parseConditionNumber(left)
		from, fok := parseConditionNumber(right)
		to, tok := parseConditionNumber(rule.ValueTo)
		return lok && fok && tok && ln >= from && ln <= to
	default:
		// Unknown operator: treat as text_exact so misconfigs don't silently match.
		return leftCmp == rightCmp
	}
}

func parseConditionNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var f float64
	if _, err := fmt.Sscan(s, &f); err != nil {
		return 0, false
	}
	return f, true
}

func slugHandle(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// evaluateConditionExpression compiles + runs a boolean expression via
// expr-lang/expr against SessionData. The result is coerced to bool —
// non-bool truthy values count as true (matches expr's natural casting).
func evaluateConditionExpression(expression string, data models.JSONB) (bool, error) {
	env := make(map[string]any, len(data))
	maps.Copy(env, data)

	program, err := expr.Compile(expression, expr.Env(env), expr.AllowUndefinedVariables())
	if err != nil {
		return false, fmt.Errorf("compile: %w", err)
	}
	out, err := expr.Run(program, env)
	if err != nil {
		return false, fmt.Errorf("run: %w", err)
	}
	switch v := out.(type) {
	case bool:
		return v, nil
	case nil:
		return false, nil
	case string:
		return v != "" && strings.ToLower(v) != "false", nil
	case float64:
		return v != 0, nil
	case int:
		return v != 0, nil
	}
	// Anything else (slices, maps) counts as truthy if non-nil.
	return out != nil, nil
}

// execChatTiming routes "in_hours" / "out_of_hours" based on a per-day
// schedule. Mirrors IVR's executeTiming (internal/calling/ivr.go:539).
// Non-blocking; no message sent.
//
// Config:
//
//	{
//	  "schedule": [
//	    { "day": "monday", "enabled": true,  "start_time": "09:00", "end_time": "18:00" },
//	    { "day": "sunday", "enabled": false }
//	  ]
//	}
//
// Days not listed in the schedule are treated as out_of_hours, matching
// IVR's behavior.
func (a *App) execChatTiming(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	rawSchedule, _ := node.Config["schedule"].([]any)
	outcome := evaluateTimingSchedule(time.Now(), rawSchedule, a.Log)
	_ = ctx // ctx unused — included for symmetry with other executors
	return nodeOutcome{outcome: outcome}, nil
}

// evaluateTimingSchedule is the pure decision function, factored out for
// unit-testing with a fixed clock. Returns "in_hours" or "out_of_hours".
func evaluateTimingSchedule(now time.Time, schedule []any, log scheduleLogger) string {
	dayName := strings.ToLower(now.Weekday().String())
	for _, item := range schedule {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		day, _ := entry["day"].(string)
		if strings.ToLower(day) != dayName {
			continue
		}
		enabled, _ := entry["enabled"].(bool)
		if !enabled {
			return "out_of_hours"
		}
		startStr, _ := entry["start_time"].(string)
		endStr, _ := entry["end_time"].(string)
		startTime, err1 := time.Parse("15:04", startStr)
		endTime, err2 := time.Parse("15:04", endStr)
		if err1 != nil || err2 != nil {
			if log != nil {
				log.Warn("timing node has invalid time format",
					"start", startStr, "end", endStr)
			}
			return "out_of_hours"
		}
		nowMinutes := now.Hour()*60 + now.Minute()
		startMinutes := startTime.Hour()*60 + startTime.Minute()
		endMinutes := endTime.Hour()*60 + endTime.Minute()
		if nowMinutes >= startMinutes && nowMinutes < endMinutes {
			return "in_hours"
		}
		return "out_of_hours"
	}
	// Day not configured — treat as out of hours.
	return "out_of_hours"
}

// scheduleLogger is the subset of the app logger evaluateTimingSchedule
// needs. Defined locally to avoid pulling logf into the test imports.
type scheduleLogger interface {
	Warn(msg string, args ...any)
}

// execChatSetVariable assigns values into SessionData. Supports:
//
//	{
//	  "set": { "greeting": "Hello {{contact_name}}!" },
//	  "paths": [
//	    { "name": "body", "path": "msgContext.text.body" },
//	    { "name": "raw",  "path": "senderMessage" }
//	  ]
//	}
//
// "set" values are templated after "paths" so templates can reference
// extracted vars. "paths" walk SessionData["__inbound__"] (seeded each
// turn) with dotted keys. Non-blocking; outcome "default".
func (a *App) execChatSetVariable(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if ctx.session.SessionData == nil {
		ctx.session.SessionData = models.JSONB{}
	}

	// Apply "paths" before "set" so templates can reference extracted vars.
	if paths, ok := node.Config["paths"].([]any); ok {
		inbound, _ := ctx.session.SessionData["__inbound__"].(map[string]any)
		inboundMsg := inboundMessageObject(inbound)
		for _, item := range paths {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name := stringFromConfig(m, "name", "var", "varName")
			path := stringFromConfig(m, "path", "responsePath", "response_path")
			if name == "" || path == "" {
				continue
			}
			if v, ok := lookupDottedPath(inboundMsg, path); ok {
				ctx.session.SessionData[name] = v
			} else if v, ok := lookupDottedPath(inbound, path); ok {
				ctx.session.SessionData[name] = v
			} else if v, ok := lookupDottedPath(map[string]any(ctx.session.SessionData), path); ok {
				// Allow paths against session itself (e.g. senderName).
				ctx.session.SessionData[name] = v
			}
		}
	}

	assignments, _ := node.Config["set"].(map[string]any)
	for name, raw := range assignments {
		if name == "" {
			continue
		}
		tmpl, ok := raw.(string)
		if !ok {
			ctx.session.SessionData[name] = raw
			continue
		}
		ctx.session.SessionData[name] = processTemplate(tmpl, ctx.session.SessionData)
	}

	return nodeOutcome{outcome: "default"}, nil
}

// inboundMessageObject returns the message inside a Meta webhook envelope
// (entry[0].changes[0].value.messages[0]) so paths like "msgContext.text.body"
// resolve against the message. Returns inbound unchanged when it isn't an
// envelope.
func inboundMessageObject(inbound map[string]any) map[string]any {
	first := func(v any) map[string]any {
		if list, ok := v.([]any); ok && len(list) > 0 {
			m, _ := list[0].(map[string]any)
			return m
		}
		return nil
	}
	entry := first(inbound["entry"])
	if entry == nil {
		return inbound
	}
	change := first(entry["changes"])
	if change == nil {
		return inbound
	}
	value, _ := change["value"].(map[string]any)
	if msg := first(value["messages"]); msg != nil {
		return msg
	}
	return inbound
}

// lookupDottedPath walks a nested map using a.b.c notation. Also accepts
// whatsCRM-style prefixes like "message.msgContext.text.body" and
// "msgContext.text.body" (mapped onto Meta-shaped inbound.text.body).
func lookupDottedPath(root map[string]any, path string) (any, bool) {
	if root == nil || path == "" {
		return nil, false
	}
	path = strings.TrimPrefix(path, "message.")
	path = strings.TrimPrefix(path, "msgContext.")
	parts := strings.Split(path, ".")
	var cur any = root
	for _, part := range parts {
		if part == "" {
			continue
		}
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			cur = next
		case models.JSONB:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			cur = next
		default:
			return nil, false
		}
	}
	return cur, true
}

// execChatAIResponse invokes the configured LLM provider via the
// existing generateAIResponse helper, sends the answer back to the user,
// and falls through. Reuses the org's chatbot settings (provider,
// model, api key, system prompt) so authors don't have to duplicate
// credentials per node.
//
// Input to the LLM is, in priority order:
//  1. config.prompt_template — runs through processTemplate, useful when
//     the AI should respond to a structured request rather than the raw
//     user text (e.g. "Summarise the customer's situation: {{summary}}").
//  2. ctx.userInput — the user's latest message.
//
// Outcome is always "default". AI failures, empty replies, or AI being
// disabled all advance via the default edge and log a warning — the
// graph author can route to a fallback message there.
func (a *App) execChatAIResponse(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	settings, err := a.getChatbotSettingsCached(ctx.account.OrganizationID, ctx.account.Name)
	if err != nil {
		a.Log.Error("ai_response node failed to load chatbot settings",
			"node", node.ID, "session", ctx.session.ID, "error", err)
		return nodeOutcome{outcome: "default"}, nil
	}
	if !settings.AI.Enabled || settings.AI.Provider == "" || settings.AI.APIKey == "" {
		a.Log.Warn("ai_response node hit but AI not configured",
			"node", node.ID, "session", ctx.session.ID,
			"ai_enabled", settings.AI.Enabled, "has_provider", settings.AI.Provider != "")
		return nodeOutcome{outcome: "default"}, nil
	}

	userMessage := ctx.userInput
	if tmpl := stringFromConfig(node.Config, "prompt_template", "prompt"); tmpl != "" {
		if ctx.session.SessionData == nil {
			ctx.session.SessionData = models.JSONB{}
		}
		userMessage = processTemplate(tmpl, ctx.session.SessionData)
	}

	answer, err := a.generateAIResponse(settings, ctx.session, userMessage)
	if err != nil {
		a.Log.Error("ai_response node generateAIResponse failed",
			"node", node.ID, "session", ctx.session.ID, "error", err)
		return nodeOutcome{outcome: "default"}, nil
	}
	if answer == "" {
		a.Log.Warn("ai_response node got empty answer from provider",
			"node", node.ID, "session", ctx.session.ID)
		return nodeOutcome{outcome: "default"}, nil
	}

	if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
		return a.sendAndSaveTextMessage(ctx.account, c, answer)
	}); err != nil {
		return nodeOutcome{}, fmt.Errorf("send ai response: %w", err)
	}
	ctx.sentOutbound = true
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, answer, node.ID)
	if storeAs := stringFromConfig(node.Config, "store_as"); storeAs != "" {
		if ctx.session.SessionData == nil {
			ctx.session.SessionData = models.JSONB{}
		}
		ctx.session.SessionData[storeAs] = answer
	}
	return nodeOutcome{outcome: "default"}, nil
}

// execChatTransfer hands the session off to a human team or the general
// queue. Optionally sends a body message first (e.g. "Connecting you to
// an agent…"), then creates the transfer row via the same helpers the
// legacy flow uses (createTransferToTeam / createTransferToQueue, source
// = TransferSourceFlow), and marks the session completed.
//
// Terminal: returns yield=true so the runner stops and persists. No
// outgoing edges are expected on this node.
//
// Config:
//
//	{
//	  "body":    "Connecting you to a human…",  // optional
//	  "team_id": "<uuid>",                       // empty or "_general" = queue
//	  "notes":   "Last seen {{last_query}}"      // optional; templated
//	}
func (a *App) execChatTransfer(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if body := stringFromConfig(node.Config, "body", "message", "text"); body != "" {
		message := processTemplate(body, ctx.session.SessionData)
		if err := a.sendAndSaveTextMessage(ctx.account, ctx.contact, message); err != nil {
			a.Log.Error("transfer node failed to send body",
				"node", node.ID, "session", ctx.session.ID, "error", err)
		} else {
			a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, message, node.ID)
		}
	}

	notes := ""
	if rawNotes := stringFromConfig(node.Config, "notes"); rawNotes != "" {
		notes = processTemplate(rawNotes, ctx.session.SessionData)
	}

	teamIDStr := stringFromConfig(node.Config, "team_id")
	agentIDStr := stringFromConfig(node.Config, "user_id", "agent_id")
	if agentIDStr != "" {
		if parsed, err := uuid.Parse(agentIDStr); err == nil {
			a.createTransferToAgent(ctx.account, ctx.contact, parsed, notes, models.TransferSourceFlow)
		} else {
			a.Log.Warn("transfer node has invalid user_id, falling back to queue",
				"node", node.ID, "user_id", agentIDStr, "error", err)
			a.createTransferToQueue(ctx.account, ctx.contact, models.TransferSourceFlow)
		}
	} else if teamIDStr != "" && teamIDStr != "_general" {
		if parsed, err := uuid.Parse(teamIDStr); err == nil {
			a.createTransferToTeam(ctx.account, ctx.contact, parsed, notes, models.TransferSourceFlow)
		} else {
			a.Log.Warn("transfer node has invalid team_id, falling back to queue",
				"node", node.ID, "team_id", teamIDStr, "error", err)
			a.createTransferToQueue(ctx.account, ctx.contact, models.TransferSourceFlow)
		}
	} else {
		a.createTransferToQueue(ctx.account, ctx.contact, models.TransferSourceFlow)
	}

	ctx.session.Status = models.SessionStatusCompleted
	return nodeOutcome{yield: true}, nil
}

// execChatWebhook fires a best-effort HTTP request. Unlike api_call, the
// response is discarded — success, non-2xx, and network errors all
// advance via the "default" edge. Use api_call when the flow needs to
// branch on the response or capture data.
//
// Non-blocking; the call is synchronous to keep test semantics simple
// but the flow does not depend on the outcome.
//
// Config (same shape as api_call minus response_mapping):
//
//	{
//	  "url":     "https://example.com/hook?phone={{phone_number}}",
//	  "method":  "POST",
//	  "headers": { "Authorization": "Bearer …" },
//	  "body":    "{\"event\":\"flow_completed\"}"
//	}
func (a *App) execChatWebhook(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if ctx.session.SessionData == nil {
		ctx.session.SessionData = models.JSONB{}
	}
	sessionData := ctx.session.SessionData
	sessionData["phone_number"] = ctx.session.PhoneNumber

	replaceVar := func(s string) string { return processTemplate(s, sessionData) }
	_, statusCode, err := a.executeConfiguredAPI(models.JSONB(node.Config), replaceVar)
	switch {
	case err != nil:
		a.Log.Warn("webhook node request errored (continuing)",
			"node", node.ID, "session", ctx.session.ID, "error", err)
	case statusCode < 200 || statusCode >= 300:
		a.Log.Warn("webhook node returned non-2xx (continuing)",
			"node", node.ID, "session", ctx.session.ID, "status", statusCode)
	}
	return nodeOutcome{outcome: "default"}, nil
}

// execChatGotoFlow jumps execution to another flow within the same
// organization + WhatsApp account. SessionData (variables, path) is
// carried forward — this is the whole point of sub-flows.
//
// Mirrors IVR's executeGotoFlow (internal/calling/ivr.go:495). Not a
// "call" — there's no return stack — so once the target flow ends, the
// session ends. The runner detects the CurrentFlowID change and reloads
// the graph + entry node within the same webhook run.
//
// Config:
//
//	{ "flow_id": "<uuid>" }
//
// Misconfiguration (missing/invalid id, target disabled, cross-account)
// is logged and terminates the source flow gracefully rather than
// erroring the inbound webhook.
func (a *App) execChatGotoFlow(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	targetIDStr := stringFromConfig(node.Config, "flow_id")
	if targetIDStr == "" {
		a.Log.Warn("goto_flow node missing flow_id",
			"node", node.ID, "session", ctx.session.ID)
		return nodeOutcome{}, nil
	}
	targetID, err := uuid.Parse(targetIDStr)
	if err != nil {
		a.Log.Warn("goto_flow node has invalid flow_id",
			"node", node.ID, "flow_id", targetIDStr, "error", err)
		return nodeOutcome{}, nil
	}

	target, err := a.getChatbotFlowByIDCached(ctx.account.OrganizationID, targetID)
	if err != nil || target == nil {
		a.Log.Warn("goto_flow target not found",
			"node", node.ID, "flow_id", targetID, "error", err)
		return nodeOutcome{}, nil
	}
	if !target.IsEnabled {
		a.Log.Warn("goto_flow target is disabled",
			"node", node.ID, "flow_id", targetID)
		return nodeOutcome{}, nil
	}
	if target.WhatsAppAccount != ctx.session.WhatsAppAccount {
		a.Log.Warn("goto_flow target belongs to a different WA account; refusing",
			"node", node.ID, "target_account", target.WhatsAppAccount,
			"session_account", ctx.session.WhatsAppAccount)
		return nodeOutcome{}, nil
	}
	if target.Graph == nil {
		a.Log.Warn("goto_flow target has no v2 graph",
			"node", node.ID, "flow_id", targetID)
		return nodeOutcome{}, nil
	}

	// Record the jump on the path for audit visibility.
	if ctx.session.SessionData == nil {
		ctx.session.SessionData = models.JSONB{}
	}
	path, _ := ctx.session.SessionData["__path__"].([]any)
	path = append(path, map[string]any{
		"action":  "goto_flow",
		"flow":    target.Name,
		"flow_id": target.ID.String(),
	})
	ctx.session.SessionData["__path__"] = path

	// Signal the switch — the runner detects CurrentFlowID change and
	// reloads + resets CurrentStep to the new entry node.
	ctx.session.CurrentFlowID = &target.ID

	return nodeOutcome{outcome: "goto"}, nil
}

// execChatWhatsAppFlow sends an interactive WhatsApp Flow form on first
// entry (yields to wait for the user's submission). On a later inbound
// that carries a parsed flow_response_data, merges those fields into
// SessionData and advances via the "default" edge.
//
// Config:
//
//	{
//	  "flow_id": "<meta_flow_id>",   // required
//	  "header":  "Header text",       // optional, templated
//	  "body":    "Body text",         // optional, templated
//	  "cta":     "Open form"           // optional CTA label
//	}
//
// Submitted fields land in SessionData keyed by their form field names.
// A misconfigured node logs + advances via default so the conversation
// doesn't dead-end.
func (a *App) execChatWhatsAppFlow(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if !ctx.consumed && len(ctx.flowResponseData) > 0 {
		ctx.consumed = true
		if ctx.session.SessionData == nil {
			ctx.session.SessionData = models.JSONB{}
		}
		maps.Copy(ctx.session.SessionData, ctx.flowResponseData)
		return nodeOutcome{outcome: "default"}, nil
	}

	flowID := stringFromConfig(node.Config, "flow_id")
	if flowID == "" {
		a.Log.Warn("whatsapp_flow node missing flow_id",
			"node", node.ID, "session", ctx.session.ID)
		return nodeOutcome{outcome: "default"}, nil
	}

	body := processTemplate(stringFromConfig(node.Config, "body", "message", "text"), ctx.session.SessionData)
	header := processTemplate(stringFromConfig(node.Config, "header"), ctx.session.SessionData)
	footer := processTemplate(stringFromConfig(node.Config, "footer"), ctx.session.SessionData)
	cta := processTemplate(stringFromConfig(node.Config, "cta"), ctx.session.SessionData)

	// Prefer local WhatsAppFlow row: resolve by meta id OR local UUID, require PUBLISHED when found.
	firstScreen := ""
	var waFlow models.WhatsAppFlow
	found := false
	if err := a.DB.Where("meta_flow_id = ?", flowID).First(&waFlow).Error; err == nil {
		found = true
	} else if id, err := uuid.Parse(flowID); err == nil {
		if err := a.DB.Where("id = ? AND organization_id = ?", id, ctx.account.OrganizationID).First(&waFlow).Error; err == nil {
			found = true
			if waFlow.MetaFlowID != "" {
				flowID = waFlow.MetaFlowID
			}
		}
	}
	if found {
		if strings.ToUpper(waFlow.Status) != "PUBLISHED" {
			a.Log.Warn("whatsapp_flow node references non-published flow",
				"node", node.ID, "flow_id", flowID, "status", waFlow.Status)
			return nodeOutcome{outcome: "default"}, nil
		}
		if len(waFlow.Screens) > 0 {
			if screenMap, ok := waFlow.Screens[0].(map[string]any); ok {
				if id, ok := screenMap["id"].(string); ok {
					firstScreen = id
				}
			}
		}
		if firstScreen == "" && waFlow.FlowJSON != nil {
			if screens, ok := waFlow.FlowJSON["screens"].([]any); ok && len(screens) > 0 {
				if screenMap, ok := screens[0].(map[string]any); ok {
					if id, ok := screenMap["id"].(string); ok {
						firstScreen = id
					}
				}
			}
		}
	}

	flowToken := fmt.Sprintf("chatbot_%s_%s_%d", ctx.session.ID.String(), node.ID, time.Now().UnixNano())
	if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
		return a.sendAndSaveFlowMessage(ctx.account, c, flowID, header, body, footer, cta, flowToken, firstScreen)
	}); err != nil {
		return nodeOutcome{}, fmt.Errorf("send whatsapp_flow: %w", err)
	}
	ctx.sentOutbound = true
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, body, node.ID)
	return nodeOutcome{yield: true}, nil
}

// execChatWATemplate sends an approved WhatsApp template and continues.
// Config:
//
//	{
//	  "template_id": "<uuid>",           // preferred
//	  "template_name": "order_update", // fallback lookup with language
//	  "language": "en",
//	  "body_params": { "1": "{{order_id}}", "name": "{{contact_name}}" },
//	  "wait_for_reply": false
//	}
func (a *App) execChatWATemplate(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	wait := boolFromConfig(node.Config, "wait_for_reply")
	if wait && !ctx.consumed && (ctx.userInput != "" || ctx.buttonID != "") {
		ctx.consumed = true
		return nodeOutcome{outcome: "default"}, nil
	}

	var tmpl models.Template
	found := false
	if idStr := stringFromConfig(node.Config, "template_id"); idStr != "" {
		if id, err := uuid.Parse(idStr); err == nil {
			if err := a.DB.Where("id = ? AND organization_id = ?", id, ctx.account.OrganizationID).First(&tmpl).Error; err == nil {
				found = true
			}
		}
	}
	if !found {
		name := stringFromConfig(node.Config, "template_name", "name")
		lang := stringFromConfig(node.Config, "language")
		q := a.DB.Where("organization_id = ? AND whatsapp_account = ? AND name = ?",
			ctx.account.OrganizationID, ctx.account.Name, name)
		if lang != "" {
			q = q.Where("language = ?", lang)
		}
		if err := q.First(&tmpl).Error; err == nil {
			found = true
		}
	}
	if !found {
		a.Log.Warn("wa_template node template not found",
			"node", node.ID, "session", ctx.session.ID)
		return nodeOutcome{outcome: "default"}, nil
	}
	if strings.ToUpper(tmpl.Status) != "APPROVED" {
		a.Log.Warn("wa_template node template not approved",
			"node", node.ID, "template", tmpl.Name, "status", tmpl.Status)
		return nodeOutcome{outcome: "default"}, nil
	}

	bodyParams := map[string]string{}
	if raw, ok := node.Config["body_params"].(map[string]any); ok {
		for k, v := range raw {
			s := fmt.Sprint(v)
			bodyParams[k] = processTemplate(s, ctx.session.SessionData)
		}
	} else if raw, ok := node.Config["variableMap"].(map[string]any); ok {
		for k, v := range raw {
			s := fmt.Sprint(v)
			bodyParams[k] = processTemplate(s, ctx.session.SessionData)
		}
	}

	sendCtx := context.Background()
	if err := a.forEachChatSendContact(ctx, node, func(c *models.Contact) error {
		_, err := a.SendOutgoingMessage(sendCtx, OutgoingMessageRequest{
			Account:    ctx.account,
			Contact:    c,
			Type:       models.MessageTypeTemplate,
			Template:   &tmpl,
			BodyParams: bodyParams,
		}, ChatbotSendOptions())
		return err
	}); err != nil {
		a.Log.Error("wa_template send failed",
			"node", node.ID, "template", tmpl.Name, "error", err)
		return nodeOutcome{outcome: "default"}, nil
	}
	ctx.sentOutbound = true
	a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, tmpl.DisplayName, node.ID)
	if wait {
		return nodeOutcome{yield: true}, nil
	}
	return nodeOutcome{outcome: "default"}, nil
}

// execChatDelay parks the session for N seconds without holding the
// inbound webhook open. A scheduler-backed poller resumes the graph.
// Config: { "seconds": 30 } — clamped to 1..3600; 0 advances immediately.
func (a *App) execChatDelay(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if ctx.session.SessionData == nil {
		ctx.session.SessionData = models.JSONB{}
	}

	pending, _ := ctx.session.SessionData["__delay_pending"].(string)
	if pending == node.ID {
		resumeAt, _ := ctx.session.SessionData["__delay_resume_at"].(float64)
		if resumeAt == 0 {
			if v, ok := ctx.session.SessionData["__delay_resume_at"].(int64); ok {
				resumeAt = float64(v)
			}
		}
		if resumeAt > 0 && time.Now().Unix() < int64(resumeAt) {
			// Still waiting — ignore early inbound and stay parked.
			return nodeOutcome{yield: true}, nil
		}
		delete(ctx.session.SessionData, "__delay_pending")
		delete(ctx.session.SessionData, "__delay_resume_at")
		return nodeOutcome{outcome: "default"}, nil
	}

	seconds := intFromConfig(node.Config, "seconds", 0)
	if seconds <= 0 {
		return nodeOutcome{outcome: "default"}, nil
	}
	if seconds > 3600 {
		seconds = 3600
	}

	resumeAt := time.Now().Add(time.Duration(seconds) * time.Second).Unix()
	ctx.session.SessionData["__delay_pending"] = node.ID
	ctx.session.SessionData["__delay_resume_at"] = resumeAt
	a.scheduleChatDelay(ctx.session.ID, seconds)
	return nodeOutcome{yield: true}, nil
}

// execChatResetSession clears custom SessionData (keeps path audit +
// builtins will be re-seeded next turn), resets step retries, and
// continues via the default edge.
func (a *App) execChatResetSession(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	_ = node
	preserved := models.JSONB{}
	if ctx.session.SessionData != nil {
		if path, ok := ctx.session.SessionData["__path__"]; ok {
			preserved["__path__"] = path
		}
	}
	ctx.session.SessionData = preserved
	ctx.session.StepRetries = 0
	// Re-seed builtins immediately so following nodes in this turn still work.
	seedChatSessionBuiltins(ctx.session, ctx.contact, ctx.userInput, nil)
	return nodeOutcome{outcome: "default"}, nil
}

// execChatSetLabels adds/removes tags on the contact and mirrors them into SessionData["tags"].
// Config: { "add": ["vip"], "remove": ["lead"] }
func (a *App) execChatSetLabels(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	add := stringSliceFromConfig(node.Config, "add", "labelsToAdd")
	remove := stringSliceFromConfig(node.Config, "remove", "labelsToRemove")

	tagSet := map[string]struct{}{}
	if ctx.contact.Tags != nil {
		for _, t := range ctx.contact.Tags {
			if s, ok := t.(string); ok && s != "" {
				tagSet[s] = struct{}{}
			}
		}
	}
	for _, t := range remove {
		delete(tagSet, t)
	}
	for _, t := range add {
		if t != "" {
			tagSet[t] = struct{}{}
		}
	}
	tags := make(models.JSONBArray, 0, len(tagSet))
	tagStrings := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
		tagStrings = append(tagStrings, t)
	}
	sort.Strings(tagStrings)
	if err := a.DB.Model(ctx.contact).Update("tags", tags).Error; err != nil {
		return nodeOutcome{}, fmt.Errorf("update tags: %w", err)
	}
	ctx.contact.Tags = tags
	if ctx.session.SessionData == nil {
		ctx.session.SessionData = models.JSONB{}
	}
	ctx.session.SessionData["tags"] = tagStrings
	return nodeOutcome{outcome: "default"}, nil
}

// execChatDisableBot mutes chatbot automation for this contact for a duration.
// Config: { "hours": 1, "minutes": 30 }
func (a *App) execChatDisableBot(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	hours := intFromConfig(node.Config, "hours", 0)
	minutes := intFromConfig(node.Config, "minutes", 0)
	total := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	if total <= 0 {
		return nodeOutcome{outcome: "default"}, nil
	}
	until := time.Now().Add(total)
	meta := ctx.contact.Metadata
	if meta == nil {
		meta = models.JSONB{}
	}
	meta["bot_muted_until"] = until.UTC().Format(time.RFC3339)
	if err := a.DB.Model(ctx.contact).Update("metadata", meta).Error; err != nil {
		return nodeOutcome{}, fmt.Errorf("mute bot: %w", err)
	}
	ctx.contact.Metadata = meta
	return nodeOutcome{outcome: "default"}, nil
}

// execChatSendEmail sends HTML email via org SMTP settings.
// Config: { "to", "subject", "html", "from?" } — no SMTP secrets in graph.
func (a *App) execChatSendEmail(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	cfg, err := a.loadOrgSMTP(ctx.account.OrganizationID)
	if err != nil {
		a.Log.Warn("send_email: smtp not configured", "node", node.ID, "error", err)
		return nodeOutcome{outcome: "error"}, nil
	}
	to := processTemplate(stringFromConfig(node.Config, "to"), ctx.session.SessionData)
	subject := processTemplate(stringFromConfig(node.Config, "subject"), ctx.session.SessionData)
	htmlBody := processTemplate(stringFromConfig(node.Config, "html", "body"), ctx.session.SessionData)
	from := processTemplate(stringFromConfig(node.Config, "from"), ctx.session.SessionData)
	recipients := mail.SplitAddresses(to)
	if len(recipients) == 0 || subject == "" {
		a.Log.Warn("send_email: missing to/subject", "node", node.ID)
		return nodeOutcome{outcome: "error"}, nil
	}
	if err := mail.Send(cfg, mail.Message{From: from, To: recipients, Subject: subject, HTML: htmlBody}); err != nil {
		a.Log.Error("send_email failed", "node", node.ID, "session", ctx.session.ID, "error", err)
		return nodeOutcome{outcome: "error"}, nil
	}
	return nodeOutcome{outcome: "default"}, nil
}

func (a *App) loadOrgSMTP(orgID uuid.UUID) (mail.SMTPConfig, error) {
	var org models.Organization
	if err := a.DB.Select("settings").Where("id = ?", orgID).First(&org).Error; err != nil {
		return mail.SMTPConfig{}, err
	}
	if org.Settings == nil {
		return mail.SMTPConfig{}, fmt.Errorf("no org settings")
	}
	host, _ := org.Settings["smtp_host"].(string)
	if host == "" {
		return mail.SMTPConfig{}, fmt.Errorf("smtp_host empty")
	}
	port := 587
	if v, ok := org.Settings["smtp_port"].(float64); ok && v > 0 {
		port = int(v)
	} else if v, ok := org.Settings["smtp_port"].(int); ok && v > 0 {
		port = v
	}
	user, _ := org.Settings["smtp_username"].(string)
	from, _ := org.Settings["smtp_from"].(string)
	sec, _ := org.Settings["smtp_security"].(string)
	pass := ""
	if enc, ok := org.Settings["smtp_password_encrypted"].(string); ok && enc != "" {
		dec, err := crypto.Decrypt(enc, a.Config.App.EncryptionKey)
		if err != nil {
			return mail.SMTPConfig{}, fmt.Errorf("decrypt smtp password: %w", err)
		}
		pass = dec
	}
	return mail.SMTPConfig{
		Host: host, Port: port, Username: user, Password: pass, From: from, Security: sec,
	}, nil
}

// execChatPhonebook adds/removes the sender (or templated mobile) on a ContactList.
// Config: { "action": "add"|"remove", "list_id", "remove_from_all", "mobile", "name" }
func (a *App) execChatPhonebook(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	action := strings.ToLower(stringFromConfig(node.Config, "action"))
	if action == "" {
		action = "add"
	}
	mobile := processTemplate(stringFromConfig(node.Config, "mobile"), ctx.session.SessionData)
	if mobile == "" {
		mobile = ctx.session.PhoneNumber
	}
	name := processTemplate(stringFromConfig(node.Config, "name"), ctx.session.SessionData)
	if name == "" && ctx.contact != nil {
		name = ctx.contact.ProfileName
	}
	removeAll := boolFromConfig(node.Config, "remove_from_all") || boolFromConfig(node.Config, "removeFromAll")

	if action == "remove" && removeAll {
		if err := a.DB.Where("organization_id = ? AND phone_number = ?", ctx.account.OrganizationID, mobile).
			Delete(&models.ContactListMember{}).Error; err != nil {
			return nodeOutcome{}, fmt.Errorf("phonebook remove all: %w", err)
		}
		return nodeOutcome{outcome: "default"}, nil
	}

	listIDStr := stringFromConfig(node.Config, "list_id", "phonebook")
	listID, err := uuid.Parse(listIDStr)
	if err != nil {
		a.Log.Warn("phonebook node missing/invalid list_id", "node", node.ID, "list_id", listIDStr)
		return nodeOutcome{outcome: "error"}, nil
	}
	var list models.ContactList
	if err := a.DB.Where("id = ? AND organization_id = ?", listID, ctx.account.OrganizationID).First(&list).Error; err != nil {
		a.Log.Warn("phonebook list not found", "node", node.ID, "list_id", listID)
		return nodeOutcome{outcome: "error"}, nil
	}

	if action == "remove" {
		if err := a.DB.Where("list_id = ? AND phone_number = ?", listID, mobile).
			Delete(&models.ContactListMember{}).Error; err != nil {
			return nodeOutcome{}, fmt.Errorf("phonebook remove: %w", err)
		}
		return nodeOutcome{outcome: "default"}, nil
	}

	var existing models.ContactListMember
	err = a.DB.Where("list_id = ? AND phone_number = ?", listID, mobile).First(&existing).Error
	contactID := &ctx.contact.ID
	if errors.Is(err, gorm.ErrRecordNotFound) {
		member := models.ContactListMember{
			BaseModel:      models.BaseModel{ID: uuid.New()},
			ListID:         listID,
			OrganizationID: ctx.account.OrganizationID,
			PhoneNumber:    mobile,
			Name:           name,
			ContactID:      contactID,
			Vars:           models.JSONB{},
		}
		if err := a.DB.Create(&member).Error; err != nil {
			return nodeOutcome{}, fmt.Errorf("phonebook add: %w", err)
		}
	} else if err != nil {
		return nodeOutcome{}, err
	} else {
		updates := map[string]any{"name": name, "contact_id": contactID}
		if err := a.DB.Model(&existing).Updates(updates).Error; err != nil {
			return nodeOutcome{}, err
		}
	}
	return nodeOutcome{outcome: "default"}, nil
}

// execChatGoogleService runs Sheets/Calendar/Drive/Docs using an org credential.
// Config:
//
//	{
//	  "credential_id": "<uuid>",
//	  "action": "sheets_append" | "sheets_read" | "sheets_update"
//	         | "calendar_create_event" | "calendar_list_events" | "calendar_get_event" | "calendar_delete_event"
//	         | "drive_list_files" | "drive_create_folder" | "drive_upload_file"
//	         | "docs_create" | "docs_append" | "docs_read",
//	  ... action-specific fields; secrets never in graph — only credential_id
//	}
func (a *App) execChatGoogleService(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	credIDStr := stringFromConfig(node.Config, "credential_id")
	credID, err := uuid.Parse(credIDStr)
	if err != nil {
		a.Log.Warn("google_service missing credential_id", "node", node.ID)
		return nodeOutcome{outcome: "error"}, nil
	}
	var cred models.GoogleCredential
	if err := a.DB.Where("id = ? AND organization_id = ?", credID, ctx.account.OrganizationID).First(&cred).Error; err != nil {
		a.Log.Warn("google_service credential not found", "node", node.ID)
		return nodeOutcome{outcome: "error"}, nil
	}
	plain, err := crypto.Decrypt(cred.EncryptedJSON, a.Config.App.EncryptionKey)
	if err != nil {
		return nodeOutcome{}, fmt.Errorf("decrypt google cred: %w", err)
	}
	client, err := wmgoogle.NewClient(context.Background(), []byte(plain))
	if err != nil {
		a.Log.Error("google_service client", "error", err)
		return nodeOutcome{outcome: "error"}, nil
	}

	action := strings.ToLower(stringFromConfig(node.Config, "action"))
	if ctx.session.SessionData == nil {
		ctx.session.SessionData = models.JSONB{}
	}
	session := ctx.session.SessionData
	outVar := stringFromConfig(node.Config, "output_variable", "outputVariable")
	bg := context.Background()

	switch action {
	case "sheets_append", "append", "":
		spreadsheetID := processTemplate(stringFromConfig(node.Config, "spreadsheet_id", "sheet_id"), session)
		sheetName := processTemplate(stringFromConfig(node.Config, "sheet_name", "sheetName"), session)
		rows := parseSheetValues(node.Config, session)
		if len(rows) == 0 {
			a.Log.Warn("google_service append: no values", "node", node.ID)
			return nodeOutcome{outcome: "error"}, nil
		}
		if err := client.AppendRows(bg, spreadsheetID, sheetName, rows); err != nil {
			a.Log.Error("sheets_append failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
	case "sheets_read", "read":
		spreadsheetID := processTemplate(stringFromConfig(node.Config, "spreadsheet_id", "sheet_id"), session)
		rangeA1 := processTemplate(stringFromConfig(node.Config, "range"), session)
		values, err := client.ReadRange(bg, spreadsheetID, rangeA1)
		if err != nil {
			a.Log.Error("sheets_read failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "sheetData"
		}
		if (boolFromConfig(node.Config, "map_first_row") || boolFromConfig(node.Config, "mapFirstRow")) && len(values) >= 2 {
			headers := values[0]
			row := values[1]
			mapped := map[string]any{}
			for i, h := range headers {
				key := fmt.Sprint(h)
				if i < len(row) {
					mapped[key] = row[i]
				}
			}
			session[outVar] = mapped
		} else {
			session[outVar] = values
		}
	case "sheets_update", "update":
		spreadsheetID := processTemplate(stringFromConfig(node.Config, "spreadsheet_id", "sheet_id"), session)
		rangeA1 := processTemplate(stringFromConfig(node.Config, "range"), session)
		rows := parseSheetValues(node.Config, session)
		if err := client.UpdateRange(bg, spreadsheetID, rangeA1, rows); err != nil {
			a.Log.Error("sheets_update failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}

	case "calendar_create_event":
		calID := processTemplate(stringFromConfig(node.Config, "calendar_id", "calendarId"), session)
		attendees := mail.SplitAddresses(processTemplate(stringFromConfig(node.Config, "attendees"), session))
		evID, err := client.CreateEvent(bg, calID, wmgoogle.CalendarEvent{
			Summary:     processTemplate(stringFromConfig(node.Config, "event_summary", "eventSummary", "summary"), session),
			Description: processTemplate(stringFromConfig(node.Config, "event_description", "eventDescription", "description"), session),
			Start:       processTemplate(stringFromConfig(node.Config, "start", "start_iso"), session),
			End:         processTemplate(stringFromConfig(node.Config, "end", "end_iso"), session),
			TimeZone:    processTemplate(stringFromConfig(node.Config, "timezone", "timeZone"), session),
			Attendees:   attendees,
		})
		if err != nil {
			a.Log.Error("calendar_create_event failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "eventId"
		}
		session[outVar] = evID

	case "calendar_list_events":
		calID := processTemplate(stringFromConfig(node.Config, "calendar_id", "calendarId"), session)
		maxN := intFromConfig(node.Config, "max_results", 10)
		items, err := client.ListEvents(bg, calID, maxN)
		if err != nil {
			a.Log.Error("calendar_list_events failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "events"
		}
		session[outVar] = items

	case "calendar_get_event":
		calID := processTemplate(stringFromConfig(node.Config, "calendar_id", "calendarId"), session)
		evID := processTemplate(stringFromConfig(node.Config, "event_id", "eventId"), session)
		item, err := client.GetEvent(bg, calID, evID)
		if err != nil {
			a.Log.Error("calendar_get_event failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "event"
		}
		session[outVar] = item

	case "calendar_delete_event":
		calID := processTemplate(stringFromConfig(node.Config, "calendar_id", "calendarId"), session)
		evID := processTemplate(stringFromConfig(node.Config, "event_id", "eventId"), session)
		if err := client.DeleteEvent(bg, calID, evID); err != nil {
			a.Log.Error("calendar_delete_event failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}

	case "drive_list_files":
		parent := processTemplate(stringFromConfig(node.Config, "parent_id", "folder_id"), session)
		files, err := client.ListFiles(bg, parent, intFromConfig(node.Config, "max_results", 20))
		if err != nil {
			a.Log.Error("drive_list_files failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "driveFiles"
		}
		session[outVar] = files

	case "drive_create_folder":
		name := processTemplate(stringFromConfig(node.Config, "name", "folder_name"), session)
		parent := processTemplate(stringFromConfig(node.Config, "parent_id", "folder_id"), session)
		id, err := client.CreateFolder(bg, name, parent)
		if err != nil {
			a.Log.Error("drive_create_folder failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "folderId"
		}
		session[outVar] = id

	case "drive_upload_file":
		name := processTemplate(stringFromConfig(node.Config, "name", "file_name"), session)
		content := processTemplate(stringFromConfig(node.Config, "content", "text"), session)
		parent := processTemplate(stringFromConfig(node.Config, "parent_id", "folder_id"), session)
		id, err := client.UploadTextFile(bg, name, content, parent)
		if err != nil {
			a.Log.Error("drive_upload_file failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "fileId"
		}
		session[outVar] = id

	case "docs_create":
		title := processTemplate(stringFromConfig(node.Config, "title", "name"), session)
		id, err := client.CreateDoc(bg, title)
		if err != nil {
			a.Log.Error("docs_create failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "documentId"
		}
		session[outVar] = id

	case "docs_append":
		docID := processTemplate(stringFromConfig(node.Config, "document_id", "documentId"), session)
		text := processTemplate(stringFromConfig(node.Config, "text", "content"), session)
		if err := client.AppendDocText(bg, docID, text); err != nil {
			a.Log.Error("docs_append failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}

	case "docs_read":
		docID := processTemplate(stringFromConfig(node.Config, "document_id", "documentId"), session)
		text, err := client.ReadDoc(bg, docID)
		if err != nil {
			a.Log.Error("docs_read failed", "node", node.ID, "error", err)
			return nodeOutcome{outcome: "error"}, nil
		}
		if outVar == "" {
			outVar = "docText"
		}
		session[outVar] = text

	default:
		a.Log.Warn("google_service unknown action", "action", action, "node", node.ID)
		return nodeOutcome{outcome: "error"}, nil
	}
	return nodeOutcome{outcome: "default"}, nil
}

// parseSheetValues builds rows from config.values (array) or config.row (object → single row).
func parseSheetValues(cfg map[string]any, session models.JSONB) [][]any {
	if raw, ok := cfg["values"]; ok {
		switch v := raw.(type) {
		case string:
			templated := processTemplate(v, session)
			var rows [][]any
			if err := json.Unmarshal([]byte(templated), &rows); err == nil {
				return rows
			}
		case []any:
			out := make([][]any, 0, len(v))
			for _, row := range v {
				switch r := row.(type) {
				case []any:
					cells := make([]any, len(r))
					for i, c := range r {
						if s, ok := c.(string); ok {
							cells[i] = processTemplate(s, session)
						} else {
							cells[i] = c
						}
					}
					out = append(out, cells)
				}
			}
			return out
		}
	}
	if raw, ok := cfg["row"].(map[string]any); ok {
		// Preserve insertion order is not guaranteed; sort keys for stability.
		keys := make([]string, 0, len(raw))
		for k := range raw {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		row := make([]any, 0, len(keys))
		for _, k := range keys {
			val := raw[k]
			if s, ok := val.(string); ok {
				row = append(row, processTemplate(s, session))
			} else {
				row = append(row, val)
			}
		}
		return [][]any{row}
	}
	return nil
}

// stringSliceFromConfig reads a []string / []any config value.
func stringSliceFromConfig(cfg map[string]any, keys ...string) []string {
	for _, key := range keys {
		raw, ok := cfg[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case []string:
			return v
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

// execChatEnd optionally sends a final message and returns an empty
// outcome. The runner sees no matching edge and marks the session
// completed.
// Config: { "message": "..." } (optional)
func (a *App) execChatEnd(node *ChatNode, ctx *chatNodeCtx) (nodeOutcome, error) {
	if msg := stringFromConfig(node.Config, "message"); msg != "" {
		msg = processTemplate(msg, ctx.session.SessionData)
		if err := a.sendAndSaveTextMessage(ctx.account, ctx.contact, msg); err != nil {
			return nodeOutcome{}, fmt.Errorf("send end message: %w", err)
		}
		a.logSessionMessage(ctx.session.ID, models.DirectionOutgoing, msg, node.ID)
	}
	return nodeOutcome{}, nil
}

// persistChatSession writes the running session state back to the DB.
// Variables, current node, and the __path__ trail all live in SessionData
// + dedicated columns. Called after every yield and on the completion path.
func (a *App) persistChatSession(s *models.ChatbotSession) error {
	s.LastActivityAt = time.Now()
	if s.Status == models.SessionStatusCompleted && s.CompletedAt == nil {
		now := time.Now()
		s.CompletedAt = &now
	}
	if err := a.DB.Save(s).Error; err != nil {
		a.Log.Error("persist chat session", "session", s.ID, "error", err)
		return err
	}
	return nil
}

// appendChatPath records the executed node + outcome in SessionData["__path__"].
// The shape mirrors IVRContext.Path so frontends and audit tooling can
// render either domain's trail with the same code.
func appendChatPath(s *models.ChatbotSession, node *ChatNode, outcome string) {
	if s.SessionData == nil {
		s.SessionData = models.JSONB{}
	}
	entry := map[string]any{
		"node":    node.ID,
		"type":    string(node.Type),
		"label":   node.Label,
		"outcome": outcome,
	}
	path, _ := s.SessionData["__path__"].([]any)
	path = append(path, entry)
	s.SessionData["__path__"] = path
}

// stringFromConfig returns the first non-empty string at any of the given keys.
func stringFromConfig(cfg map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := cfg[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// intFromConfig returns an int value from config at the given key, falling
// back to def. JSON numbers decode as float64 in map[string]any, so accept
// both float64 and int.
func intFromConfig(cfg map[string]any, key string, def int) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

// boolFromConfig returns a bool from config. Accepts bool or common string forms.
func boolFromConfig(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return false
}

// floatFromConfig returns a float64 from config (JSON numbers / strings).
func floatFromConfig(cfg map[string]any, key string) float64 {
	switch v := cfg[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		var f float64
		if _, err := fmt.Sscan(v, &f); err == nil {
			return f
		}
	}
	return 0
}

// buttonsFromConfig normalizes node.Config["buttons"] into the shape the
// existing sendAndSaveInteractiveButtons helper expects.
// Accepts: [{"id": "...", "title": "...", "type": "..."(optional)}, ...]
func buttonsFromConfig(cfg map[string]any) []map[string]any {
	raw, ok := cfg["buttons"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// listSectionsFromConfig normalizes Config["sections"] into WhatsApp list sections.
func listSectionsFromConfig(cfg map[string]any) []whatsapp.ListSection {
	raw, ok := cfg["sections"].([]any)
	if !ok {
		return nil
	}
	out := make([]whatsapp.ListSection, 0, len(raw))
	for _, item := range raw {
		secMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sec := whatsapp.ListSection{
			Title: stringFromConfig(secMap, "title"),
		}
		rowsRaw, _ := secMap["rows"].([]any)
		for _, rowItem := range rowsRaw {
			rowMap, ok := rowItem.(map[string]any)
			if !ok {
				continue
			}
			id := stringFromConfig(rowMap, "id")
			title := stringFromConfig(rowMap, "title")
			if id == "" || title == "" {
				continue
			}
			sec.Rows = append(sec.Rows, whatsapp.ListRow{
				ID:          id,
				Title:       title,
				Description: stringFromConfig(rowMap, "description"),
			})
		}
		if len(sec.Rows) > 0 {
			out = append(out, sec)
		}
	}
	return out
}
