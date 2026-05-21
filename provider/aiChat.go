package provider

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/kataras/iris/v12"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"
	"kandaoni.com/anqicms/model"
	"kandaoni.com/anqicms/pkg/mcp/server"
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
	Logger   *slog.Logger
	mcpSrv   *server.Server
	db       *gorm.DB
	site     *Website

	// Cached tool definitions and handlers (initialized once)
	Tools    []*schema.ToolInfo
	Handlers map[string]toolHandler
}

// NewAiChatService creates a new AI chat service
func (w *Website) NewAiChatService() *AiChatService {
	if mcpServer == nil {
		log.Println("mcp Server is nil")
		return nil
	}
	svc := &AiChatService{
		mu:       sync.RWMutex{},
		sessions: make(map[string]*ChatSession),
		Logger:   slog.Default(),
		mcpSrv:   mcpServer,
		db:       w.DB,
		site:     w,
	}

	// Initialize tools
	svc.Tools, svc.Handlers = svc.getEinoTools()
	// Load built-in tools (file, shell, web, code intelligence)
	builtinTools, builtinHandlers := svc.getBuiltinEinoTools()
	svc.Tools = append(svc.Tools, builtinTools...)
	for name, handler := range builtinHandlers {
		svc.Handlers[name] = handler
	}
	svc.Logger.Info("AI tools initialized", "count", len(svc.Tools))
	// Load sessions from database on startup
	svc.loadSessionsFromDB()
	w.AiSrv = svc
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
	svc.Logger.Info("Loaded AI chat sessions from DB", "count", len(rows))
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
				svc.Logger.Error("Failed to persist chat message", "error", err)
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

// GetAllTools returns all available MCP tools, built from the Eino tool definitions.
func (svc *AiChatService) GetAllTools() []*mcp.Tool {
	toolInfos, _ := svc.getEinoTools()
	// Also include built-in tools
	builtinInfos, _ := svc.getBuiltinEinoTools()
	toolInfos = append(toolInfos, builtinInfos...)
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
func (svc *AiChatService) BuildAIResponse(message string, toolNames []string) string {
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
		if containsAny(msg, []string{"page"}) {
			return "要查看单页面列表，请使用 `page_list` 工具。\n\n可用工具：\n" + formatTools(toolNames)
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
			response += "- archive_tag_update: 更新文章标签\n"
			response += "- category_list: 查看分类列表\n"
			response += "- category_get: 获取分类详情\n"
			response += "- category_create: 创建分类\n"
			response += "- category_update: 更新分类\n"
			response += "- category_delete: 删除分类\n"
			response += "- page_list: 查看单页面列表\n"
			response += "- page_get: 获取单页面详情\n"
			response += "- page_create: 创建单页面\n"
			response += "- page_update: 更新单页面\n"
			response += "- page_delete: 删除单页面\n"
			response += "- moduel_list: 查看模块列表\n"
			response += "- module_get: 获取模块详情\n"
			response += "- module_create: 创建模块\n"
			response += "- module_update: 更新模块\n"
			response += "- module_delete: 删除模块\n"
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
	if svc.site != nil {
		response += fmt.Sprintf("当前站点：%s\n", svc.site.System.SiteName)
	}
	response += "输入 'help' 查看可用的工具和命令。\n\n可用工具：\n" + formatTools(toolNames)
	return response
}

// BuildToolMessages builds the message array for tool-calling conversations
func (svc *AiChatService) BuildToolMessages(sessionID string, systemPrompt string) []*schema.Message {
	var messages []*schema.Message
	messages = append(messages, schema.SystemMessage(systemPrompt))

	// Add conversation history (limit to last 10 messages)
	sess := svc.GetOrCreateSession(sessionID)
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
