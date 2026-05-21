package manageController

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("session_%d", time.Now().UnixNano())
	}

	// Add user message to history
	currentSite.AiSrv.AddMessage(sessionID, provider.ChatMessage{
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
	response, err := generateAIResponse(requestCtx, ctx, sessionID, req.Message, writer)
	if err != nil {
		slog.Error("AI response generation failed", "error", err)
		// Fallback: use keyword-based response
		var toolNames []string
		allTools := currentSite.AiSrv.GetAllTools()
		for _, tool := range allTools {
			toolNames = append(toolNames, fmt.Sprintf("- %s: %s", tool.Name, tool.Description))
		}
		currentSite := provider.CurrentSite(ctx)
		response = currentSite.AiSrv.BuildAIResponse(req.Message, toolNames)
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

// generateAIResponse calls the DeepSeek API via Eino with tool support and streams the response back via SSE
func generateAIResponse(ctx context.Context, irisCtx iris.Context, sessionID string, userMessage string, writer io.Writer) (string, error) {
	// Try to get the Eino client
	client, err := eino.GetClient()
	if err != nil {
		return "", fmt.Errorf("AI client not available: %w", err)
	}

	// Build system prompt
	systemPrompt := `你是一个专业的 AnQiCMS 网站内容管理 AI 助手，帮助用户管理文章、分类、标签和附件。

你可以使用以下工具完成用户请求：
- archive_create / archive_list / archive_get / archive_delete / archive_tag_update / archive_publish: 文章管理
- category_create / category_list / category_get / category_delete: 分类管理
- page_create / page_list / page_get / page_delete: 页面管理
- module_create / module_list / module_get / module_delete: 模型管理
- tag_create / tag_list / tag_get / tag_delete: 标签管理

在创建文章时，请先查看可用分类（使用 category_list 工具），然后选择合适的分类ID。
在创建分类时，请先查看可用模型（使用 module_list 工具），然后选择合适的模型ID。
请用中文回复，保持专业、友好的语气。

用户的操作都会在当前站点中执行，请根据实际情况使用工具。`

	currentSite := provider.CurrentSite(irisCtx)
	if currentSite != nil {
		systemPrompt += fmt.Sprintf("\n\n当前站点：%s", currentSite.System.SiteName)
	}

	// Build messages array: system + history + current user message
	messages := currentSite.AiSrv.BuildToolMessages(sessionID, systemPrompt)

	// Bind tools to the client
	if len(currentSite.AiSrv.Tools) > 0 {
		if err := client.BindTools(currentSite.AiSrv.Tools); err != nil {
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
		var toolCallChunks []*schema.Message

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

			// Collect all tool call chunks for later merging
			// Stream chunks may contain partial tool calls spread across multiple frames;
			// we must concatenate them properly to get complete tool names, IDs and arguments.
			if len(msg.ToolCalls) > 0 {
				toolCallChunks = append(toolCallChunks, msg)
			}
		}

		stream.Close()

		// Merge all tool call chunks into complete tool calls
		if len(toolCallChunks) > 0 {
			merged, err := schema.ConcatMessages(toolCallChunks)
			if err == nil && merged != nil {
				roundToolCalls = merged.ToolCalls
			}
		}

		// Check if the model wants to call tools
		if len(roundToolCalls) == 0 {
			// No tool calls — this is the final text response
			finalResponse = fullResponse.String()
			break
		}

		// ---- Execute tool calls ----
		// Add the assistant's message (with tool calls and reasoning content) to the history
		// DeepSeek requires that reasoning_content be passed back when thinking mode is used.
		assistantMsg := &schema.Message{
			Role:             schema.Assistant,
			Content:          fullResponse.String(),
			ToolCalls:        roundToolCalls,
			ReasoningContent: fullReasoning.String(),
		}
		messages = append(messages, assistantMsg)

		// Execute each tool
		for _, tc := range roundToolCalls {
			toolName := tc.Function.Name
			argsJSON := tc.Function.Arguments

			log.Printf("roundToolCalls: %+v", tc)

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
		}

		// Continue the loop to get the next AI response with tool results
	}

	if round == maxRounds && finalResponse == "" {
		return "", fmt.Errorf("工具调用次数超过上限")
	}

	return finalResponse, nil
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
