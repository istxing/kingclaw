// KingClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 KingClaw contributors

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/istxing/kingclaw/pkg/bus"
	"github.com/istxing/kingclaw/pkg/channels"
	"github.com/istxing/kingclaw/pkg/config"
	"github.com/istxing/kingclaw/pkg/constants"
	"github.com/istxing/kingclaw/pkg/logger"
	"github.com/istxing/kingclaw/pkg/providers"
	"github.com/istxing/kingclaw/pkg/routing"
	"github.com/istxing/kingclaw/pkg/runs"
	"github.com/istxing/kingclaw/pkg/skills"
	"github.com/istxing/kingclaw/pkg/state"
	"github.com/istxing/kingclaw/pkg/tools"
	"github.com/istxing/kingclaw/pkg/utils"
)

type AgentLoop struct {
	bus            *bus.MessageBus
	cfg            *config.Config
	registry       *AgentRegistry
	state          *state.Manager
	running        atomic.Bool
	summarizing    sync.Map
	summaryNotice  sync.Map
	recentActions  sync.Map
	fallback       *providers.FallbackChain
	channelManager *channels.Manager
}

type actionReceipt struct {
	RunID     string
	Tool      string
	Status    string
	ErrorCode string
	Error     string
	TS        int64
}

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey      string // Session identifier for history/context
	Channel         string // Target channel for tool execution
	ChatID          string // Target chat ID for tool execution
	UserMessage     string // User message content (may include prefix)
	DefaultResponse string // Response when LLM returns empty
	EnableSummary   bool   // Whether to trigger summarization
	SendResponse    bool   // Whether to send response via bus
	NoHistory       bool   // If true, don't load session history (for heartbeat)
	ThinkingLevel   string // Session-level thinking override
	VerboseLevel    string // Session-level verbosity override
}

type llmIterationMeta struct {
	SelectedModel    string
	FallbackAttempts string
}

func NewAgentLoop(cfg *config.Config, msgBus *bus.MessageBus, provider providers.LLMProvider) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider)

	// Register shared tools to all agents
	registerSharedTools(cfg, msgBus, registry, provider)

	// Set up shared fallback chain
	cooldown := providers.NewCooldownTracker()
	fallbackChain := providers.NewFallbackChain(cooldown)
	fallbackChain.SetRetryBudget(providers.FailoverRateLimit, max(0, cfg.Agents.Defaults.FallbackRetryRateLimit))
	fallbackChain.SetRetryBudget(providers.FailoverTimeout, max(0, cfg.Agents.Defaults.FallbackRetryTimeout))
	fallbackChain.SetRetryBudget(providers.FailoverAuth, max(0, cfg.Agents.Defaults.FallbackRetryAuth))
	fallbackChain.SetRetryBudget(providers.FailoverBilling, max(0, cfg.Agents.Defaults.FallbackRetryBilling))

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	return &AgentLoop{
		bus:         msgBus,
		cfg:         cfg,
		registry:    registry,
		state:       stateManager,
		summarizing: sync.Map{},
		fallback:    fallbackChain,
	}
}

// registerSharedTools registers tools that are shared across all agents (web, message, spawn).
func registerSharedTools(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
) {
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		// Web tools
		if searchTool := tools.NewWebSearchTool(tools.WebSearchToolOptions{
			BraveAPIKey:          cfg.Tools.Web.Brave.APIKey,
			BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
			BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
			DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
			DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
			PerplexityAPIKey:     cfg.Tools.Web.Perplexity.APIKey,
			PerplexityMaxResults: cfg.Tools.Web.Perplexity.MaxResults,
			PerplexityEnabled:    cfg.Tools.Web.Perplexity.Enabled,
		}); searchTool != nil {
			agent.Tools.Register(searchTool)
		}
		agent.Tools.Register(tools.NewWebFetchTool(50000))

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		agent.Tools.Register(tools.NewI2CTool())
		agent.Tools.Register(tools.NewSPITool())

		// Message tool
		messageTool := tools.NewMessageTool(agent.Workspace)
		messageTool.SetSendCallback(func(channel, chatID, content string) error {
			msgBus.PublishOutbound(bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: content,
			})
			return nil
		})
		agent.Tools.Register(messageTool)

		// Skill discovery and installation tools
		registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
			MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
			ClawHub:               skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
		})
		searchCache := skills.NewSearchCache(
			cfg.Tools.Skills.SearchCache.MaxSize,
			time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
		)
		agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
		agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))

		// Spawn tool with allowlist checker
		subagentManager := tools.NewSubagentManager(provider, agent.Model, agent.Workspace, msgBus)
		subagentManager.SetLLMOptions(agent.MaxTokens, agent.Temperature)
		spawnTool := tools.NewSpawnTool(subagentManager)
		currentAgentID := agentID
		spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
			return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
		})
		agent.Tools.Register(spawnTool)

		// Update context builder with the complete tools registry
		agent.ContextBuilder.SetToolsRegistry(agent.Tools)
	}
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	for al.running.Load() {
		select {
		case <-ctx.Done():
			return nil
		default:
			msg, ok := al.bus.ConsumeInbound(ctx)
			if !ok {
				continue
			}

			response, err := al.processMessage(ctx, msg)
			if err != nil {
				response = fmt.Sprintf("Error processing message: %v", err)
			}

			if response != "" {
				// Suppress only true duplicates: if message tool already sent the same
				// content to the same target in this turn, skip final publish.
				suppressDuplicate := false
				defaultAgent := al.registry.GetDefaultAgent()
				if defaultAgent != nil {
					if tool, ok := defaultAgent.Tools.Get("message"); ok {
						if mt, ok := tool.(*tools.MessageTool); ok {
							suppressDuplicate = mt.HasSentMatchingInRound(msg.Channel, msg.ChatID, response)
						}
					}
				}

				if !suppressDuplicate {
					al.bus.PublishOutbound(bus.OutboundMessage{
						Channel: msg.Channel,
						ChatID:  msg.ChatID,
						Content: response,
					})
				}
			}
		}
	}

	return nil
}

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	for _, agentID := range al.registry.ListAgentIDs() {
		if agent, ok := al.registry.GetAgent(agentID); ok {
			agent.Tools.Register(tool)
		}
	}
}

