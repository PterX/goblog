package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/kataras/iris/v12"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"

	"kandaoni.com/anqicms/model"
	"kandaoni.com/anqicms/pkg/ai/eino"
	"kandaoni.com/anqicms/pkg/mcp/server"
	"kandaoni.com/anqicms/provider"
)

// ChatSession represents a user chat session
type ChatSession struct {
	ID        string        `json:"id"`
	Messages  []ChatMessage `json:"messages"`
	CreatedAt time.Time     `json:"created_at"`
}

// ChatMessage represents a message in a conversation
type ChatMessage struct {
	Role        string `json:"role"`
	Content     string `json:"content"`
	CreatedTime int64  `json:"created_time"`
}

// AiChatService manages AI chat conversations
type AiChatService struct {
	sessions map[string]*ChatSession
	mu       sync.RWMutex
	logger   *slog.Logger
	mcpSrv   *server.Server
	db       *gorm.DB
	site     *provider.Website

	// Cached tool definitions and handlers (initialized once)
	tools    []*schema.ToolInfo
	handlers map[string]toolHandler
}

// NewAiChatService creates a new AI chat service
func NewAiChatService(mcpSrv *server.Server) *AiChatService {
	// Get first available site for tool operations
	var site *provider.Website
	sites := provider.GetWebsites()
	if len(sites) > 0 {
		site = sites[0]
	}

	svc := &AiChatService{
		sessions: make(map[string]*ChatSession),
		logger:   slog.Default(),
		mcpSrv:   mcpSrv,
		db:       provider.GetDefaultDB(),
		site:     site,
	}

	// Initialize tools
	svc.tools, svc.handlers = svc.getEinoTools()
	svc.logger.Info("AI tools initialized", "count", len(svc.tools))
	// Load sessions from database on startup
	svc.loadSessionsFromDB()
	return svc
}

// loadSessionsFromDB loads all existing sessions from the database into memory
func (svc *AiChatService) loadSessionsFromDB() {
	if svc.db == nil {
		return
	}
	// Get distinct session IDs, ordered by first message time
	type sessionRow struct {
		SessionId   string
		CreatedTime int64
	}
	var rows []sessionRow
	svc.db.Model(&model.AiChatMessage{}).
		Select("session_id, MIN(created_time) as created_time").
		Group("session_id").
		Order("MIN(created_time) ASC").
		Scan(&rows)
	if len(rows) == 0 {
		return
	}
	for _, row := range rows {
		sess := &ChatSession{
			ID:        row.SessionId,
			Messages:  make([]ChatMessage, 0),
			CreatedAt: time.Unix(row.CreatedTime, 0),
		}
		// Load messages for this session
		var dbMessages []model.AiChatMessage
		svc.db.Model(&model.AiChatMessage{}).
			Where("session_id = ?", row.SessionId).
			Order("created_time ASC").
			Find(&dbMessages)
		for _, dbm := range dbMessages {
			sess.Messages = append(sess.Messages, ChatMessage{
				Role:        dbm.Role,
				Content:     dbm.Content,
				CreatedTime: dbm.CreatedTime,
			})
		}
		svc.sessions[row.SessionId] = sess
	}
	svc.logger.Info("Loaded AI chat sessions from DB", "count", len(rows))
}

// GetOrCreateSession gets or creates a chat session
func (svc *AiChatService) GetOrCreateSession(sessionID string) *ChatSession {
	svc.mu.Lock()
	defer svc.mu.Unlock()

	if sess, exists := svc.sessions[sessionID]; exists {
		return sess
	}

	// Try loading from DB first
	if svc.db != nil {
		var dbMessages []model.AiChatMessage
		svc.db.Model(&model.AiChatMessage{}).
			Where("session_id = ?", sessionID).
			Order("created_time ASC").
			Find(&dbMessages)
		if len(dbMessages) > 0 {
			sess := &ChatSession{
				ID:        sessionID,
				Messages:  make([]ChatMessage, 0, len(dbMessages)),
				CreatedAt: time.Unix(dbMessages[0].CreatedTime, 0),
			}
			for _, dbm := range dbMessages {
				sess.Messages = append(sess.Messages, ChatMessage{
					Role:        dbm.Role,
					Content:     dbm.Content,
					CreatedTime: dbm.CreatedTime,
				})
			}
			svc.sessions[sessionID] = sess
			return sess
		}
	}

	sess := &ChatSession{
		ID:        sessionID,
		Messages:  make([]ChatMessage, 0),
		CreatedAt: time.Now(),
	}
	svc.sessions[sessionID] = sess
	return sess
}

