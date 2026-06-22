package manageController

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/kataras/iris/v12"

	"kandaoni.com/anqicms/pkg/ai/eino"
	"kandaoni.com/anqicms/provider"
)

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
func AiChat(ctx iris.Context) {
	currentSite := provider.CurrentSubSite(ctx)
	if currentSite.AiSrv == nil {
		ctx.JSON(iris.Map{
			"code": -1,
			"msg":  "ai service not available",
		})
		return
	}
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

	// Set SSE headers
	ctx.ContentType("text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Connection", "keep-alive")
	writer := ctx.ResponseWriter()

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().UnixNano())
	}

	// Send session ID first
	fmt.Fprintf(writer, "event: session\ndata: %s\n\n", sessionID)
	writer.Flush()

	template := `{"api_key": "your-api-key","model": "deepseek-chat","base_url": "https://api.deepseek.com","max_tokens": 4096,"temperature": 0.7,"timeout_seconds": 60,"max_retries": 3}`

	if strings.HasPrefix(req.Message, "config:") {
		var cfg eino.Config
		tmpString := strings.TrimPrefix(req.Message, "config:")
		if err := json.Unmarshal([]byte(tmpString), &cfg); err == nil && cfg.APIKey != "" {
			// 保存到 setting 表
			if err := currentSite.SaveSettingValue(provider.AiSettingKey, cfg); err != nil {
				slog.Error("Failed to save AI setting", "error", err)
				sendSSEWarning(writer, "保存AI配置失败: "+err.Error())
				return
			}
			// 初始化AI客户端
			if err := eino.SetGlobalConfig(&cfg); err != nil {
				slog.Error("Failed to initialize AI client with provided config", "error", err)
				sendSSEWarning(writer, "AI配置无效: "+err.Error())
				return
			}
			slog.Info("AI client initialized with config from message")
			// 清空 message，避免将配置内容当作聊天消息处理
			warningData, _ := json.Marshal(iris.Map{
				"v":         "已添加AI配置",
				"timestamp": time.Now().Unix(),
			})
			fmt.Fprintf(writer, "event: message\ndata: %s\n\n", string(warningData))
			writer.Flush()
			return
		}
	}

	// 设置AI
	setting := eino.GlobalConfig()
	if setting == nil {
		// 未检测到有效配置，返回 JSON 模板提示
		sendSSEWarning(writer, "AI接口尚未配置或配置错误。请将以上 JSON 配置（替换为你自己的 api_key）作为消息内容发送以完成配置")
		fmt.Fprintf(writer, "event: config\ndata: %s\n\n", template)
		writer.Flush()
		return
	}
	// 接受配置信息提示
	if req.Message == "/config" || req.Message == "/设置" {
		// 显示配置信息
		sendSSEWarning(writer, "请将以上 JSON 配置（替换为你自己的 api_key）作为消息内容发送以完成配置")
		fmt.Fprintf(writer, "event: config\ndata: %s\n\n", template)
		writer.Flush()
		return
	}

	// ── Step 1: 接收与诊断 ──
	// 负面反馈检测: 如果用户消息较短且包含负面关键词，标记为不满
	message := req.Message
	negativeKeywords := []string{"不对", "错了", "不行", "还是不行", "没用", "不是这样", "搞错",
		"又错", "白做", "越改越差", "恢复", "回滚", "撤销",
		"wrong", "not right", "still broken", "doesn't work", "undo", "revert", "go back"}
	isNegative := false
	if len([]rune(message)) < 80 {
		lowerMsg := strings.ToLower(message)
		for _, kw := range negativeKeywords {
			if strings.Contains(lowerMsg, kw) || strings.Contains(message, kw) {
				isNegative = true
				break
			}
		}
	}
	if isNegative {
		// 附加诊断提示，让模型回顾之前的操作
		message += "\n\n[系统诊断: 检测到你可能对之前的回答不满意。请回顾之前的操作，仔细检查是否有错误，然后重新处理。如果需要回滚或撤销，请明确说明。]"
	}

	// 自动诊断: 如果用户消息包含错误关键词，扫描日志附加错误信息
	if provider.ContainsErrorKeywords(message) {
		diagInfo := autoDiagnoseErrors()
		if diagInfo != "" {
			message += "\n\n[系统发现以下可能的错误信息]:\n" + diagInfo
		}
	}

	// Add user message to history
	currentSite.AiSrv.AddMessage(sessionID, provider.ChatMessage{
		Role:    "user",
		Content: message,
	})

	requestCtx := ctx.Request().Context()

	// Generate AI response — try DeepSeek first, fall back to keyword matching
	response, err := generateAIResponse(requestCtx, ctx, sessionID, message, writer)
	if err != nil {
		slog.Error("AI response generation failed", "error", err)
		if strings.Contains(err.Error(), "401") {
			sendSSEWarning(writer, "AI接口尚未配置或配置错误。请将以上 JSON 配置（替换为你自己的 api_key）作为消息内容发送以完成配置")
			fmt.Fprintf(writer, "event: config\ndata: %s\n\n", template)
			writer.Flush()
			return
		}
		// Fallback: use keyword-based response
		var toolNames []string
		allTools := currentSite.AiSrv.GetAllTools()
		for _, tool := range allTools {
			toolNames = append(toolNames, fmt.Sprintf("- %s: %s", tool.Name, tool.Description))
		}
		currentSite := provider.CurrentSite(ctx)
		response = currentSite.AiSrv.BuildAIResponse(message, toolNames)
	}

	// Add assistant message to history
	currentSite.AiSrv.AddMessage(sessionID, provider.ChatMessage{
		Role:    "assistant",
		Content: response,
	})

	// Send final end event
	if writer != nil {
		fmt.Fprintf(writer, "event: end\ndata: [DONE]\n\n")
		writer.Flush()
	}
}