func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.channelManager = cm
}

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

func (al *AgentLoop) ProcessDirect(ctx context.Context, content, sessionKey string) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
	}

	return al.processMessage(ctx, msg)
}

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.
func (al *AgentLoop) ProcessHeartbeat(ctx context.Context, content, channel, chatID string) (string, error) {
	agent := al.registry.GetDefaultAgent()
	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      "heartbeat",
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true, // Don't load session history for heartbeat
	})
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	// Add message preview to log (show full content for error messages)
	var logContent string
	if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
		logContent = msg.Content // Full content for errors
	} else {
		logContent = utils.Truncate(msg.Content, 80)
	}
	logger.InfoCF("agent", fmt.Sprintf("Processing message from %s:%s: %s", msg.Channel, msg.SenderID, logContent),
		map[string]any{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
		})

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	// Check for commands
	if response, handled := al.handleCommand(ctx, msg); handled {
		return response, nil
	}

	// Route to determine agent and session key
	route := al.registry.ResolveRoute(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  msg.Metadata["account_id"],
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		GuildID:    msg.Metadata["guild_id"],
		TeamID:     msg.Metadata["team_id"],
	})

	agent, ok := al.registry.GetAgent(route.AgentID)
	if !ok {
		agent = al.registry.GetDefaultAgent()
	}

	// Use routed session key, but honor pre-set agent-scoped keys (for ProcessDirect/cron)
	sessionKey := route.SessionKey
	if msg.SessionKey != "" && strings.HasPrefix(msg.SessionKey, "agent:") {
		sessionKey = msg.SessionKey
	}

	logger.InfoCF("agent", "Routed message",
		map[string]any{
			"agent_id":    agent.ID,
			"session_key": sessionKey,
			"matched_by":  route.MatchedBy,
		})

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		UserMessage:     msg.Content,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   true,
		SendResponse:    false,
	})
}

func (al *AgentLoop) processSystemMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf("processSystemMessage called with non-system message channel: %s", msg.Channel)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel, originChatID string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
		originChatID = msg.ChatID[idx+1:]
	} else {
		originChannel = "cli"
		originChatID = msg.ChatID
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// Skip internal channels - only log, don't send to user
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	// Use default agent for system messages
	agent := al.registry.GetDefaultAgent()

	// Use the origin session for context
	sessionKey := routing.BuildAgentMainSessionKey(agent.ID)

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		EnableSummary:   false,
		SendResponse:    true,
	})
}

// runAgentLoop is the core message processing logic.
func (al *AgentLoop) runAgentLoop(ctx context.Context, agent *AgentInstance, opts processOptions) (string, error) {
	runID := fmt.Sprintf("agent-%d", time.Now().UnixNano())
	startAt := time.Now()

	appendAgentRun := func(status, errCode, errMsg, selectedModel, attempts, thinking, verbose, summary string) {
		if strings.TrimSpace(al.cfg.WorkspacePath()) == "" {
			return
		}
		_ = runs.NewService(al.cfg.WorkspacePath()).Append(runs.Entry{
			RunID:        runID,
			Status:       status,
			TS:           startAt.UnixMilli(),
			DurationMS:   time.Since(startAt).Milliseconds(),
			Model:        strings.TrimSpace(selectedModel),
			Attempts:     strings.TrimSpace(attempts),
			Thinking:     strings.TrimSpace(thinking),
			Verbose:      strings.TrimSpace(verbose),
			ErrorCode:    errCode,
			ErrorMessage: errMsg,
			Error:        errMsg,
			Source:       "agent_llm",
			Summary:      summary,
		})
	}

	// Resolve per-session overrides if not explicitly provided.
	if opts.ThinkingLevel == "" {
		opts.ThinkingLevel = strings.TrimSpace(agent.Sessions.GetThinkingLevel(opts.SessionKey))
	}
	if opts.VerboseLevel == "" {
		opts.VerboseLevel = strings.TrimSpace(agent.Sessions.GetVerboseLevel(opts.SessionKey))
	}

	// 0. Record last channel for heartbeat notifications (skip internal channels)
	if opts.Channel != "" && opts.ChatID != "" {
		// Don't record internal channels (cli, system, subagent)
		if !constants.IsInternalChannel(opts.Channel) {
			channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
			if err := al.RecordLastChannel(channelKey); err != nil {
				logger.WarnCF("agent", "Failed to record last channel", map[string]any{"error": err.Error()})
			}
		}
	}

	// 1. Update tool contexts
	al.updateToolContexts(agent, opts.Channel, opts.ChatID)

	// 2. Build messages (skip history for heartbeat)
	var history []providers.Message
	var summary string
	if !opts.NoHistory {
		history = agent.Sessions.GetHistory(opts.SessionKey)
		summary = agent.Sessions.GetSummary(opts.SessionKey)
	}
	messages := agent.ContextBuilder.BuildMessages(
		history,
		summary,
		al.injectActionContext(opts.SessionKey, opts.UserMessage),
		nil,
		opts.Channel,
		opts.ChatID,
	)

	// 3. Save user message to session
	agent.Sessions.AddMessage(opts.SessionKey, "user", opts.UserMessage)

	// 4. Run LLM iteration loop
	finalContent, iteration, llmMeta, err := al.runLLMIteration(ctx, agent, messages, opts)
	if err != nil {
		appendAgentRun(
			"failed",
			"llm_failed",
			err.Error(),
			llmMeta.SelectedModel,
			llmMeta.FallbackAttempts,
			opts.ThinkingLevel,
			opts.VerboseLevel,
			fmt.Sprintf("agent=%s status=failed model=%s", agent.ID, llmMeta.SelectedModel),
		)
		return "", err
	}

	// If last tool had ForUser content and we already sent it, we might not need to send final response
	// This is controlled by the tool's Silent flag and ForUser content

	// 5. Handle empty response
	if finalContent == "" {
		finalContent = opts.DefaultResponse
	}
	finalContent = al.applyExecutionResponseProtocol(opts.SessionKey, opts.UserMessage, finalContent)
	finalContent = enforceInferenceLabel(finalContent)

	// 6. Save final assistant message to session
	agent.Sessions.AddMessage(opts.SessionKey, "assistant", finalContent)
	agent.Sessions.Save(opts.SessionKey)

	// 7. Optional: summarization
	if opts.EnableSummary {
		al.maybeSummarize(agent, opts.SessionKey, opts.Channel, opts.ChatID)
	}

	// 8. Optional: send response via bus
	if opts.SendResponse {
		al.bus.PublishOutbound(bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: finalContent,
		})
	}

	// 9. Log response
	responsePreview := utils.Truncate(finalContent, 120)
	logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
		map[string]any{
			"agent_id":     agent.ID,
			"session_key":  opts.SessionKey,
			"iterations":   iteration,
			"thinking":     opts.ThinkingLevel,
			"verbose":      opts.VerboseLevel,
			"final_length": len(finalContent),
		})

	appendAgentRun(
		"completed",
		"",
		"",
		llmMeta.SelectedModel,
		llmMeta.FallbackAttempts,
		opts.ThinkingLevel,
		opts.VerboseLevel,
		fmt.Sprintf("agent=%s status=completed model=%s", agent.ID, llmMeta.SelectedModel),
	)

	return finalContent, nil
}