// AddMessage adds a message to a session
func (svc *AiChatService) AddMessage(sessionID string, msg ChatMessage) {
	svc.mu.Lock()

	sess, exists := svc.sessions[sessionID]
	if !exists {
		sess = &ChatSession{
			ID:        sessionID,
			Messages:  make([]ChatMessage, 0),
			CreatedAt: time.Now(),
		}
		svc.sessions[sessionID] = sess
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:        msg.Role,
		Content:     msg.Content,
		CreatedTime: time.Now().Unix(),
	})
	svc.mu.Unlock()

	// Persist to database asynchronously
	if svc.db != nil {
		go func() {
			dbMsg := &model.AiChatMessage{
				SessionId: sessionID,
				Role:      msg.Role,
				Content:   msg.Content,
			}
			if err := svc.db.Create(dbMsg).Error; err != nil {
				svc.logger.Error("Failed to persist chat message", "error", err)
			}
		}()
	}
}

// GetMessages returns messages from a session
func (svc *AiChatService) GetMessages(sessionID string) []ChatMessage {
	svc.mu.RLock()
	sess, exists := svc.sessions[sessionID]
	svc.mu.RUnlock()

	if !exists {
		// Try loading from DB
		svc.mu.Lock()
		// Double-check after acquiring write lock
		if sess, exists = svc.sessions[sessionID]; !exists && svc.db != nil {
			var dbMessages []model.AiChatMessage
			svc.db.Model(&model.AiChatMessage{}).
				Where("session_id = ?", sessionID).
				Order("created_time ASC").
				Find(&dbMessages)
			if len(dbMessages) > 0 {
				sess = &ChatSession{
					ID:        sessionID,
					Messages:  make([]ChatMessage, 0, len(dbMessages)),
					CreatedAt: time.Unix(dbMessages[0].CreatedTime, 0),
				}
				for _, dbm := range dbMessages {
					sess.Messages = append(sess.Messages, ChatMessage{
						Role:    dbm.Role,
						Content: dbm.Content,
					})
				}
				svc.sessions[sessionID] = sess
			}
		}
		svc.mu.Unlock()
		if sess == nil {
			return nil
		}
	}

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	result := make([]ChatMessage, len(sess.Messages))
	copy(result, sess.Messages)
	return result
}

// ListSessions returns a list of all sessions with summary info
func (svc *AiChatService) ListSessions() []iris.Map {
	svc.mu.RLock()
	defer svc.mu.RUnlock()

	var result []iris.Map
	for id, sess := range svc.sessions {
		lastMsg := ""
		if len(sess.Messages) > 0 {
			lastMsg = sess.Messages[len(sess.Messages)-1].Content
			if len([]rune(lastMsg)) > 100 {
				lastMsg = string([]rune(lastMsg)[:100]) + "..."
			}
		}
		result = append(result, iris.Map{
			"session_id":   id,
			"created_at":   sess.CreatedAt.Unix(),
			"updated_at":   sess.Messages[len(sess.Messages)-1].CreatedTime,
			"msg_count":    len(sess.Messages),
			"last_message": lastMsg,
		})
	}
	// Sort by updated_at descending (most recent first)
	// Convert to slice of iris.Map and sort
	// Actually, just return unsorted for now — the frontend can sort
	return result
}

// CloseSession closes and removes a session
func (svc *AiChatService) CloseSession(sessionID string) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	delete(svc.sessions, sessionID)
}

// AiChatController handles AI chat requests
type AiChatController struct {
	service *AiChatService
}

// NewAiChatController creates a new AI chat controller
func NewAiChatController(service *AiChatService) *AiChatController {
	return &AiChatController{service: service}
}

// ChatRequest represents an AI chat request
type ChatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// ChatResponse represents an AI chat response
type ChatResponse struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

