package provider

import (
	"encoding/json"
	"fmt"
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

// Turn represents a round of interaction: one user message + all following
// AI responses and tool calls/results.
type Turn struct {
	StartIdx int    `json:"start_idx"` // index in Messages slice
	MsgCount int    `json:"msg_count"` // number of messages in this turn
	Summary  string `json:"summary"`   // LLM-generated summary (after compression)
}

// ChatSession represents a user chat session
type ChatSession struct {
	ID        string        `json:"id"`
	Messages  []ChatMessage `json:"messages"`
	Turns     []Turn        `json:"turns"`
	CreatedAt time.Time     `json:"created_at"`
}

// ChatMessage represents a message in a conversation
type ChatMessage struct {
	Role        string `json:"role"`
	Content     string `json:"content"`
	CreatedTime int64  `json:"created_time"`
	ToolCallID  string `json:"tool_call_id,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	TurnID      uint   `json:"turn_id,omitempty"`
	ToolCalls   string `json:"tool_calls,omitempty"`
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
				ToolCallID:  dbm.ToolCallID,
				ToolName:    dbm.ToolName,
				TurnID:      dbm.TurnID,
				ToolCalls:   dbm.ToolCalls,
			})
		}
		rebuildTurns(sess)
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
					ToolCallID:  dbm.ToolCallID,
					ToolName:    dbm.ToolName,
					TurnID:      dbm.TurnID,
				})
			}
			rebuildTurns(sess)
			svc.sessions[sessionID] = sess
			return sess
		}
	}

	sess := &ChatSession{
		ID:        sessionID,
		Messages:  make([]ChatMessage, 0),
		Turns:     make([]Turn, 0),
		CreatedAt: time.Now(),
	}
	svc.sessions[sessionID] = sess
	return sess
}

// AddMessage adds a message to a session and updates turn tracking.
func (svc *AiChatService) AddMessage(sessionID string, msg ChatMessage) {
	svc.mu.Lock()

	sess, exists := svc.sessions[sessionID]
	if !exists {
		sess = &ChatSession{
			ID:        sessionID,
			Messages:  make([]ChatMessage, 0),
			Turns:     make([]Turn, 0),
			CreatedAt: time.Now(),
		}
		svc.sessions[sessionID] = sess
	}
	now := time.Now().Unix()
	msg.CreatedTime = now

	// Turn tracking: user message starts a new turn
	if msg.Role == "user" {
		// Close previous active turn
		if len(sess.Turns) > 0 {
			last := &sess.Turns[len(sess.Turns)-1]
			last.MsgCount = len(sess.Messages) - last.StartIdx
		}
		// New turn starts at the upcoming message index
		sess.Turns = append(sess.Turns, Turn{
			StartIdx: len(sess.Messages),
			MsgCount: 1,
		})
	}

	sess.Messages = append(sess.Messages, msg)

	// Update turn count for non-user messages (extend current turn)
	if msg.Role != "user" && len(sess.Turns) > 0 {
		last := &sess.Turns[len(sess.Turns)-1]
		last.MsgCount = len(sess.Messages) - last.StartIdx
	}

	svc.mu.Unlock()

	// Persist to database asynchronously
	if svc.db != nil {
		go func() {
			dbMsg := &model.AiChatMessage{
				SessionId:  sessionID,
				Role:       msg.Role,
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
				ToolName:   msg.ToolName,
				TurnID:     msg.TurnID,
				ToolCalls:  msg.ToolCalls,
			}
			if err := svc.db.Create(dbMsg).Error; err != nil {
				svc.Logger.Error("Failed to persist chat message", "error", err)
			}
		}()
	}
}

// rebuildTurns rebuilds the TurnTracker from the session's message list.
func rebuildTurns(sess *ChatSession) {
	sess.Turns = make([]Turn, 0)
	for i, msg := range sess.Messages {
		if msg.Role == "user" {
			sess.Turns = append(sess.Turns, Turn{
				StartIdx: i,
				MsgCount: 1,
			})
		} else if len(sess.Turns) > 0 {
			last := &sess.Turns[len(sess.Turns)-1]
			last.MsgCount = i - last.StartIdx + 1
		}
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
						Role:        dbm.Role,
						Content:     dbm.Content,
						CreatedTime: dbm.CreatedTime,
						ToolCallID:  dbm.ToolCallID,
						ToolName:    dbm.ToolName,
						TurnID:      dbm.TurnID,
						ToolCalls:   dbm.ToolCalls,
					})
				}
				rebuildTurns(sess)
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
// Uses smart windowing with turn-aware compression: keeps recent turns full
// and compresses older turns into a summary. Uses TurnTracker to ensure
// tool_call ↔ tool_result pairs are never split across the compaction boundary.
func (svc *AiChatService) BuildToolMessages(sessionID string, systemPrompt string) []*schema.Message {
	var messages []*schema.Message

	sess := svc.GetOrCreateSession(sessionID)

	// Build the message list with turn-aware compaction
	compactHistory := CompactMessagesFromChat(sess, systemPrompt, 5)
	messages = append(messages, compactHistory...)

	return messages
}

// CompactMessagesFromChat converts session chat history to schema messages
// with turn-aware smart windowing. It keeps the last keepTurns number of turns
// full and compresses older turn groups into a system message summary.
// The original system prompt is always prepended first.
//
// Turn-aware boundary: ensures tool_call ↔ tool_result pairs are never split.
// Tool results are condensed by type (read_file→skeleton, bash→first line, etc.)
func CompactMessagesFromChat(sess *ChatSession, systemPrompt string, keepTurns int) []*schema.Message {
	var messages []*schema.Message
	messages = append(messages, schema.SystemMessage(systemPrompt))

	if len(sess.Messages) == 0 {
		return messages
	}

	// Use the session's TurnTracker (rebuilt after compression, or fresh from DB load)
	turns := sess.Turns
	if len(turns) == 0 {
		// Fallback: rebuild inline
		rebuildTurns(sess)
		turns = sess.Turns
	}
	if len(turns) == 0 {
		return messages
	}

	// Determine how many turns we keep in full from the tail.
	// Only keep turns that start wholly after any compressed turns.
	var keepStartIdx int
	if len(turns) <= keepTurns {
		// No compression needed — keep all messages
		for _, msg := range sess.Messages {
			messages = append(messages, chatMessageToSchema(msg))
		}
		return messages
	}

	keepStartTurn := len(turns) - keepTurns
	keepStartIdx = turns[keepStartTurn].StartIdx

	// ── Phase 1: Compress older turns (turn 0 … keepStartTurn-1) ──
	// Build a concise summary as a system message
	var summaryParts []string
	for ti := 0; ti < keepStartTurn; ti++ {
		turn := turns[ti]
		turnEnd := turn.StartIdx + turn.MsgCount
		if turnEnd > len(sess.Messages) {
			turnEnd = len(sess.Messages)
		}
		turnMsgs := sess.Messages[turn.StartIdx:turnEnd]
		summaryParts = append(summaryParts, condenseTurn(turnMsgs))
	}

	summary := "[历史对话摘要]\n" + strings.Join(summaryParts, "\n---\n")
	messages = append(messages, schema.SystemMessage(summary))

	// ── Phase 2: Keep the last keepTurns turns in full, with condensed tool results ──
	for i := keepStartIdx; i < len(sess.Messages); i++ {
		msg := sess.Messages[i]
		if msg.Role == "tool" {
			// Condense tool result based on tool name
			condensed := condenseToolResult(msg)
			messages = append(messages, schema.ToolMessage(condensed, msg.ToolCallID))
		} else {
			messages = append(messages, chatMessageToSchema(msg))
		}
	}

	return messages
}

// chatMessageToSchema converts a ChatMessage to a schema.Message.
func chatMessageToSchema(msg ChatMessage) *schema.Message {
	if msg.Role == "user" {
		return schema.UserMessage(msg.Content)
	} else if msg.Role == "assistant" {
		if msg.ToolCalls != "" {
			var toolCalls []schema.ToolCall
			if err := json.Unmarshal([]byte(msg.ToolCalls), &toolCalls); err == nil {
				return schema.AssistantMessage(msg.Content, toolCalls)
			}
		}
		return schema.AssistantMessage(msg.Content, nil)
	} else if msg.Role == "tool" {
		return schema.ToolMessage(msg.Content, msg.ToolCallID)
	}
	// Fallback: treat as user message
	return schema.UserMessage(msg.Content)
}

// condenseTurn condenses an entire turn (user+assistant+tool messages) into
// a single-line summary for the compressed history block.
func condenseTurn(msgs []ChatMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	// Extract user message
	userContent := ""
	for _, m := range msgs {
		if m.Role == "user" {
			userContent = truncate(m.Content, 150)
			break
		}
	}
	// Collect tool names used
	toolNames := make([]string, 0)
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolName != "" {
			toolNames = append(toolNames, m.ToolName)
		}
	}
	// Find assistant final response
	assistantContent := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			assistantContent = truncate(msgs[i].Content, 200)
			break
		}
	}

	var parts []string
	if userContent != "" {
		parts = append(parts, "用户: "+userContent)
	}
	if len(toolNames) > 0 {
		parts = append(parts, "工具: "+strings.Join(uniqueStrings(toolNames), ", "))
	}
	if assistantContent != "" {
		parts = append(parts, "AI: "+assistantContent)
	}
	return strings.Join(parts, " | ")
}

// condenseToolResult condenses a tool result based on the tool type.
// Follows atomcode's condensed() pattern (message.rs:162-202):
// - read_file: compress to skeleton (extract signatures/imports)
// - bash: keep first 2 lines
// - Others: truncate to 200 chars, append "..."
func condenseToolResult(msg ChatMessage) string {
	content := msg.Content
	if len(content) <= 200 {
		return content
	}
	switch msg.ToolName {
	case "read_file":
		return CompressFileToSkeleton(content)
	case "bash", "shell":
		// Keep first 2 lines
		lines := strings.SplitN(content, "\n", 3)
		if len(lines) <= 2 {
			return content
		}
		return strings.Join(lines[:2], "\n") + "\n..."
	default:
		return truncate(content, 200)
	}
}

// truncate truncates a string to maxChars runes, appending "..." if truncated.
func truncate(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars]) + "..."
}

// uniqueStrings returns deduplicated strings preserving order.
func uniqueStrings(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	result := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			result = append(result, v)
		}
	}
	return result
}

// signatureKeywords lists line prefixes that indicate a structural declaration.
// Matches atomcode's compress_file_to_skeleton (message.rs:249-279).
var signatureKeywords = []string{
	"fn ", "pub fn ", "async fn ", "pub async fn ",
	"def ", "class ", "function ", "func ",
	"export ", "import ", "const ", "let ",
	"public ", "private ", "protected ",
	"interface ", "type ", "struct ", "enum ", "impl ",
	"package ", "use ", "from ", "#include",
}

// isSignatureLine checks whether a trimmed content line looks like a structural
// declaration at indent 0-2.
func isSignatureLine(content string) bool {
	if len(content) == 0 {
		return false
	}
	indent := 0
	for _, c := range content {
		if c == ' ' || c == '\t' {
			indent++
		} else {
			break
		}
	}
	if indent > 2 {
		return false
	}
	trimmed := strings.TrimSpace(content)
	for _, kw := range signatureKeywords {
		if strings.HasPrefix(trimmed, kw) {
			return true
		}
	}
	// Decorators / attributes
	if strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "#[") {
		return true
	}
	// Vue/HTML template markers
	if trimmed == "<template>" || trimmed == "</template>" ||
		trimmed == "<script>" || trimmed == "</script>" ||
		trimmed == "<style>" || trimmed == "</style>" ||
		strings.HasPrefix(trimmed, "<template ") ||
		strings.HasPrefix(trimmed, "<script ") ||
		strings.HasPrefix(trimmed, "<style ") {
		return true
	}
	return false
}

// CompressFileToSkeleton extracts structural signatures from a read_file output
// (numbered lines). Returns ~10-20% of the original content but preserves
// function/class/import structure for the LLM. Falls back to first line + count
// if no signatures are found.
// Matches atomcode's compress_file_to_skeleton (message.rs:243-325).
func CompressFileToSkeleton(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return output
	}

	totalLines := 0
	var skeleton []string

	for _, line := range lines {
		// Parse "文件: path (N 行, M 字节)" header
		if strings.HasPrefix(line, "文件:") {
			skeleton = append(skeleton, line)
			continue
		}
		// Parse "  N| content" numbered lines
		content := line
		if len(line) > 7 {
			// Try to strip "%6d| " prefix (6 digit + "| ")
			rest := strings.TrimLeft(line, " ")
			if len(rest) >= 2 && rest[0] >= '0' && rest[0] <= '9' {
				// Find the "| " separator
				if idx := strings.Index(rest, "| "); idx >= 0 && idx <= 6 {
					totalLines++
					content = rest[idx+2:]
				}
			}
		}
		trimmed := strings.TrimSpace(content)
		if trimmed == "" {
			continue
		}
		if isSignatureLine(content) {
			skeleton = append(skeleton, strings.TrimRight(line, " "))
		}
	}

	if len(skeleton) <= 1 { // Header only or nothing
		first := lines[0]
		if len(lines) > 1 {
			return first + fmt.Sprintf(" (%d 行)", len(lines))
		}
		return first
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("[文件骨架 — %d 行，可使用 edit_file 编辑]\n", totalLines))
	for _, s := range skeleton {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
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

// IsRateLimitError checks if the error indicates a rate limit hit.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	patterns := []string{
		"rate limit",
		"rate_limit",
		"too many requests",
		"throttle",
		"429",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// IsContextOverflowError checks if the error is related to context window exceeding.
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	patterns := []string{
		"context length exceeded",
		"too many tokens",
		"max tokens exceeded",
		"prompt too long",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// HasWriteOperation checks if any of the tool names is a write operation.
func HasWriteOperation(toolNames []string) bool {
	writeOps := map[string]bool{
		"archive_create":      true,
		"archive_update":      true,
		"archive_delete":      true,
		"category_create":     true,
		"category_update":     true,
		"category_delete":     true,
		"page_create":         true,
		"page_update":         true,
		"page_delete":         true,
		"module_create":       true,
		"module_update":       true,
		"module_delete":       true,
		"tag_create":          true,
		"tag_update":          true,
		"tag_delete":          true,
		"archive_publish":     true,
		"archive_tag_update":  true,
		"attachment_upload":   true,
		"attachment_delete":   true,
		"write_file":          true,
		"edit_file":           true,
		"create_file":         true,
		"search_replace":      true,
		"bash":                true,
	}
	for _, name := range toolNames {
		if writeOps[name] {
			return true
		}
	}
	return false
}

// ContainsErrorKeywords checks if the string contains common error keywords.
func ContainsErrorKeywords(s string) bool {
	lower := strings.ToLower(s)
	patterns := []string{
		"error",
		"exception",
		"traceback",
		"panic",
	}
	chinesePatterns := []string{
		"错误",
		"异常",
		"报错",
		"失败",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	for _, p := range chinesePatterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// CompactMessages compresses older messages in a schema.Message slice into a
// [系统压缩] summary system message. It keeps the first (system) message and the
// last keepCount messages intact, summarizing everything in between.
// Uses safe boundary detection: never splits tool_call ↔ tool_result pairs.
// Anti-nesting: skips results with already-compressed summaries (does NOT
// re-summarize a [系统压缩] summary, instead replaces it in place).
func CompactMessages(messages []*schema.Message, keepCount int) []*schema.Message {
	if len(messages) <= keepCount+1 {
		return messages
	}

	// First message is the system prompt — always keep it
	systemMsg := messages[0]

	// ── Anti-nesting: detect existing compressed summary ──
	// If messages[1] is a system message with a compression marker, this
	// conversation was already compressed. In that case, remove it and
	// shift the index space so we compress fresh messages.
	compressedPrefix := 1 // default: compressible region starts at index 1
	if len(messages) > 2 && messages[1].Role == schema.System &&
		(strings.Contains(messages[1].Content, "[系统压缩]") ||
			strings.Contains(messages[1].Content, "[历史对话摘要]")) {
		compressedPrefix = 2 // skip the old summary, keep messages from index 2
	}

	// ── Find a safe cut point ──
	// Scan backwards from the end to find a boundary that doesn't split
	// tool_call/tool_result pairs.
	targetCut := len(messages) - keepCount
	// Ensure targetCut is within the compressible region
	if targetCut < compressedPrefix {
		targetCut = compressedPrefix
	}
	safeCut := targetCut

	// Backward scan from targetCut to find a safe boundary
	for i := targetCut; i >= compressedPrefix; i-- {
		msg := messages[i]
		if msg.Role == schema.User {
			safeCut = i
			break
		}
		if msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 {
			// Assistant without tool calls — safe, all tool results before this are complete
			safeCut = i
			break
		}
	}

	// If backward scan found no safe boundary, scan forward from targetCut
	if safeCut == targetCut {
		for i := targetCut + 1; i < len(messages); i++ {
			msg := messages[i]
			if msg.Role == schema.User {
				safeCut = i
				break
			}
			if msg.Role == schema.Assistant && len(msg.ToolCalls) == 0 {
				safeCut = i
				break
			}
		}
	}

	// ── Final API compliance validation ──
	// Ensure the first message in the kept block is not an orphan Tool result
	// or Assistant-with-tool-calls (which would have its paired messages in the
	// compressed region). If it is, advance to the next safe boundary.
	for safeCut < len(messages) {
		msg := messages[safeCut]
		if msg.Role == schema.Tool {
			// Orphan tool result — find next safe boundary
			safeCut++
			for safeCut < len(messages) {
				m := messages[safeCut]
				if m.Role == schema.User {
					break
				}
				if m.Role == schema.Assistant && len(m.ToolCalls) == 0 {
					break
				}
				safeCut++
			}
		} else if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			// Assistant with tool calls at the boundary — its results might be in
			// the compressed region. Advance past this and its tool results.
			safeCut++
			for safeCut < len(messages) {
				m := messages[safeCut]
				if m.Role != schema.Tool {
					break
				}
				safeCut++
			}
		} else {
			break
		}
	}

	// ── Compress messages between compressedPrefix and safeCut ──
	var summaryParts []string
	hasOldSummaryKept := false
	for i := compressedPrefix; i < safeCut; i++ {
		msg := messages[i]
		// Already-compressed summary in the middle — keep its content directly
		// instead of re-truncating it (anti-nesting)
		if msg.Role == schema.System &&
			(strings.Contains(msg.Content, "[系统压缩]") ||
				strings.Contains(msg.Content, "[历史对话摘要]")) {
			summaryParts = append(summaryParts, msg.Content)
			hasOldSummaryKept = true
			continue
		}
		role := string(msg.Role)
		content := msg.Content
		if len(content) > 150 {
			content = string([]rune(content)[:150]) + "..."
		}
		summaryParts = append(summaryParts, role+": "+content)
	}

	// Determine whether to keep the old summary or create a new one.
	// If we kept an old summary directly and it already contains all the info,
	// prepend a brief note rather than nesting another [系统压缩] layer.
	var compacted []*schema.Message
	compacted = append(compacted, systemMsg)

	if hasOldSummaryKept {
		// Old summary already includes the relevant history — just add a brief
		// context note and proceed
		compacted = append(compacted, schema.SystemMessage(
			"[系统压缩] 以下为历史对话摘要，包含上方压缩的历史信息"))
	} else if len(summaryParts) > 0 {
		summary := "[系统压缩] 以下为历史对话摘要:\n" + strings.Join(summaryParts, "\n---\n")
		compacted = append(compacted, schema.SystemMessage(summary))
	}

	// Keep from safeCut to end (may be empty if safeCut >= len(messages))
	if safeCut < len(messages) {
		compacted = append(compacted, messages[safeCut:]...)
	}

	return compacted
}