// runLLMIteration executes the LLM call loop with tool handling.
func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
) (string, int, llmIterationMeta, error) {
	iteration := 0
	var finalContent string
	meta := llmIterationMeta{SelectedModel: agent.Model}

	for iteration < agent.MaxIterations {
		iteration++

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  agent.ID,
				"iteration": iteration,
				"max":       agent.MaxIterations,
			})

		// Build tool definitions
		providerToolDefs := agent.Tools.ToProviderDefs()

		// Log LLM request details
		candidateRefs := fallbackCandidateRefs(agent.Candidates)
		logger.DebugCF("agent", "LLM request",
			map[string]any{
				"agent_id":          agent.ID,
				"iteration":         iteration,
				"model":             agent.Model,
				"candidates":        candidateRefs,
				"messages_count":    len(messages),
				"tools_count":       len(providerToolDefs),
				"max_tokens":        agent.MaxTokens,
				"temperature":       agent.Temperature,
				"thinking":          opts.ThinkingLevel,
				"verbose":           opts.VerboseLevel,
				"system_prompt_len": len(messages[0].Content),
			})

		// Log full messages (detailed)
		logger.DebugCF("agent", "Full LLM request",
			map[string]any{
				"iteration":     iteration,
				"messages_json": formatMessagesForLog(messages),
				"tools_json":    formatToolsForLog(providerToolDefs),
			})

		// Call LLM with fallback chain if candidates are configured.
		var response *providers.LLMResponse
		var err error
		modelUsed := agent.Model
		fallbackTrace := ""

		callLLM := func() (*providers.LLMResponse, error) {
			llmOpts := map[string]any{
				"max_tokens":  agent.MaxTokens,
				"temperature": agent.Temperature,
			}
			if opts.ThinkingLevel != "" {
				llmOpts["thinking_level"] = opts.ThinkingLevel
			}
			if opts.VerboseLevel != "" {
				llmOpts["verbose_level"] = opts.VerboseLevel
			}
			if len(agent.Candidates) > 1 && al.fallback != nil {
				fbResult, fbErr := al.fallback.Execute(ctx, agent.Candidates,
					func(ctx context.Context, provider, model string) (*providers.LLMResponse, error) {
						return agent.Provider.Chat(ctx, messages, providerToolDefs, model, llmOpts)
					},
				)
				if fbErr != nil {
					if exhausted, ok := fbErr.(*providers.FallbackExhaustedError); ok {
						trace := formatFallbackAttemptChain(exhausted.Attempts)
						logger.WarnCF("agent", "LLM fallback exhausted", map[string]any{
							"agent_id":  agent.ID,
							"iteration": iteration,
							"attempts":  trace,
						})
					}
					return nil, fbErr
				}
				fallbackTrace = formatFallbackAttemptChain(fbResult.Attempts)
				if fbResult.Provider != "" {
					modelUsed = fmt.Sprintf("%s/%s", fbResult.Provider, fbResult.Model)
				}
				if fallbackTrace != "" {
					logger.InfoCF("agent", "LLM fallback attempts", map[string]any{
						"agent_id":    agent.ID,
						"iteration":   iteration,
						"selected":    modelUsed,
						"attempts":    fallbackTrace,
						"candidates":  candidateRefs,
						"attempt_cnt": len(fbResult.Attempts),
					})
				}
				return fbResult.Response, nil
			}
			return agent.Provider.Chat(ctx, messages, providerToolDefs, agent.Model, llmOpts)
		}

		// Retry loop for context/token errors
		maxRetries := 2
		for retry := 0; retry <= maxRetries; retry++ {
			response, err = callLLM()
			if err == nil {
				break
			}

			errMsg := strings.ToLower(err.Error())
			isContextError := strings.Contains(errMsg, "token") ||
				strings.Contains(errMsg, "context") ||
				strings.Contains(errMsg, "invalidparameter") ||
				strings.Contains(errMsg, "length")

			if isContextError && retry < maxRetries {
				logger.WarnCF("agent", "Context window error detected, attempting compression", map[string]any{
					"error": err.Error(),
					"retry": retry,
				})

				if retry == 0 && !constants.IsInternalChannel(opts.Channel) {
					al.bus.PublishOutbound(bus.OutboundMessage{
						Channel: opts.Channel,
						ChatID:  opts.ChatID,
						Content: "Context window exceeded. Compressing history and retrying...",
					})
				}

				al.forceCompression(agent, opts.SessionKey)
				newHistory := agent.Sessions.GetHistory(opts.SessionKey)
				newSummary := agent.Sessions.GetSummary(opts.SessionKey)
				messages = agent.ContextBuilder.BuildMessages(
					newHistory, newSummary, "",
					nil, opts.Channel, opts.ChatID,
				)
				continue
			}
			break
		}

		if err != nil {
			meta.SelectedModel = modelUsed
			meta.FallbackAttempts = fallbackTrace
			logger.ErrorCF("agent", "LLM call failed",
				map[string]any{
					"agent_id":  agent.ID,
					"iteration": iteration,
					"model":     modelUsed,
					"attempts":  fallbackTrace,
					"error":     err.Error(),
				})
			return "", iteration, meta, fmt.Errorf("LLM call failed after retries: %w", err)
		}

		// Check if no tool calls - we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
				map[string]any{
					"agent_id":      agent.ID,
					"iteration":     iteration,
					"model":         modelUsed,
					"attempts":      fallbackTrace,
					"content_chars": len(finalContent),
				})
			break
		}
		meta.SelectedModel = modelUsed
		meta.FallbackAttempts = fallbackTrace

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]any{
				"agent_id":  agent.ID,
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:    "assistant",
			Content: response.Content,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			// Copy ExtraContent to ensure thought_signature is persisted for Gemini 3
			extraContent := tc.ExtraContent
			thoughtSignature := ""
			if tc.Function != nil {
				thoughtSignature = tc.Function.ThoughtSignature
			}

			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Name: tc.Name,
				Function: &providers.FunctionCall{
					Name:             tc.Name,
					Arguments:        string(argumentsJSON),
					ThoughtSignature: thoughtSignature,
				},
				ExtraContent:     extraContent,
				ThoughtSignature: thoughtSignature,
			})
		}
		messages = append(messages, assistantMsg)

		// Save assistant message with tool calls to session
		agent.Sessions.AddFullMessage(opts.SessionKey, assistantMsg)

		// Execute tool calls
		for _, tc := range normalizedToolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			argsPreview := utils.Truncate(string(argsJSON), 200)
			logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", tc.Name, argsPreview),
				map[string]any{
					"agent_id":  agent.ID,
					"tool":      tc.Name,
					"iteration": iteration,
				})

			// Create async callback for tools that implement AsyncTool
			// NOTE: Async tools do NOT send results directly to users.
			// Instead, they notify the agent via PublishInbound, and the agent decides
			// whether to forward the result to the user (in processSystemMessage).
			asyncCallback := func(callbackCtx context.Context, result *tools.ToolResult) {
				// Log the async completion but don't send directly to user
				// The agent will handle user notification via processSystemMessage
				if !result.Silent && result.ForUser != "" {
					logger.InfoCF("agent", "Async tool completed, agent will handle notification",
						map[string]any{
							"tool":        tc.Name,
							"content_len": len(result.ForUser),
						})
				}
			}

			toolResult := agent.Tools.ExecuteWithContext(
				ctx,
				tc.Name,
				tc.Arguments,
				opts.Channel,
				opts.ChatID,
				asyncCallback,
			)
			retries := 0
			retryBudget := al.toolRetryBudget()
			for toolResult != nil && toolResult.IsError && retries < retryBudget && isRetriableToolError(toolResult) {
				retries++
				logger.WarnCF("agent", "Tool execution retry",
					map[string]any{
						"agent_id":     agent.ID,
						"tool":         tc.Name,
						"retry":        retries,
						"retry_budget": retryBudget,
						"error":        safeToolErrorMessage(toolResult),
					})
				abortRetry := false
				if d := al.toolRetryDelay(); d > 0 {
					select {
					case <-ctx.Done():
						abortRetry = true
					case <-time.After(d):
					}
				}
				if abortRetry {
					break
				}
				toolResult = agent.Tools.ExecuteWithContext(
					ctx,
					tc.Name,
					tc.Arguments,
					opts.Channel,
					opts.ChatID,
					asyncCallback,
				)
			}
			if toolResult == nil {
				toolResult = tools.ErrorResult("tool returned nil result").WithError(fmt.Errorf("nil tool result"))
			}

			// Send ForUser content to user immediately if not Silent
			if !toolResult.Silent && toolResult.ForUser != "" && opts.SendResponse {
				al.bus.PublishOutbound(bus.OutboundMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Content: toolResult.ForUser,
				})
				logger.DebugCF("agent", "Sent tool result to user",
					map[string]any{
						"tool":        tc.Name,
						"content_len": len(toolResult.ForUser),
					})
			}

			// Determine content for LLM based on tool result
			contentForLLM := toolResult.ForLLM
			if contentForLLM == "" && toolResult.Err != nil {
				contentForLLM = toolResult.Err.Error()
			}
			contentForLLM = ensureToolReceiptContent(tc.Name, toolResult, contentForLLM, retries)

			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolResultMsg)
			al.captureActionReceipt(opts.SessionKey, tc.Name, contentForLLM)

			// Save tool result message to session
			agent.Sessions.AddFullMessage(opts.SessionKey, toolResultMsg)
		}
	}

	return finalContent, iteration, meta, nil
}