// Chat handles the chat request and returns SSE stream with AI response
func (ctrl *AiChatController) Chat(ctx iris.Context) {
	var req ChatRequest
	if err := ctx.ReadJSON(&req); err != nil {
		ctx.JSON(iris.Map{
			"code": -1,
			"msg":  "invalid request",
		})
		return
	}

	if req.Message == "" {
		ctx.JSON(iris.Map{
			"code": -1,
			"msg":  "message cannot be empty",
		})
		return
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().UnixNano())
	}

	// Add user message to history
	ctrl.service.AddMessage(sessionID, ChatMessage{
		Role:    "user",
		Content: req.Message,
	})

	// Set SSE headers
	ctx.ContentType("text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Connection", "keep-alive")

	// Send session ID first
	fmt.Fprintf(ctx.ResponseWriter(), "event: session\ndata: %s\n\n", sessionID)
	ctx.ResponseWriter().Flush()

	// Send user confirmation
	// userData, _ := json.Marshal(iris.Map{
	// 	"session_id": sessionID,
	// 	"message":    ChatMessage{Role: "user", Content: req.Message},
	// 	"timestamp":  time.Now().Unix(),
	// })
	// fmt.Fprintf(ctx.ResponseWriter(), "event: message\ndata: %s\n\n", string(userData))
	// ctx.ResponseWriter().Flush()

	writer := ctx.ResponseWriter()
	requestCtx := ctx.Request().Context()

	// Generate AI response — try DeepSeek first, fall back to keyword matching
	response, err := ctrl.generateAIResponse(requestCtx, ctx, sessionID, req.Message, writer)
	if err != nil {
		slog.Error("AI response generation failed", "error", err)
		// Fallback: use keyword-based response
		var toolNames []string
		if ctrl.service.mcpSrv != nil {
			allTools := ctrl.service.getAllTools()
			for _, tool := range allTools {
				toolNames = append(toolNames, fmt.Sprintf("- %s: %s", tool.Name, tool.Description))
			}
		}
		currentSite := provider.CurrentSite(ctx)
		response = buildAIResponse(req.Message, toolNames, currentSite)
	}

	// Add assistant message to history
	ctrl.service.AddMessage(sessionID, ChatMessage{
		Role:    "assistant",
		Content: response,
	})

	// Send final end event
	if writer != nil {
		fmt.Fprintf(writer, "event: end\ndata: [DONE]\n\n")
		writer.Flush()
	}
}

// generateAIResponse calls the DeepSeek API via Eino with tool support and streams the response back via SSE
func (ctrl *AiChatController) generateAIResponse(ctx context.Context, irisCtx iris.Context, sessionID string, userMessage string, writer io.Writer) (string, error) {
	// Try to get the Eino client
	client, err := eino.GetClient()
	if err != nil {
		return "", fmt.Errorf("AI client not available: %w", err)
	}

	// Build system prompt
	systemPrompt := `你是一个专业的 AnQiCMS 网站内容管理 AI 助手，帮助用户管理文章、分类、标签和附件。

你可以使用以下工具完成用户请求：
- archive_create / archive_list / archive_get / archive_delete / archive_publish: 文章管理
- category_create / category_list / category_get / category_delete: 分类管理
- tag_create / tag_list / tag_get / tag_delete: 标签管理

在创建文章时，请先查看可用分类（使用 category_list 工具），然后选择合适的分类ID。
请用中文回复，保持专业、友好的语气。

用户的操作都会在当前站点中执行，请根据实际情况使用工具。`

	site := provider.CurrentSite(irisCtx)
	if site != nil {
		systemPrompt += fmt.Sprintf("\n\n当前站点：%s", site.Name)
	}

	// Build messages array: system + history + current user message
	messages := ctrl.buildToolMessages(sessionID, systemPrompt)

	// Bind tools to the client
	if len(ctrl.service.tools) > 0 {
		if err := client.BindTools(ctrl.service.tools); err != nil {
			return "", fmt.Errorf("failed to bind tools: %w", err)
		}
	}

	// Tool-calling loop (max 10 rounds to prevent infinite loops)
	maxRounds := 10
	var finalResponse string
	var round int

	for round = 0; round < maxRounds; round++ {
		// Stream from AI
		stream, err := client.Stream(ctx, messages)
		if err != nil {
			return "", fmt.Errorf("AI stream generate failed: %w", err)
		}

		var fullResponse strings.Builder
		var fullReasoning strings.Builder
		var roundToolCalls []schema.ToolCall

		for {
			msg, err := stream.Recv()
			if err != nil {
				break
			}

			chunk := msg.Content
			if chunk != "" {
				fullResponse.WriteString(chunk)
				// Send each chunk as SSE
				chunkData, _ := json.Marshal(iris.Map{
					"v":         chunk,
					"timestamp": time.Now().Unix(),
				})
				if writer != nil {
					fmt.Fprintf(writer, "event: message\ndata: %s\n\n", string(chunkData))
					if f, ok := writer.(interface{ Flush() error }); ok {
						f.Flush()
					}
				}
			}

			// Handle reasoning content
			if msg.ReasoningContent != "" {
				fullReasoning.WriteString(msg.ReasoningContent)
				reasoningData, _ := json.Marshal(iris.Map{
					"v":         msg.ReasoningContent,
					"timestamp": time.Now().Unix(),
				})
				if writer != nil {
					fmt.Fprintf(writer, "event: reasoning\ndata: %s\n\n", string(reasoningData))
					if f, ok := writer.(interface{ Flush() error }); ok {
						f.Flush()
					}
				}
			}

			// Collect tool calls from the message
			if len(msg.ToolCalls) > 0 {
				roundToolCalls = msg.ToolCalls
			}
		}

		stream.Close()

		// Check if the model wants to call tools
		if len(roundToolCalls) == 0 {
			// No tool calls — this is the final text response
			finalResponse = fullResponse.String()
			break
		}

		// ---- Execute tool calls ----
		// Add the assistant's message (with tool calls) to the history
		assistantMsg := schema.AssistantMessage(fullResponse.String(), roundToolCalls)
		messages = append(messages, assistantMsg)

		// Execute each tool
		for _, tc := range roundToolCalls {
			toolName := tc.Function.Name
			argsJSON := tc.Function.Arguments

			ctrl.service.logger.Info("Executing tool",
				"name", toolName,
				"args", argsJSON,
				"round", round)

			// Send tool_call event to client
			toolCallData, _ := json.Marshal(iris.Map{
				"name":      toolName,
				"arguments": argsJSON,
				"tool_call_id": tc.ID,
			})
			if writer != nil {
				fmt.Fprintf(writer, "event: tool_call\ndata: %s\n\n", string(toolCallData))
				if f, ok := writer.(interface{ Flush() error }); ok {
					f.Flush()
				}
			}

			// Execute the handler
			handler, exists := ctrl.service.handlers[toolName]
			var result string
			if !exists {
				result = fmt.Sprintf("错误：未知工具 %s", toolName)
			} else {
				result, err = handler(ctx, argsJSON)
				if err != nil {
					result = fmt.Sprintf("工具执行错误: %s", err.Error())
				}
			}

			ctrl.service.logger.Info("Tool result",
				"name", toolName,
				"result", result)

			// Send tool_result event to client
			toolResultData, _ := json.Marshal(iris.Map{
				"name":         toolName,
				"tool_call_id": tc.ID,
				"result":       result,
			})
			if writer != nil {
				fmt.Fprintf(writer, "event: tool_result\ndata: %s\n\n", string(toolResultData))
				if f, ok := writer.(interface{ Flush() error }); ok {
					f.Flush()
				}
			}

			// Add tool result message to the conversation
			toolMsg := schema.ToolMessage(result, tc.ID)
			messages = append(messages, toolMsg)
		}

		// Continue the loop to get the next AI response with tool results
	}

	if round == maxRounds && finalResponse == "" {
		return "", fmt.Errorf("工具调用次数超过上限")
	}

	return finalResponse, nil
}