// generateAIResponse calls the DeepSeek API via Eino with tool support and streams the response back via SSE.
// Implements a 7-step verification workflow inspired by atomcode:
// Step 1: 接收与诊断 | Step 2: 上下文构建 | Step 3: 模型推理 | Step 4: 工具执行
// Step 5: 错误恢复 | Step 6: 验证 | Step 7: 压缩与闭环
func generateAIResponse(ctx context.Context, irisCtx iris.Context, sessionID string, userMessage string, writer io.Writer) (string, error) {
	// Try to get the Eino client
	client, err := eino.GetClient()
	if err != nil {
		return "", fmt.Errorf("AI client not available: %w", err)
	}

	// ── Step 1 + 2: 构建系统提示（含诊断信息、工作流指导） ──
	systemPrompt := `你是一个专业的 AnQiCMS 网站内容管理 AI 助手，帮助用户管理文章、分类、标签和附件。

## 工作流
请遵循以下步骤完成每个任务：
1. **先规划**：了解用户需求后，确定需要使用的工具和步骤，不要盲目操作。
2. **再执行**：按计划调用工具完成任务。
3. **验证**：执行修改操作后，运行验证工具（如 bash 执行构建/检查命令）确认修改正确。
4. **总结**：验证通过后，用中文总结完成的操作和结果。

## 可用工具
### 内容管理
- archive_create / archive_list / archive_get / archive_update / archive_delete / archive_tag_update / archive_publish: 文章管理
- category_create / category_list / category_get / category_update / category_delete: 分类管理
- page_create / page_list / page_get / page_update / page_delete: 页面管理
- module_create / module_list / module_get / module_update / module_delete: 模型管理
- tag_create / tag_list / tag_get / tag_update / tag_delete: 标签管理
- attachment_list / attachment_upload / attachment_delete: 附件管理

### 文件与代码
- read_file / write_file / edit_file / search_replace: 文件操作
- bash: 运行 shell 命令（编译、测试等）
- grep / glob / list_directory: 搜索和浏览项目文件
- web_fetch / web_search: 互联网搜索
- list_symbols / read_symbol / find_references: Go 代码分析
- call_graph / file_deps: 代码依赖和调用关系

## 使用指南
- 在创建文章时，先查看可用分类（使用 category_list 工具），然后选择合适的分类ID。
- 在创建分类时，先查看可用模型（使用 module_list 工具），然后选择合适的模型ID。
- 请用中文回复，保持专业、友好的语气。
- 执行了修改操作（创建/更新/删除）后，先验证再继续下一步。

## 技能系统(Skills)
- skill_list: 列出所有可用技能。当用户任务需要专业指导时先调用此工具。
- skill_get: 加载指定技能的完整内容。先确认技能匹配再调用。
- skill_reload: 管理员编辑技能后使用。
处理专业任务时，先查看可用技能，再加载匹配的技能内容并遵循其指导。

用户的操作都会在当前站点中执行，请根据实际情况使用工具。`

	currentSite := provider.CurrentSite(irisCtx)
	if currentSite != nil {
		systemPrompt += fmt.Sprintf("\n\n当前站点：%s", currentSite.System.SiteName)
	}

	// ── Step 2: 上下文构建（带智能窗口压缩） ──
	messages := currentSite.AiSrv.BuildToolMessages(sessionID, systemPrompt)

	// Add current user message
	messages = append(messages, schema.UserMessage(userMessage))

	// Bind tools to the client
	if len(currentSite.AiSrv.Tools) > 0 {
		if err := client.BindTools(currentSite.AiSrv.Tools); err != nil {
			return "", fmt.Errorf("failed to bind tools: %w", err)
		}
	}

	// ── 7步验证工作流主循环 ──
	maxRounds := 15
	var finalResponse string
	var round int
	var retryCount int
	var consecutiveReads int   // Step 4: 空转检测计数器
	var executedTools []string // Step 4: 本轮已执行工具列表
	var hadWriteOperation bool // Step 4: 是否执行过写操作
	var totalTokens int        // Token 统计
	contextCompactCalls := 0   // 上下文压缩计数

	for round = 0; round < maxRounds; round++ {
		// ── Step 5: 错误恢复 — 重试循环 ──
		var stream *schema.StreamReader[*schema.Message]
		for attempt := 0; attempt <= 3; attempt++ {
			if attempt > 0 {
				retryCount++
			}
			stream, err = client.Stream(ctx, messages)
			if err == nil {
				break
			}
			if provider.IsRateLimitError(err) && attempt < 3 {
				wait := time.Duration((attempt+1)*3) * time.Second
				sendSSEWarning(writer, fmt.Sprintf("请求频率限制，%v 后重试(%d/3)...", wait, attempt+1))
				slog.Warn("Rate limited, retrying", "attempt", attempt+1, "wait", wait)
				time.Sleep(wait)
				continue
			}
			if provider.IsContextOverflowError(err) {
				sendSSEWarning(writer, "上下文过长，正在压缩...")
				slog.Warn("Context overflow, compacting messages")
				messages = provider.CompactMessages(messages, 5)
				contextCompactCalls++
				continue
			}
			return "", fmt.Errorf("AI stream generate failed: %w", err)
		}
		if stream == nil {
			return "", fmt.Errorf("AI stream generate failed after retries")
		}

		// ── Step 3: 模型推理（流式接收） ──
		var fullResponse strings.Builder
		var fullReasoning strings.Builder
		var roundToolCalls []schema.ToolCall
		var toolCallChunks []*schema.Message
		var promptTokens, completionTokens int

		for {
			msg, err := stream.Recv()
			if err != nil {
				break
			}

			// 跟踪 token 使用
			if msg.ResponseMeta != nil {
				if msg.ResponseMeta.Usage != nil {
					promptTokens = msg.ResponseMeta.Usage.PromptTokens
					completionTokens = msg.ResponseMeta.Usage.CompletionTokens
				}
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

			// Collect all tool call chunks for later merging
			if len(msg.ToolCalls) > 0 {
				toolCallChunks = append(toolCallChunks, msg)
			}
		}
		stream.Close()

		totalTokens += promptTokens + completionTokens

		// Merge all tool call chunks accumulated during streaming.
		// Individual chunks contain partial data (e.g. empty ID, split Arguments),
		// so we must use schema.ConcatMessages to reconstruct the full tool calls.
		if len(toolCallChunks) > 0 {
			merged, err := schema.ConcatMessages(toolCallChunks)
			if err == nil && merged != nil && len(merged.ToolCalls) > 0 {
				roundToolCalls = merged.ToolCalls
			}
		}

		// ── Step 6: 检查模型是否完成 ──
		if len(roundToolCalls) == 0 {
			// No tool calls — this is the final text response
			finalResponse = fullResponse.String()
			if hadWriteOperation && finalResponse == "" {
				// 有修改但没有总结，注入验证提示
				sendSSEWarning(writer, "正在验证修改...")
			}
			break
		}

		// ── Step 4: 工具执行 ──
		// Add the assistant's message (with tool calls and reasoning content) to the history
		assistantMsg := &schema.Message{
			Role:             schema.Assistant,
			Content:          fullResponse.String(),
			ToolCalls:        roundToolCalls,
			ReasoningContent: fullReasoning.String(),
		}
		messages = append(messages, assistantMsg)

		// Save intermediate assistant message (with tool calls) to session history
		toolCallsJSON, _ := json.Marshal(roundToolCalls)
		currentSite.AiSrv.AddMessage(sessionID, provider.ChatMessage{
			Role:      "assistant",
			Content:   fullResponse.String(),
			ToolCalls: string(toolCallsJSON),
		})

		// 执行工具前的统计
		var currentRoundExecutedTools []string
		var currentRoundHadWrite bool

		// Execute each tool
		for _, tc := range roundToolCalls {
			toolName := tc.Function.Name
			argsJSON := tc.Function.Arguments

			currentSite.AiSrv.Logger.Info("Executing tool",
				"name", toolName,
				"args", argsJSON,
				"round", round)

			// Send tool_call event to client
			toolCallData, _ := json.Marshal(iris.Map{
				"name":         toolName,
				"arguments":    argsJSON,
				"tool_call_id": tc.ID,
			})
			if writer != nil {
				fmt.Fprintf(writer, "event: tool_call\ndata: %s\n\n", string(toolCallData))
				if f, ok := writer.(interface{ Flush() error }); ok {
					f.Flush()
				}
			}

			// Execute the handler
			handler, exists := currentSite.AiSrv.Handlers[toolName]
			var result string
			if !exists {
				result = fmt.Sprintf("错误：未知工具 %s", toolName)
			} else {
				result, err = handler(ctx, argsJSON)
				if err != nil {
					result = fmt.Sprintf("工具执行错误: %s", err.Error())
				}
			}

			currentSite.AiSrv.Logger.Info("Tool result",
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

			// Save tool result to session history (truncated if too large, condensed with tool name)
			toolContent := condenseToolContent(result, toolName)
			currentSite.AiSrv.AddMessage(sessionID, provider.ChatMessage{
				Role:       "tool",
				Content:    toolContent,
				ToolCallID: tc.ID,
				ToolName:   toolName,
			})

			// 跟踪工具类型
			currentRoundExecutedTools = append(currentRoundExecutedTools, toolName)
			if provider.HasWriteOperation([]string{toolName}) {
				currentRoundHadWrite = true
				hadWriteOperation = true
			}

			// 截断过大的工具结果
			if len(result) > 10000 {
				result = result[:10000] + "\n... [结果已截断]"
			}
		}

		executedTools = append(executedTools, currentRoundExecutedTools...)

		// ── Step 4 (续): 空转检测 ──
		if currentRoundHadWrite {
			consecutiveReads = 0
		} else {
			// 检查是否全是读取操作
			allReadOnly := true
			for _, name := range currentRoundExecutedTools {
				if provider.HasWriteOperation([]string{name}) {
					allReadOnly = false
					break
				}
			}
			if allReadOnly {
				consecutiveReads++
			} else {
				consecutiveReads = 0
			}
		}

		// 如果连续多轮只有读取操作，注入空转警告
		if consecutiveReads >= 4 {
			sendSSEWarning(writer, "检测到连续读取操作，提示模型聚焦实际任务")
			slog.Warn("Stagnation detected", "consecutiveReads", consecutiveReads)
			warningMsg := schema.SystemMessage(
				"[系统提示] 你已经连续多次执行读取操作但没有执行任何修改。请评估当前进度，" +
					"如果已经获取了足够的信息，请开始执行实际的操作（创建/更新/删除/发布）。")
			messages = append(messages, warningMsg)
			consecutiveReads = 0
			continue
		}

		// ── Step 6: 验证注入 ──
		// 如果本轮有写操作且有可运行的验证手段，注入验证提示
		if currentRoundHadWrite {
			sendSSEWarning(writer, "修改已执行，正在等待模型验证...")
			slog.Info("Write operation detected, injecting verification prompt")

			verifyMsg := schema.SystemMessage(
				"[系统验证] 你已经成功执行了修改操作。请先验证你的修改是否正确，" +
					"必要时通过 bash 工具运行构建/检查/测试命令确认没有引入错误。" +
					"验证通过后再总结回答用户。")
			messages = append(messages, verifyMsg)
			continue
		}

		// ── Step 7: 上下文压缩（每 3 轮或消息过多时） ──
		if len(messages) > 12 && (round%3 == 2 || len(messages) > 20) {
			slog.Info("Compressing context", "messageCount", len(messages), "round", round)
			messages = provider.CompactMessages(messages, 5)
			contextCompactCalls++
		}
	}

	// 统计日志
	slog.Info("AI response completed",
		"rounds", round+1,
		"totalTokens", totalTokens,
		"contextCompactCalls", contextCompactCalls,
		"toolsExecuted", len(executedTools))

	if round == maxRounds && finalResponse == "" {
		// 获取最后一次响应的文本
		if len(messages) >= 2 {
			lastMsg := messages[len(messages)-1]
			if lastMsg.Role == "assistant" {
				finalResponse = lastMsg.Content
			}
		}
		if finalResponse == "" {
			return "", fmt.Errorf("工具调用次数超过上限，未获取到最终回复")
		}
	}

	return finalResponse, nil
}

// sendSSEWarning sends a warning event via SSE
func sendSSEWarning(writer io.Writer, warning string) {
	if writer == nil {
		return
	}
	warningData, _ := json.Marshal(iris.Map{
		"v":         warning,
		"timestamp": time.Now().Unix(),
	})
	fmt.Fprintf(writer, "event: warning\ndata: %s\n\n", string(warningData))
	if f, ok := writer.(interface{ Flush() error }); ok {
		f.Flush()
	}
}

// condenseToolContent condenses a tool result for history storage based on the tool type.
// read_file: compress to skeleton (extract signatures/imports)
// bash: keep first 2 lines
// Others: keep first 500 chars
func condenseToolContent(result string, toolName string) string {
	if len(result) <= 500 {
		return result
	}
	switch toolName {
	case "read_file":
		return provider.CompressFileToSkeleton(result)
	case "bash", "shell":
		lines := strings.SplitN(result, "\n", 3)
		if len(lines) <= 2 {
			return result
		}
		return strings.Join(lines[:2], "\n") + "\n... [已截断]"
	default:
		runes := []rune(result)
		if len(runes) > 500 {
			return string(runes[:500]) + "\n... [已截断]"
		}
		return result
	}
}

// autoDiagnoseErrors scans recent log output and returns formatted error context.
func autoDiagnoseErrors() string {
	// 这里可以扩展为读取日志文件并提取最新错误
	// 目前返回空字符串，表示没有额外的诊断信息
	return ""
}

// GetHistory returns chat history
func GetAiHistory(ctx iris.Context) {
	currentSite := provider.CurrentSubSite(ctx)
	if currentSite.AiSrv == nil {
		ctx.JSON(iris.Map{
			"code": -1,
			"msg":  "ai service not available",
		})
		return
	}
	sessionID := ctx.URLParamDefault("session_id", "")
	if sessionID == "" {
		ctx.JSON(iris.Map{
			"code": -1,
			"msg":  "session_id is required",
		})
		return
	}

	messages := currentSite.AiSrv.GetMessages(sessionID)
	ctx.JSON(iris.Map{
		"code": 0,
		"msg":  "success",
		"data": messages,
	})
}

// Health returns health status
func AiHealth(ctx iris.Context) {
	currentSite := provider.CurrentSubSite(ctx)
	if currentSite.AiSrv == nil {
		ctx.JSON(iris.Map{
			"code": -1,
			"msg":  "ai service not available",
		})
		return
	}
	ctx.JSON(iris.Map{
		"code": 0,
		"msg":  "ok",
		"data": iris.Map{
			"service": "anqicms-ai-chat",
			"status":  "running",
		},
	})
}