func fallbackCandidateRefs(candidates []providers.FallbackCandidate) []string {
	if len(candidates) == 0 {
		return nil
	}
	refs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		refs = append(refs, fmt.Sprintf("%s/%s", c.Provider, c.Model))
	}
	return refs
}

func formatFallbackAttemptChain(attempts []providers.FallbackAttempt) string {
	if len(attempts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(attempts))
	for i, a := range attempts {
		status := "failed"
		if a.Skipped {
			status = "skipped"
		}
		reason := "-"
		if a.Reason != "" {
			reason = string(a.Reason)
		}
		duration := "-"
		if a.Duration > 0 {
			duration = a.Duration.Round(time.Millisecond).String()
		}
		errMsg := "-"
		if a.Error != nil {
			errMsg = strings.TrimSpace(a.Error.Error())
			if errMsg == "" {
				errMsg = "-"
			}
		}
		retryMeta := ""
		if a.RetryOrdinal > 0 || a.RetryBudget > 0 || a.RetryConsumed > 0 {
			retryMeta = fmt.Sprintf(" retry=%d budget=%d consumed=%d", a.RetryOrdinal, a.RetryBudget, a.RetryConsumed)
		}
		parts = append(parts, fmt.Sprintf(
			"#%d %s/%s status=%s reason=%s duration=%s error=%s%s",
			i+1, a.Provider, a.Model, status, reason, duration, errMsg, retryMeta,
		))
	}
	return strings.Join(parts, " | ")
}

