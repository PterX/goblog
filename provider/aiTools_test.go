package provider

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// testService creates an AiChatService without a database (site=nil) for unit testing.
// All handlers will return "错误：站点未初始化" for DB-dependent operations.
func testService() *AiChatService {
	svc := &AiChatService{
		mu:       sync.RWMutex{},
		sessions: make(map[string]*ChatSession),
		Logger:   slog.Default(),
		mcpSrv:   nil,
		db:       nil,
		site:     nil,
	}
	svc.Tools, svc.Handlers = svc.getEinoTools()
	return svc
}

// expectedTools defines the expected set of AI tool names.
var expectedTools = []string{
	"archive_list",
	"archive_get",
	"archive_create",
	"archive_delete",
	"archive_publish",
	"module_list",
	"module_get",
	"module_create",
	"module_delete",
	"category_list",
	"category_get",
	"category_create",
	"category_delete",
	"tag_list",
	"tag_get",
	"tag_create",
	"tag_delete",
}

func TestGetEinoTools_AllDefined(t *testing.T) {
	svc := testService()

	if len(svc.Tools) != len(expectedTools) {
		t.Errorf("expected %d tools, got %d", len(expectedTools), len(svc.Tools))
	}

	// Check that all expected tools exist
	nameSet := make(map[string]bool)
	for _, ti := range svc.Tools {
		nameSet[ti.Name] = true
		// Each tool must have a description
		if ti.Desc == "" {
			t.Errorf("tool %q has empty description", ti.Name)
		}
		// Each tool must have a parameter schema (even if nil/empty)
		if ti.ParamsOneOf == nil {
			t.Errorf("tool %q has nil ParamsOneOf", ti.Name)
		}
	}
	for _, name := range expectedTools {
		if !nameSet[name] {
			t.Errorf("expected tool %q not found in getEinoTools() output", name)
		}
	}

	// Check all handlers are registered
	for _, ti := range svc.Tools {
		if _, exists := svc.Handlers[ti.Name]; !exists {
			t.Errorf("handler for tool %q not registered", ti.Name)
		}
	}
}

func Test_GetAllTools_ReturnsAll(t *testing.T) {
	svc := testService()
	mcpTools := svc.GetAllTools()

	if len(mcpTools) != len(expectedTools) {
		t.Errorf("expected %d MCP tools, got %d", len(expectedTools), len(mcpTools))
	}

	nameSet := make(map[string]bool)
	for _, mt := range mcpTools {
		nameSet[mt.Name] = true
	}
	for _, name := range expectedTools {
		if !nameSet[name] {
			t.Errorf("expected MCP tool %q not found", name)
		}
	}
}

// --- Argument parsing tests ---

func Test_ArchiveList_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["archive_list"]

	// Valid JSON with all optional params
	result, err := handler(context.Background(), `{"page":2,"page_size":5,"category_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Empty JSON (all defaults)
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `not-json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_ArchiveGet_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["archive_get"]

	// Valid args
	result, err := handler(context.Background(), `{"archive_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing archive_id — handler will still try GetArchiveById(0) which needs DB
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `invalid`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_ArchiveCreate_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["archive_create"]

	// Valid args
	result, err := handler(context.Background(), `{"title":"Test","content":"Content","category_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing title → returns error message (not error)
	result, err = handler(context.Background(), `{"content":"Content","category_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：文章标题不能为空" {
		t.Fatalf("expected '文章标题不能为空', got %q", result)
	}

	// Missing content
	result, err = handler(context.Background(), `{"title":"Test","category_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：文章内容不能为空" {
		t.Fatalf("expected '文章内容不能为空', got %q", result)
	}

	// Missing category_id
	result, err = handler(context.Background(), `{"title":"Test","content":"Content"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：请指定分类ID" {
		t.Fatalf("expected '请指定分类ID', got %q", result)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `not-json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_ArchiveDelete_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["archive_delete"]

	// Valid args
	result, err := handler(context.Background(), `{"archive_id":5}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing archive_id
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_ArchivePublish_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["archive_publish"]

	// Valid args
	result, err := handler(context.Background(), `{"archive_id":1,"status":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing status
	result, err = handler(context.Background(), `{"archive_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_CategoryList_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["category_list"]

	// Even empty args should work (no params required)
	result, err := handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}
}

func Test_CategoryGet_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["category_get"]

	// Valid args
	result, err := handler(context.Background(), `{"category_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing category_id
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_CategoryCreate_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["category_create"]

	// Valid args
	result, err := handler(context.Background(), `{"title":"Test Category","parent_id":0}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing title
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：分类名称不能为空" {
		t.Fatalf("expected '分类名称不能为空', got %q", result)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_CategoryDelete_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["category_delete"]

	// Valid args
	result, err := handler(context.Background(), `{"category_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing category_id
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_TagList_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["tag_list"]

	// Even empty args should work
	result, err := handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}
}

func Test_TagGet_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["tag_get"]

	// Valid args
	result, err := handler(context.Background(), `{"tag_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing tag_id
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_TagCreate_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["tag_create"]

	// Valid args
	result, err := handler(context.Background(), `{"title":"Test Tag"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing title
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：标签名称不能为空" {
		t.Fatalf("expected '标签名称不能为空', got %q", result)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_TagDelete_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["tag_delete"]

	// Valid args
	result, err := handler(context.Background(), `{"tag_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing tag_id
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_UnknownTool(t *testing.T) {
	svc := testService()
	// Verify that unknown tool names return an appropriate error via the handler map
	_, exists := svc.Handlers["nonexistent_tool"]
	if exists {
		t.Fatal("handler for nonexistent tool should not exist")
	}
}

func Test_ModuleList_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["module_list"]

	// Even empty args should work (no params required)
	result, err := handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}
}

func Test_ModuleGet_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["module_get"]

	// Valid args
	result, err := handler(context.Background(), `{"module_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing module_id
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_ModuleCreate_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["module_create"]

	// Valid args
	result, err := handler(context.Background(), `{"title":"News","table_name":"news"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing title
	result, err = handler(context.Background(), `{"table_name":"news"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：模型名称不能为空" {
		t.Fatalf("expected '模型名称不能为空', got %q", result)
	}

	// Missing table_name
	result, err = handler(context.Background(), `{"title":"News"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：表名不能为空" {
		t.Fatalf("expected '表名不能为空', got %q", result)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_ModuleDelete_ParseArgs(t *testing.T) {
	svc := testService()
	handler := svc.Handlers["module_delete"]

	// Valid args
	result, err := handler(context.Background(), `{"module_id":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "错误：站点未初始化" {
		t.Fatalf("expected '站点未初始化', got %q", result)
	}

	// Missing module_id
	result, err = handler(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Invalid JSON
	_, err = handler(context.Background(), `bad`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