// buildToolMessages builds the message array for tool-calling conversations
func (ctrl *AiChatController) buildToolMessages(sessionID string, systemPrompt string) []*schema.Message {
	var messages []*schema.Message
	messages = append(messages, schema.SystemMessage(systemPrompt))

	// Add conversation history (limit to last 10 messages)
	sess := ctrl.service.GetOrCreateSession(sessionID)
	history := sess.Messages
	if len(history) > 10 {
		history = history[len(history)-10:]
	}
	for _, msg := range history {
		if msg.Role == "user" {
			messages = append(messages, schema.UserMessage(msg.Content))
		} else if msg.Role == "assistant" {
			messages = append(messages, schema.AssistantMessage(msg.Content, nil))
		}
	}

	return messages
}

// GetHistory returns chat history
func (ctrl *AiChatController) GetHistory(ctx iris.Context) {
	sessionID := ctx.URLParamDefault("session_id", "")
	if sessionID == "" {
		ctx.JSON(iris.Map{
			"code": -1,
			"msg":  "session_id is required",
		})
		return
	}

	messages := ctrl.service.GetMessages(sessionID)
	ctx.JSON(iris.Map{
		"code": 0,
		"msg":  "success",
		"data": messages,
	})
}

// Health returns health status
func (ctrl *AiChatController) Health(ctx iris.Context) {
	ctx.JSON(iris.Map{
		"code": 0,
		"msg":  "ok",
		"data": iris.Map{
			"service": "anqicms-ai-chat",
			"status":  "running",
		},
	})
}

// getAllTools returns all available MCP tools, built from the Eino tool definitions.
func (svc *AiChatService) getAllTools() []*mcp.Tool {
	toolInfos, _ := svc.getEinoTools()
	tools := make([]*mcp.Tool, 0, len(toolInfos))
	for _, ti := range toolInfos {
		tools = append(tools, &mcp.Tool{
			Name:        ti.Name,
			Description: ti.Desc,
		})
	}
	return tools
}