// updateToolContexts updates the context for tools that need channel/chatID info.
func (al *AgentLoop) updateToolContexts(agent *AgentInstance, channel, chatID string) {
	// Use ContextualTool interface instead of type assertions
	if tool, ok := agent.Tools.Get("message"); ok {
		if mt, ok := tool.(tools.ContextualTool); ok {
			mt.SetContext(channel, chatID)
		}
	}
	if tool, ok := agent.Tools.Get("spawn"); ok {
		if st, ok := tool.(tools.ContextualTool); ok {
			st.SetContext(channel, chatID)
		}
	}
	if tool, ok := agent.Tools.Get("subagent"); ok {
		if st, ok := tool.(tools.ContextualTool); ok {
			st.SetContext(channel, chatID)
		}
	}
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
func (al *AgentLoop) maybeSummarize(agent *AgentInstance, sessionKey, channel, chatID string) {
	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory)
	triggerPercent := al.summaryTriggerPercent()
	threshold := agent.ContextWindow * triggerPercent / 100

	if tokenEstimate > threshold {
		summarizeKey := agent.ID + ":" + sessionKey
		if _, loading := al.summarizing.LoadOrStore(summarizeKey, true); !loading {
			go func() {
				defer al.summarizing.Delete(summarizeKey)
				al.logSummarizationEvent(summarizeKey, channel, chatID, tokenEstimate, threshold, len(newHistory))
				al.summarizeSession(agent, sessionKey)
			}()
		}
	}
}

// logSummarizationEvent logs summarization trigger with cooldown to avoid log spam.
func (al *AgentLoop) logSummarizationEvent(
	summarizeKey, channel, chatID string,
	tokenEstimate, threshold, historyLen int,
) {
	// Keep the UX quiet on user-facing channels; only log internally.
	if constants.IsInternalChannel(channel) {
		return
	}

	const cooldown = 30 * time.Minute
	now := time.Now()

	if raw, ok := al.summaryNotice.Load(summarizeKey); ok {
		if last, ok := raw.(time.Time); ok && now.Sub(last) < cooldown {
			return
		}
	}

	al.summaryNotice.Store(summarizeKey, now)
	logger.InfoCF("agent", "Session history summarized",
		map[string]any{
			"channel":        channel,
			"chat_id":        chatID,
			"history_len":    historyLen,
			"token_estimate": tokenEstimate,
			"threshold":      threshold,
		})
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest 50% of messages (keeping system prompt and last user message).
func (al *AgentLoop) forceCompression(agent *AgentInstance, sessionKey string) {
	history := agent.Sessions.GetHistory(sessionKey)
	keepLast := al.summaryRetainMessages()
	if len(history) <= keepLast {
		return
	}

	droppedCount := len(history) - keepLast
	newHistory := make([]providers.Message, keepLast)
	copy(newHistory, history[len(history)-keepLast:])

	// Update session
	agent.Sessions.SetHistory(sessionKey, newHistory)
	al.appendCompressionSummaryNote(agent, sessionKey, droppedCount)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(newHistory),
		"keep_last":    keepLast,
	})
}

// GetStartupInfo returns information about loaded tools and skills for logging.
func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(al.registry.ListAgentIDs()),
		"ids":   al.registry.ListAgentIDs(),
	}

	return info
}

