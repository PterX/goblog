package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"kandaoni.com/anqicms/request"
)

// toolHandler executes a tool given its JSON arguments and returns a text result.
type toolHandler func(ctx context.Context, argsJSON string) (string, error)

// getEinoTools returns tool definitions (schema.ToolInfo) and a name→handler map.
// The handlers use the site stored in the service.
func (svc *AiChatService) getEinoTools() ([]*schema.ToolInfo, map[string]toolHandler) {
	tools := make([]*schema.ToolInfo, 0)
	handlers := make(map[string]toolHandler)

	add := func(ti *schema.ToolInfo, fn toolHandler) {
		tools = append(tools, ti)
		handlers[ti.Name] = fn
	}

	// ---- Archive tools ----
	add(&schema.ToolInfo{
		Name: "archive_list",
		Desc: "分页获取文章列表，支持按分类、状态、关键词筛选和排序。返回文章列表和总数。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"page":        {Type: schema.Integer, Desc: "页码，从1开始，默认1"},
			"page_size":   {Type: schema.Integer, Desc: "每页数量，最大100，默认10"},
			"category_id": {Type: schema.Integer, Desc: "分类ID，筛选指定分类的文章"},
			"status":      {Type: schema.Integer, Desc: "状态筛选：0=草稿，1=已发布，2=已下架"},
			"keyword":     {Type: schema.String, Desc: "关键词搜索，匹配标题和内容"},
			"order_by":    {Type: schema.String, Desc: "排序字段：created_time, updated_time, views"},
			"order_dir":   {Type: schema.String, Desc: "排序方向：asc 升序, desc 降序"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Page       int    `json:"page"`
			PageSize   int    `json:"page_size"`
			CategoryID uint   `json:"category_id"`
			Status     int    `json:"status"`
			Keyword    string `json:"keyword"`
			OrderBy    string `json:"order_by"`
			OrderDir   string `json:"order_dir"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Page <= 0 {
			args.Page = 1
		}
		if args.PageSize <= 0 || args.PageSize > 100 {
			args.PageSize = 10
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		order := "created_time DESC"
		if args.OrderBy != "" {
			dir := "DESC"
			if args.OrderDir == "asc" {
				dir = "ASC"
			}
			order = args.OrderBy + " " + dir
		}
		archives, total, err := w.GetArchiveList(nil, order, args.Page, args.PageSize)
		if err != nil {
			return "", fmt.Errorf("查询文章列表失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 篇文章，当前第 %d 页：\n\n", total, args.Page))
		for _, a := range archives {
			b.WriteString(fmt.Sprintf("- [%d] %s (分类ID: %d)\n", a.Id, a.Title, a.CategoryId))
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_get",
		Desc: "获取单篇文章的完整详情，包括标题、内容、关键词、描述等。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"archive_id": {Type: schema.Integer, Desc: "文章ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			ArchiveID int64 `json:"archive_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		archive, err := w.GetArchiveById(args.ArchiveID)
		if err != nil {
			return "", fmt.Errorf("获取文章失败: %w", err)
		}
		data, _ := w.GetArchiveDataById(args.ArchiveID)
		content := ""
		if data != nil {
			content = data.Content
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("标题: %s\n", archive.Title))
		b.WriteString(fmt.Sprintf("ID: %d\n", archive.Id))
		b.WriteString(fmt.Sprintf("分类ID: %d\n", archive.CategoryId))
		b.WriteString(fmt.Sprintf("关键词: %s\n", archive.Keywords))
		b.WriteString(fmt.Sprintf("描述: %s\n", archive.Description))
		b.WriteString(fmt.Sprintf("创建时间: %s\n", time.Unix(archive.CreatedTime, 0).Format("2006-01-02 15:04:05")))
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		b.WriteString(fmt.Sprintf("内容预览: %s\n", content))
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_create",
		Desc: "创建新文章。必填字段：title（标题）、content（内容）、category_id（分类ID）。创建成功返回文章ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":       {Type: schema.String, Desc: "文章标题", Required: true},
			"content":     {Type: schema.String, Desc: "文章内容，支持Markdown格式", Required: true},
			"category_id": {Type: schema.Integer, Desc: "分类ID", Required: true},
			"keywords":    {Type: schema.String, Desc: "关键词，多个用逗号分隔"},
			"description": {Type: schema.String, Desc: "文章摘要/描述"},
			"cover":       {Type: schema.String, Desc: "封面图片URL"},
			"author":      {Type: schema.String, Desc: "作者"},
			"status":      {Type: schema.Integer, Desc: "状态：0=草稿，1=已发布，默认0（草稿）"},
			"tag_ids":     {Type: schema.String, Desc: "标签ID列表，JSON数组格式如 [1,2,3]"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title       string `json:"title"`
			Content     string `json:"content"`
			CategoryID  uint   `json:"category_id"`
			Keywords    string `json:"keywords"`
			Description string `json:"description"`
			Cover       string `json:"cover"`
			Author      string `json:"author"`
			Status      int    `json:"status"`
			TagIDs      string `json:"tag_ids"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" {
			return "错误：文章标题不能为空", nil
		}
		if args.Content == "" {
			return "错误：文章内容不能为空", nil
		}
		if args.CategoryID == 0 {
			return "错误：请指定分类ID", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		req := &request.Archive{
			Title:       args.Title,
			Content:     args.Content,
			CategoryId:  args.CategoryID,
			Keywords:    args.Keywords,
			Description: args.Description,
		}
		if args.Status == 1 {
			req.Draft = false
		} else {
			req.Draft = true
		}
		if args.Cover != "" {
			req.Images = []string{args.Cover}
		}
		archive, err := w.SaveArchive(req)
		if err != nil {
			return "", fmt.Errorf("创建文章失败: %w", err)
		}
		return fmt.Sprintf("文章创建成功！ID: %d，标题: %s", archive.Id, archive.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_delete",
		Desc: "删除文章（软删除）。需要传入文章ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"archive_id": {Type: schema.Integer, Desc: "要删除的文章ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			ArchiveID int64 `json:"archive_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		archive, err := w.GetArchiveById(args.ArchiveID)
		if err != nil {
			return "", fmt.Errorf("获取文章失败: %w", err)
		}
		if err := w.DeleteArchive(archive); err != nil {
			return "", fmt.Errorf("删除文章失败: %w", err)
		}
		return fmt.Sprintf("文章 [%d] %s 已成功删除", archive.Id, archive.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_publish",
		Desc: "发布或下架文章。传入archive_id和status（1=发布，0=下架）。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"archive_id": {Type: schema.Integer, Desc: "文章ID", Required: true},
			"status":     {Type: schema.Integer, Desc: "状态：1=发布，0=下架", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			ArchiveID int64 `json:"archive_id"`
			Status    uint  `json:"status"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		archive, err := w.GetArchiveById(args.ArchiveID)
		if err != nil {
			return "", fmt.Errorf("获取文章失败: %w", err)
		}
		updateReq := &request.ArchivesUpdateRequest{
			Ids:    []int64{args.ArchiveID},
			Status: args.Status,
		}
		if err := w.UpdateArchiveStatus(updateReq); err != nil {
			return "", fmt.Errorf("更新文章状态失败: %w", err)
		}
		statusStr := "已发布"
		if args.Status == 0 {
			statusStr = "已下架"
		}
		return fmt.Sprintf("文章 [%d] %s %s", archive.Id, archive.Title, statusStr), nil
	})

	// ---- Module tools ----
	add(&schema.ToolInfo{
		Name:        "module_list",
		Desc:        "获取自定义模型列表，返回所有模型的ID、名称、表名和URL别名等信息。",
		ParamsOneOf: schema.NewParamsOneOfByParams(nil),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		modules, err := w.GetModules()
		if err != nil {
			return "", fmt.Errorf("获取模型列表失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 个自定义模型：\n\n", len(modules)))
		for _, m := range modules {
			b.WriteString(fmt.Sprintf("- [%d] %s (表: %s, URL别名: %s)", m.Id, m.Title, m.TableName, m.UrlToken))
			if m.IsSystem == 1 {
				b.WriteString(" [系统]")
			}
			b.WriteString("\n")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "module_get",
		Desc: "获取单个自定义模型的详细信息，包括模型字段配置。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"module_id": {Type: schema.Integer, Desc: "模型ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			ModuleID uint `json:"module_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		mod, err := w.GetModuleById(args.ModuleID)
		if err != nil {
			return "", fmt.Errorf("获取模型失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("模型信息：\nID: %d\n名称: %s\n表名: %s\nURL别名: %s\n", mod.Id, mod.Title, mod.TableName, mod.UrlToken))
		b.WriteString(fmt.Sprintf("关键词: %s\n描述: %s\n", mod.Keywords, mod.Description))
		b.WriteString(fmt.Sprintf("标题字段名: %s\n", mod.TitleName))
		sys := "否"
		if mod.IsSystem == 1 {
			sys = "是"
		}
		b.WriteString(fmt.Sprintf("系统模型: %s\n", sys))
		statusStr := "启用"
		if mod.Status != 1 {
			statusStr = "禁用"
		}
		b.WriteString(fmt.Sprintf("状态: %s\n", statusStr))
		if len(mod.Fields) > 0 {
			b.WriteString(fmt.Sprintf("自定义字段: %d 个\n", len(mod.Fields)))
			for _, f := range mod.Fields {
				b.WriteString(fmt.Sprintf("  - %s (%s, %s)\n", f.FieldName, f.Type, f.Name))
			}
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "module_create",
		Desc: "创建新自定义模型。必填字段：title（模型名称）、table_name（表名）。创建成功返回模型ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":       {Type: schema.String, Desc: "模型名称", Required: true},
			"table_name":  {Type: schema.String, Desc: "数据库表名（英文小写）", Required: true},
			"url_token":   {Type: schema.String, Desc: "URL别名"},
			"name":        {Type: schema.String, Desc: "模型标识"},
			"keywords":    {Type: schema.String, Desc: "关键词"},
			"description": {Type: schema.String, Desc: "描述"},
			"title_name":  {Type: schema.String, Desc: "标题字段显示名称"},
			"status":      {Type: schema.Integer, Desc: "状态：1=启用，0=禁用，默认1"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title       string `json:"title"`
			TableName   string `json:"table_name"`
			UrlToken    string `json:"url_token"`
			Name        string `json:"name"`
			Keywords    string `json:"keywords"`
			Description string `json:"description"`
			TitleName   string `json:"title_name"`
			Status      uint   `json:"status"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" {
			return "错误：模型名称不能为空", nil
		}
		if args.TableName == "" {
			return "错误：表名不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		if args.Status == 0 {
			args.Status = 1
		}
		req := &request.ModuleRequest{
			Title:       args.Title,
			TableName:   args.TableName,
			UrlToken:    args.UrlToken,
			Name:        args.Name,
			Keywords:    args.Keywords,
			Description: args.Description,
			TitleName:   args.TitleName,
			Status:      args.Status,
		}
		mod, err := w.SaveModule(req)
		if err != nil {
			return "", fmt.Errorf("创建模型失败: %w", err)
		}
		return fmt.Sprintf("模型创建成功！ID: %d, 名称: %s, 表名: %s", mod.Id, mod.Title, mod.TableName), nil
	})

	add(&schema.ToolInfo{
		Name: "module_delete",
		Desc: "删除自定义模型。需要传入模型ID。注意：删除模型会同时删除该模型的所有数据。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"module_id": {Type: schema.Integer, Desc: "要删除的模型ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			ModuleID uint `json:"module_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		mod, err := w.GetModuleById(args.ModuleID)
		if err != nil {
			return "", fmt.Errorf("获取模型失败: %w", err)
		}
		if mod.IsSystem == 1 {
			return "错误：系统模型不允许删除", nil
		}
		if err := w.DeleteModule(mod); err != nil {
			return "", fmt.Errorf("删除模型失败: %w", err)
		}
		return fmt.Sprintf("模型 [%d] %s 已成功删除", mod.Id, mod.Title), nil
	})

	// ---- Category tools ----
	add(&schema.ToolInfo{
		Name:        "category_list",
		Desc:        "获取分类列表，返回所有分类的ID、标题和层级结构。",
		ParamsOneOf: schema.NewParamsOneOfByParams(nil),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		categories, err := w.GetCategories(nil, 0, 1)
		if err != nil {
			return "", fmt.Errorf("获取分类列表失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 个分类：\n\n", len(categories)))
		for _, c := range categories {
			b.WriteString(fmt.Sprintf("- [%d] %s", c.Id, c.Title))
			if c.ParentId > 0 {
				b.WriteString(fmt.Sprintf(" (父分类ID: %d)", c.ParentId))
			}
			b.WriteString("\n")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "category_get",
		Desc: "获取单个分类的详细信息。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"category_id": {Type: schema.Integer, Desc: "分类ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			CategoryID uint `json:"category_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		cat, err := w.GetCategoryById(args.CategoryID)
		if err != nil {
			return "", fmt.Errorf("获取分类失败: %w", err)
		}
		return fmt.Sprintf("分类信息：\nID: %d\n标题: %s\n父分类ID: %d\n描述: %s\nURL别名: %s",
			cat.Id, cat.Title, cat.ParentId, cat.Description, cat.UrlToken), nil
	})

	add(&schema.ToolInfo{
		Name: "category_create",
		Desc: "创建新分类。必填字段：title（分类名称）。创建成功返回分类ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":       {Type: schema.String, Desc: "分类名称", Required: true},
			"parent_id":   {Type: schema.Integer, Desc: "父分类ID，默认为0（顶级分类）"},
			"description": {Type: schema.String, Desc: "分类描述"},
			"keywords":    {Type: schema.String, Desc: "分类关键词"},
			"type":        {Type: schema.Integer, Desc: "类型：1=文档分类，2=页面分类，默认1"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title       string `json:"title"`
			ParentID    uint   `json:"parent_id"`
			Description string `json:"description"`
			Keywords    string `json:"keywords"`
			Type        uint   `json:"type"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" {
			return "错误：分类名称不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		if args.Type == 0 {
			args.Type = 1
		}
		req := &request.Category{
			Title:       args.Title,
			ParentId:    args.ParentID,
			Description: args.Description,
			Keywords:    args.Keywords,
			Type:        args.Type,
			Status:      1,
		}
		cat, err := w.SaveCategory(req)
		if err != nil {
			return "", fmt.Errorf("创建分类失败: %w", err)
		}
		return fmt.Sprintf("分类创建成功！ID: %d, 标题: %s", cat.Id, cat.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "category_delete",
		Desc: "删除分类。需要传入分类ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"category_id": {Type: schema.Integer, Desc: "要删除的分类ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			CategoryID uint `json:"category_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		cat, err := w.GetCategoryById(args.CategoryID)
		if err != nil {
			return "", fmt.Errorf("获取分类失败: %w", err)
		}
		err = w.DB.Unscoped().Delete(cat).Error
		if err != nil {
			return "", fmt.Errorf("删除分类失败: %w", err)
		}
		w.DeleteCacheCategories()
		return fmt.Sprintf("分类 [%d] %s 已成功删除", cat.Id, cat.Title), nil
	})

	// ---- Tag tools ----
	add(&schema.ToolInfo{
		Name:        "tag_list",
		Desc:        "获取标签列表，返回所有标签的ID和名称。",
		ParamsOneOf: schema.NewParamsOneOfByParams(nil),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		tags, total, err := w.GetTagList(0, "", nil, "", 1, 100, 0, "id ASC")
		if err != nil {
			return "", fmt.Errorf("获取标签列表失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 个标签：\n\n", total))
		for _, t := range tags {
			b.WriteString(fmt.Sprintf("- [%d] %s", t.Id, t.Title))
			if t.FirstLetter != "" {
				b.WriteString(fmt.Sprintf(" (%s)", t.FirstLetter))
			}
			b.WriteString("\n")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "tag_get",
		Desc: "获取单个标签的详细信息。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"tag_id": {Type: schema.Integer, Desc: "标签ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			TagID uint `json:"tag_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		tag, err := w.GetTagById(args.TagID)
		if err != nil {
			return "", fmt.Errorf("获取标签失败: %w", err)
		}
		return fmt.Sprintf("标签信息：\nID: %d\n标题: %s\n首字母: %s\n描述: %s",
			tag.Id, tag.Title, tag.FirstLetter, tag.Description), nil
	})

	add(&schema.ToolInfo{
		Name: "tag_create",
		Desc: "创建新标签。必填字段：title（标签名称）。创建成功返回标签ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":       {Type: schema.String, Desc: "标签名称", Required: true},
			"description": {Type: schema.String, Desc: "标签描述"},
			"category_id": {Type: schema.Integer, Desc: "标签分类ID"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			CategoryID  uint   `json:"category_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" {
			return "错误：标签名称不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		tag, err := w.SaveTag(&request.PluginTag{
			Title:       args.Title,
			Description: args.Description,
			CategoryId:  args.CategoryID,
		})
		if err != nil {
			return "", fmt.Errorf("创建标签失败: %w", err)
		}
		return fmt.Sprintf("标签创建成功！ID: %d, 名称: %s", tag.Id, tag.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "tag_delete",
		Desc: "删除标签。需要传入标签ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"tag_id": {Type: schema.Integer, Desc: "要删除的标签ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			TagID uint `json:"tag_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		tag, err := w.GetTagById(args.TagID)
		if err != nil {
			return "", fmt.Errorf("获取标签失败: %w", err)
		}
		if err := w.DeleteTag(args.TagID); err != nil {
			return "", fmt.Errorf("删除标签失败: %w", err)
		}
		return fmt.Sprintf("标签 [%d] %s 已成功删除", tag.Id, tag.Title), nil
	})

	return tools, handlers
}