// buildAIResponse builds an AI response based on the user message
func buildAIResponse(message string, toolNames []string, currentSite *provider.Website) string {
	msg := strings.ToLower(message)

	// 简单的基于关键词的路由规则
	// 生产环境中会使用大语言模型（LLM）

	if containsAny(msg, []string{"list", "list_", "get_", "search"}) {
		if containsAny(msg, []string{"article", "archive", "post"}) {
			return "要查看文章列表，请使用 `archive_list` 工具。该工具支持分页、分类筛选和关键词搜索。\n\n可用工具：\n" + formatTools(toolNames)
		}
		if containsAny(msg, []string{"category", "categor"}) {
			return "要查看分类列表，请使用 `category_list` 工具。\n\n可用工具：\n" + formatTools(toolNames)
		}
		if containsAny(msg, []string{"tag", "tags"}) {
			return "要查看标签列表，请使用 `tag_list` 工具。\n\n可用工具：\n" + formatTools(toolNames)
		}
	}

	if containsAny(msg, []string{"create", "new", "add"}) {
		if containsAny(msg, []string{"article", "archive", "post"}) {
			return "要创建文章，请使用 `archive_create` 工具。必填字段：title（标题）、content（内容）、category_id（分类ID）。\n\n可用工具：\n" + formatTools(toolNames)
		}
		if containsAny(msg, []string{"category", "categor"}) {
			return "要创建分类，请使用 `category_create` 工具。\n\n可用工具：\n" + formatTools(toolNames)
		}
		if containsAny(msg, []string{"tag", "tags"}) {
			return "要创建标签，请使用 `tag_create` 工具。必填字段：title（标题）。\n\n可用工具：\n" + formatTools(toolNames)
		}
	}

	if containsAny(msg, []string{"update", "edit", "modify"}) {
		return "要更新资源，请使用对应的 `*_update` 工具（archive_update、category_update、tag_update）。每个工具都需要传入资源的 ID。\n\n可用工具：\n" + formatTools(toolNames)
	}

	if containsAny(msg, []string{"delete", "remove"}) {
		return "要删除资源，请使用对应的 `*_delete` 工具（archive_delete、category_delete、tag_delete）。每个工具都需要传入资源的 ID。\n\n可用工具：\n" + formatTools(toolNames)
	}

	if containsAny(msg, []string{"publish"}) {
		return "要发布文章，请使用 `archive_publish` 工具，传入 archive_id 和 status（1=发布，2=取消发布）。\n\n可用工具：\n" + formatTools(toolNames)
	}

	if containsAny(msg, []string{"attachment", "upload", "file"}) {
		return "要管理附件，请使用 `attachment_*` 系列工具（attachment_list、attachment_upload、attachment_delete）。\n\n可用工具：\n" + formatTools(toolNames)
	}

	if containsAny(msg, []string{"help", "tool", "capability"}) {
		response := "可用的 AnqiCMS MCP 工具：\n\n"
		if len(toolNames) > 0 {
			response += formatTools(toolNames)
		} else {
			response += "- archive_list: 查看文章列表（支持分页和筛选）\n"
			response += "- archive_get: 获取文章详情\n"
			response += "- archive_create: 创建新文章\n"
			response += "- archive_update: 更新文章\n"
			response += "- archive_delete: 删除文章\n"
			response += "- archive_publish: 发布或取消发布文章\n"
			response += "- category_list: 查看分类列表\n"
			response += "- category_get: 获取分类详情\n"
			response += "- category_create: 创建分类\n"
			response += "- category_update: 更新分类\n"
			response += "- category_delete: 删除分类\n"
			response += "- tag_list: 查看标签列表\n"
			response += "- tag_get: 获取标签详情\n"
			response += "- tag_create: 创建标签\n"
			response += "- tag_update: 更新标签\n"
			response += "- tag_delete: 删除标签\n"
			response += "- attachment_list: 查看附件列表\n"
			response += "- attachment_upload: 上传附件\n"
		}
		return response
	}

	// 默认回复
	response := "你好！我是您的 AnqiCMS AI 助手，可以帮助您管理文章、分类、标签和附件。\n\n"
	if currentSite != nil {
		response += fmt.Sprintf("当前站点：%s\n", currentSite.Name)
	}
	response += "输入 'help' 查看可用的工具和命令。\n\n可用工具：\n" + formatTools(toolNames)
	return response
}

// Helper functions

func formatTools(tools []string) string {
	result := ""
	for _, tool := range tools {
		result += tool + "\n"
	}
	return result
}

func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// Ensure required imports are available
var _ = io.EOF