// formatMessagesForLog formats messages for logging
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, msg := range messages {
		fmt.Fprintf(&sb, "  [%d] Role: %s\n", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			sb.WriteString("  ToolCalls:\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&sb, "    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					fmt.Fprintf(&sb, "      Arguments: %s\n", utils.Truncate(tc.Function.Arguments, 200))
				}
			}
		}
		if msg.Content != "" {
			content := utils.Truncate(msg.Content, 200)
			fmt.Fprintf(&sb, "  Content: %s\n", content)
		}
		if msg.ToolCallID != "" {
			fmt.Fprintf(&sb, "  ToolCallID: %s\n", msg.ToolCallID)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("]")
	return sb.String()
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, tool := range toolDefs {
		fmt.Fprintf(&sb, "  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		fmt.Fprintf(&sb, "      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			fmt.Fprintf(&sb, "      Parameters: %s\n", utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200))
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(agent *AgentInstance, sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := agent.Sessions.GetHistory(sessionKey)
	summary := agent.Sessions.GetSummary(sessionKey)
	keepLast := al.summaryRetainMessages()

	// Keep recent messages for continuity.
	if len(history) <= keepLast {
		return
	}

	toSummarize := history[:len(history)-keepLast]

	// Oversized Message Guard
	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgTokens := len(m.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	// Multi-Part Summarization
	var finalSummary string
	if len(validMessages) > 10 {
		mid := len(validMessages) / 2
		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := al.summarizeBatch(ctx, agent, part1, "")
		s2, _ := al.summarizeBatch(ctx, agent, part2, "")

		mergePrompt := fmt.Sprintf(
			"Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s",
			s1,
			s2,
		)
		resp, err := agent.Provider.Chat(
			ctx,
			[]providers.Message{{Role: "user", Content: mergePrompt}},
			nil,
			agent.Model,
			map[string]any{
				"max_tokens":  1024,
				"temperature": 0.3,
			},
		)
		if err == nil {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, agent, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, keepLast)
		agent.Sessions.Save(sessionKey)
	}
}

func (al *AgentLoop) summaryTriggerPercent() int {
	p := al.cfg.Agents.Defaults.SummaryTriggerPercent
	if p <= 0 {
		p = 75
	}
	if p < 40 {
		return 40
	}
	if p > 95 {
		return 95
	}
	return p
}

func (al *AgentLoop) summaryRetainMessages() int {
	n := al.cfg.Agents.Defaults.SummaryRetainMessages
	if n <= 0 {
		n = 6
	}
	if n < 2 {
		return 2
	}
	if n > 32 {
		return 32
	}
	return n
}

func (al *AgentLoop) appendCompressionSummaryNote(agent *AgentInstance, sessionKey string, droppedCount int) {
	if droppedCount <= 0 {
		return
	}
	const summaryMaxChars = 6000
	note := fmt.Sprintf("[Compression] Emergency compression dropped %d older messages.", droppedCount)
	current := strings.TrimSpace(agent.Sessions.GetSummary(sessionKey))
	next := note
	if current != "" {
		next = current + "\n" + note
	}
	if len(next) > summaryMaxChars {
		next = next[len(next)-summaryMaxChars:]
	}
	agent.Sessions.SetSummary(sessionKey, next)
}

func (al *AgentLoop) toolRetryBudget() int {
	b := al.cfg.Agents.Defaults.ToolRetryBudget
	if b < 0 {
		b = 0
	}
	if b > 3 {
		b = 3
	}
	return b
}

func (al *AgentLoop) toolRetryDelay() time.Duration {
	ms := al.cfg.Agents.Defaults.ToolRetryDelayMS
	if ms < 0 {
		ms = 0
	}
	if ms > 2000 {
		ms = 2000
	}
	return time.Duration(ms) * time.Millisecond
}

func isRetriableToolError(result *tools.ToolResult) bool {
	if result == nil || !result.IsError {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(safeToolErrorMessage(result)))
	if msg == "" {
		return false
	}
	hints := []string{
		"timeout",
		"timed out",
		"deadline exceeded",
		"temporar",
		"try again",
		"rate limit",
		"too many requests",
		"429",
		"connection reset",
		"connection refused",
		"network",
		"unavailable",
		"econnreset",
	}
	for _, h := range hints {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

func safeToolErrorMessage(result *tools.ToolResult) string {
	if result == nil {
		return ""
	}
	if strings.TrimSpace(result.ForLLM) != "" {
		return strings.TrimSpace(result.ForLLM)
	}
	if result.Err != nil {
		return strings.TrimSpace(result.Err.Error())
	}
	return ""
}

func ensureToolReceiptContent(toolName string, result *tools.ToolResult, content string, retries int) string {
	raw := strings.TrimSpace(content)
	if runID, status, _, _ := parseToolReceipt(raw); runID != "" || status != "" {
		return raw
	}

	runID := fmt.Sprintf("tool-%d", time.Now().UnixNano())
	status := "completed"
	if result != nil {
		if result.Async {
			status = "accepted"
		}
		if result.IsError {
			status = "failed"
		}
	}
	if raw == "" && result != nil && result.Err != nil {
		raw = strings.TrimSpace(result.Err.Error())
	}
	if raw == "" {
		raw = "-"
	}
	raw = strings.ReplaceAll(raw, "\n", " | ")

	if status == "failed" {
		errCode := classifyToolErrorCode(raw)
		return fmt.Sprintf("[tool:%s][run_id=%s] status=failed error_code=%s error=%s retries=%d",
			toolName, runID, errCode, raw, retries)
	}
	return fmt.Sprintf("[tool:%s][run_id=%s] status=%s retries=%d result=%s",
		toolName, runID, status, retries, raw)
}

func classifyToolErrorCode(msg string) string {
	lower := strings.ToLower(strings.TrimSpace(msg))
	switch {
	case lower == "":
		return "tool_error"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return "timeout"
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many requests"), strings.Contains(lower, "429"):
		return "rate_limit"
	case strings.Contains(lower, "not found"):
		return "not_found"
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"), strings.Contains(lower, "outside workspace"):
		return "permission_denied"
	case strings.Contains(lower, "invalid"), strings.Contains(lower, "required"):
		return "invalid_args"
	default:
		return "tool_error"
	}
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	var sb strings.Builder
	sb.WriteString("Provide a concise summary of this conversation segment, preserving core context and key points.\n")
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, m := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	prompt := sb.String()

	response, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: prompt}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":  1024,
			"temperature": 0.3,
		},
	)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// estimateTokens estimates the number of tokens in a message list.
// Uses a safe heuristic of 2.5 characters per token to account for CJK and other
// overheads better than the previous 3 chars/token.
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	totalChars := 0
	for _, m := range messages {
		totalChars += utf8.RuneCountInString(m.Content)
	}
	// 2.5 chars per token = totalChars * 2 / 5
	return totalChars * 2 / 5
}

func (al *AgentLoop) handleCommand(ctx context.Context, msg bus.InboundMessage) (string, bool) {
	content := strings.TrimSpace(msg.Content)
	if !strings.HasPrefix(content, "/") {
		return "", false
	}

	parts := strings.Fields(content)
	if len(parts) == 0 {
		return "", false
	}

	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "/show":
		if len(args) < 1 {
			return "Usage: /show [model|channel|agents]", true
		}
		switch args[0] {
		case "model":
			defaultAgent := al.registry.GetDefaultAgent()
			if defaultAgent == nil {
				return "No default agent configured", true
			}
			return fmt.Sprintf("Current model: %s", defaultAgent.Model), true
		case "channel":
			return fmt.Sprintf("Current channel: %s", msg.Channel), true
		case "agents":
			agentIDs := al.registry.ListAgentIDs()
			return fmt.Sprintf("Registered agents: %s", strings.Join(agentIDs, ", ")), true
		default:
			return fmt.Sprintf("Unknown show target: %s", args[0]), true
		}

	case "/list":
		if len(args) < 1 {
			return "Usage: /list [models|channels|agents]", true
		}
		switch args[0] {
		case "models":
			return "Available models: configured in config.json per agent", true
		case "channels":
			if al.channelManager == nil {
				return "Channel manager not initialized", true
			}
			channels := al.channelManager.GetEnabledChannels()
			if len(channels) == 0 {
				return "No channels enabled", true
			}
			return fmt.Sprintf("Enabled channels: %s", strings.Join(channels, ", ")), true
		case "agents":
			agentIDs := al.registry.ListAgentIDs()
			return fmt.Sprintf("Registered agents: %s", strings.Join(agentIDs, ", ")), true
		default:
			return fmt.Sprintf("Unknown list target: %s", args[0]), true
		}

	case "/switch":
		if len(args) < 3 || args[1] != "to" {
			return "Usage: /switch [model|channel] to <name>", true
		}
		target := args[0]
		value := args[2]

		switch target {
		case "model":
			defaultAgent := al.registry.GetDefaultAgent()
			if defaultAgent == nil {
				return "No default agent configured", true
			}
			oldModel := defaultAgent.Model
			defaultAgent.Model = value
			return fmt.Sprintf("Switched model from %s to %s", oldModel, value), true
		case "channel":
			if al.channelManager == nil {
				return "Channel manager not initialized", true
			}
			if _, exists := al.channelManager.GetChannel(value); !exists && value != "cli" {
				return fmt.Sprintf("Channel '%s' not found or not enabled", value), true
			}
			return fmt.Sprintf("Switched target channel to %s", value), true
		default:
			return fmt.Sprintf("Unknown switch target: %s", target), true
		}
	case "/thinking":
		defaultAgent := al.registry.GetDefaultAgent()
		if defaultAgent == nil {
			return "No default agent configured", true
		}
		sessionKey := strings.TrimSpace(msg.SessionKey)
		if sessionKey == "" {
			sessionKey = "cli:default"
		}
		if len(args) == 0 {
			current := strings.TrimSpace(defaultAgent.Sessions.GetThinkingLevel(sessionKey))
			if current == "" {
				current = "off"
			}
			return fmt.Sprintf("Thinking level (%s): %s", sessionKey, current), true
		}
		level := normalizeThinkingLevel(args[0])
		if level == "" {
			return "Usage: /thinking [off|minimal|low|medium|high]", true
		}
		defaultAgent.Sessions.SetThinkingLevel(sessionKey, level)
		_ = defaultAgent.Sessions.Save(sessionKey)
		return fmt.Sprintf("Thinking level set (%s): %s", sessionKey, level), true
	case "/verbose":
		defaultAgent := al.registry.GetDefaultAgent()
		if defaultAgent == nil {
			return "No default agent configured", true
		}
		sessionKey := strings.TrimSpace(msg.SessionKey)
		if sessionKey == "" {
			sessionKey = "cli:default"
		}
		if len(args) == 0 {
			current := strings.TrimSpace(defaultAgent.Sessions.GetVerboseLevel(sessionKey))
			if current == "" {
				current = "off"
			}
			return fmt.Sprintf("Verbose level (%s): %s", sessionKey, current), true
		}
		level := normalizeVerboseLevel(args[0])
		if level == "" {
			return "Usage: /verbose [off|on|full]", true
		}
		defaultAgent.Sessions.SetVerboseLevel(sessionKey, level)
		_ = defaultAgent.Sessions.Save(sessionKey)
		return fmt.Sprintf("Verbose level set (%s): %s", sessionKey, level), true
	case "/runs":
		limit := 20
		if len(args) > 0 {
			var parsed int
			if _, err := fmt.Sscanf(args[0], "%d", &parsed); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		svc := runs.NewService(al.cfg.WorkspacePath())
		entries, err := svc.List(limit)
		if err != nil {
			return fmt.Sprintf("runs query failed: %v", err), true
		}
		if len(entries) == 0 {
			return "No runs found.", true
		}
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, fmt.Sprintf("Recent runs (%d):", len(entries)))
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf(
				"- run_id=%s job_id=%s status=%s ts=%d duration=%dms source=%s error=%s",
				e.RunID, safeQueryValue(e.JobID), e.Status, e.TS, e.DurationMS, safeQueryValue(e.Source), safeQueryValue(runErrorMessage(e)),
			))
		}
		return strings.Join(lines, "\n"), true
	case "/status":
		if len(args) < 1 {
			return "Usage: /status <run_id>", true
		}
		svc := runs.NewService(al.cfg.WorkspacePath())
		entry, found, err := svc.Get(args[0])
		if err != nil {
			return fmt.Sprintf("status query failed: %v", err), true
		}
		if !found || entry == nil {
			return fmt.Sprintf("Run not found: %s", args[0]), true
		}
		return fmt.Sprintf(
			"run_id=%s\njob_id=%s\nstatus=%s\nts=%d\nduration=%dms\nsource=%s\nerror_code=%s\nerror=%s",
			entry.RunID,
			safeQueryValue(entry.JobID),
			entry.Status,
			entry.TS,
			entry.DurationMS,
			safeQueryValue(entry.Source),
			safeQueryValue(entry.ErrorCode),
			safeQueryValue(runErrorMessage(*entry)),
		), true
	case "/errors":
		limit := 20
		if len(args) > 0 {
			var parsed int
			if _, err := fmt.Sscanf(args[0], "%d", &parsed); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		svc := runs.NewService(al.cfg.WorkspacePath())
		entries, err := svc.Errors(limit)
		if err != nil {
			return fmt.Sprintf("errors query failed: %v", err), true
		}
		if len(entries) == 0 {
			return "No failed runs found.", true
		}
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, fmt.Sprintf("Recent failed runs (%d):", len(entries)))
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf(
				"- run_id=%s job_id=%s status=%s ts=%d duration=%dms source=%s error_code=%s error=%s",
				e.RunID,
				safeQueryValue(e.JobID),
				e.Status,
				e.TS,
				e.DurationMS,
				safeQueryValue(e.Source),
				safeQueryValue(e.ErrorCode),
				safeQueryValue(runErrorMessage(e)),
			))
		}
		return strings.Join(lines, "\n"), true
	}

	return "", false
}

func safeQueryValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func normalizeThinkingLevel(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "off":
		return ""
	case "minimal", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func normalizeVerboseLevel(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "off":
		return ""
	case "on", "full":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func runErrorMessage(e runs.Entry) string {
	if strings.TrimSpace(e.ErrorMessage) != "" {
		return e.ErrorMessage
	}
	return e.Error
}

var runIDPattern = regexp.MustCompile(`run_id[=:]\s*([a-zA-Z0-9._:-]+)`)

func (al *AgentLoop) applyExecutionResponseProtocol(sessionKey, userMessage, content string) string {
	user := strings.TrimSpace(userMessage)
	out := strings.TrimSpace(content)
	if out == "" {
		return content
	}
	if strings.HasPrefix(user, "[System:") {
		return content
	}
	if !isExecutionStatusQuestion(user) {
		return content
	}
	if strings.Contains(out, "结论：") && strings.Contains(out, "证据：") && strings.Contains(out, "下一步：") {
		return content
	}

	evidenceFound := hasExecutionEvidence(out)
	latest, hasLatest := al.latestActionReceipt(sessionKey)
	conclusion := "未确认完成，正在等待回执。"
	if evidenceFound {
		lower := strings.ToLower(out)
		switch {
		case strings.Contains(lower, "status=failed"),
			strings.Contains(lower, "status=error"),
			strings.Contains(lower, " run failed"),
			strings.Contains(lower, "执行失败"),
			strings.Contains(lower, "已确认失败"):
			conclusion = "已确认失败。"
		case strings.Contains(lower, "status=completed"),
			strings.Contains(lower, "status=ok"),
			strings.Contains(lower, "已执行完成"),
			strings.Contains(lower, "已确认完成"):
			conclusion = "已确认完成。"
		default:
			conclusion = "已返回执行状态信息。"
		}
	}
	if !evidenceFound && hasLatest {
		evidenceFound = true
		switch strings.ToLower(latest.Status) {
		case "completed", "ok":
			conclusion = "已确认完成。"
		case "failed", "error":
			conclusion = "已确认失败。"
		case "accepted", "running":
			conclusion = "未确认完成，正在等待回执。"
		}
	}

	evidence := "未找到可验证回执（run_id/状态记录）。"
	if evidenceFound {
		evidence = buildEvidenceSummary(out)
		if hasLatest {
			evidence = fmt.Sprintf("%s | recent_run_id=%s status=%s error_code=%s error=%s",
				evidence,
				safeQueryValue(latest.RunID),
				safeQueryValue(latest.Status),
				safeQueryValue(latest.ErrorCode),
				safeQueryValue(latest.Error),
			)
		}
	}

	next := "继续等待回执，或使用 /runs 和 /status <run_id> 查询最新状态。"
	switch conclusion {
	case "已确认完成。":
		next = "如需复核，使用 /status <run_id> 或 kingclaw runs status <run_id>。"
	case "已确认失败。":
		next = "可用 /errors 或 kingclaw runs errors 查看失败原因后重试。"
	}

	return fmt.Sprintf("结论：%s\n证据：%s\n下一步：%s", conclusion, evidence, next)
}

func (al *AgentLoop) captureActionReceipt(sessionKey, toolName, content string) {
	runID, status, errCode, errMsg := parseToolReceipt(content)
	if runID == "" && status == "" {
		return
	}
	rcpt := actionReceipt{
		RunID:     runID,
		Tool:      toolName,
		Status:    status,
		ErrorCode: errCode,
		Error:     errMsg,
		TS:        time.Now().UnixMilli(),
	}
	v, _ := al.recentActions.LoadOrStore(sessionKey, []actionReceipt{})
	existing, _ := v.([]actionReceipt)
	existing = append(existing, rcpt)
	if len(existing) > 20 {
		existing = existing[len(existing)-20:]
	}
	al.recentActions.Store(sessionKey, existing)
}

func (al *AgentLoop) latestActionReceipt(sessionKey string) (actionReceipt, bool) {
	v, ok := al.recentActions.Load(sessionKey)
	if !ok {
		return actionReceipt{}, false
	}
	list, ok := v.([]actionReceipt)
	if !ok || len(list) == 0 {
		return actionReceipt{}, false
	}
	return list[len(list)-1], true
}

func (al *AgentLoop) injectActionContext(sessionKey, userMessage string) string {
	if !isExecutionStatusQuestion(userMessage) {
		return userMessage
	}
	latest, ok := al.latestActionReceipt(sessionKey)
	if !ok {
		return userMessage
	}
	note := fmt.Sprintf(
		"[RecentAction]\nrun_id=%s\ntool=%s\nstatus=%s\nerror_code=%s\nerror=%s\n\n",
		safeQueryValue(latest.RunID),
		safeQueryValue(latest.Tool),
		safeQueryValue(latest.Status),
		safeQueryValue(latest.ErrorCode),
		safeQueryValue(latest.Error),
	)
	return note + userMessage
}

func parseToolReceipt(content string) (runID, status, errorCode, errMsg string) {
	if m := runIDPattern.FindStringSubmatch(content); len(m) > 1 {
		runID = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`status[=:]\s*([a-zA-Z0-9_-]+)`).FindStringSubmatch(content); len(m) > 1 {
		status = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`error_code[=:]\s*([a-zA-Z0-9_.-]+)`).FindStringSubmatch(content); len(m) > 1 {
		errorCode = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`error[=:]\s*([^\n]+)`).FindStringSubmatch(content); len(m) > 1 {
		errMsg = strings.TrimSpace(m[1])
	}
	return
}

func isExecutionStatusQuestion(user string) bool {
	lower := strings.ToLower(strings.TrimSpace(user))
	keywords := []string{
		"执行了吗",
		"完成了吗",
		"是否完成",
		"成功了吗",
		"失败了吗",
		"任务状态",
		"运行状态",
		"回执",
		"run_id",
		"/status",
		"/runs",
		"/errors",
	}
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func hasExecutionEvidence(content string) bool {
	lower := strings.ToLower(content)
	return runIDPattern.MatchString(content) ||
		strings.Contains(lower, "status=") ||
		strings.Contains(lower, "recent runs") ||
		strings.Contains(lower, "run not found") ||
		strings.Contains(lower, "no runs found") ||
		strings.Contains(lower, "error_code=")
}

func buildEvidenceSummary(content string) string {
	lines := strings.Split(content, "\n")
	collected := make([]string, 0, 3)
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if runIDPattern.MatchString(line) ||
			strings.Contains(lower, "status=") ||
			strings.Contains(lower, "error_code=") ||
			strings.Contains(lower, "ts=") ||
			strings.Contains(lower, "duration=") {
			collected = append(collected, line)
		}
		if len(collected) >= 3 {
			break
		}
	}
	if len(collected) == 0 {
		if len(content) > 180 {
			return strings.TrimSpace(content[:180]) + "..."
		}
		return strings.TrimSpace(content)
	}
	return strings.Join(collected, " | ")
}

func enforceInferenceLabel(content string) string {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "推断") || strings.Contains(lower, "待确认") {
		return content
	}
	uncertainHints := []string{
		"可能", "也许", "不确定", "猜测", "大概",
		"might", "maybe", "likely", "uncertain", "not sure",
	}
	for _, hint := range uncertainHints {
		if strings.Contains(lower, hint) {
			return strings.TrimSpace(content) + "\n\n注：以上包含推断，待确认。"
		}
	}
	return content
}

// extractPeer extracts the routing peer from inbound message metadata.
func extractPeer(msg bus.InboundMessage) *routing.RoutePeer {
	peerKind := msg.Metadata["peer_kind"]
	if peerKind == "" {
		return nil
	}
	peerID := msg.Metadata["peer_id"]
	if peerID == "" {
		if peerKind == "direct" {
			peerID = msg.SenderID
		} else {
			peerID = msg.ChatID
		}
	}
	return &routing.RoutePeer{Kind: peerKind, ID: peerID}
}

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := msg.Metadata["parent_peer_kind"]
	parentID := msg.Metadata["parent_peer_id"]
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
}
