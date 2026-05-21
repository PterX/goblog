package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
	"kandaoni.com/anqicms/config"
	"kandaoni.com/anqicms/model"
	"kandaoni.com/anqicms/provider/fulltext"
	"kandaoni.com/anqicms/request"
	"kandaoni.com/anqicms/response"
)

type ArgId struct {
	Id int64 `json:"id"`
}

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
		Desc: "分页获取文档列表，支持按分类、状态、关键词筛选和排序。返回文档列表和总数。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"page":        {Type: schema.Integer, Desc: "页码，从1开始，默认1"},
			"page_size":   {Type: schema.Integer, Desc: "每页数量，最大100，默认10"},
			"category_id": {Type: schema.Integer, Desc: "分类ID，筛选指定分类的文档"},
			"module_id":   {Type: schema.Integer, Desc: "模型ID，筛选指定模型的文档"},
			"parent_id":   {Type: schema.Integer, Desc: "父文档ID，筛选指定父文档的下级文档"},
			"status":      {Type: schema.String, Desc: "状态筛选：draft=草稿，ok=已发布，plan=定时发布，默认为ok"},
			"keyword":     {Type: schema.String, Desc: "关键词搜索，匹配标题和内容"},
			"flag":        {Type: schema.String, Desc: "文档属性筛选，h=头条，c=推荐，f=幻灯，a=特荐，s=滚动，b=加粗，p=图片，j=跳转"},
			"order_by":    {Type: schema.String, Desc: "排序字段：created_time, updated_time, views"},
			"order_dir":   {Type: schema.String, Desc: "排序方向：asc 升序, desc 降序"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Page       int    `json:"page"`
			PageSize   int    `json:"page_size"`
			CategoryID uint   `json:"category_id"`
			ModuleID   uint   `json:"module_id"`
			ParentID   uint   `json:"parent_id"`
			Flag       string `json:"flag"`
			Status     string `json:"status"`
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
		isDraft := 0
		if args.Status == "draft" || args.Status == "plan" {
			isDraft = 1
		}
		offset := (args.Page - 1) * args.PageSize
		var fulltextSearch bool
		var fulltextTotal int64
		archives, total, err := w.GetArchiveList(func(tx *gorm.DB) *gorm.DB {
			if args.CategoryID > 0 {
				tx = tx.Where("category_id = ?", args.CategoryID)
			}
			if args.ModuleID > 0 {
				tx = tx.Where("module_id = ?", args.ModuleID)
			}
			if args.ParentID > 0 {
				tx = tx.Where("parent_id = ?", args.ParentID)
			}
			if args.Status != "" {
				if args.Status == "draft" {
					tx = tx.Where("status = ?", config.ContentStatusDraft)
				} else if args.Status == "plan" {
					tx = tx.Where("status = ?", config.ContentStatusPlan)
				}
			}
			if args.Keyword != "" {
				var ids []int64
				// 如果开启了全文索引，则尝试使用全文索引搜索，status = "ok" 时有效
				if args.Status == "ok" {
					var tmpDocs []fulltext.TinyArchive
					var err2 error
					tmpDocs, fulltextTotal, err2 = svc.site.Search(args.Keyword, args.ModuleID, args.Page, args.PageSize)
					if err2 == nil {
						fulltextSearch = true
						// 只保留文档
						for _, doc := range tmpDocs {
							if doc.Type == fulltext.ArchiveType {
								ids = append(ids, doc.Id)
							}
						}
						if len(tmpDocs) == 0 || len(ids) == 0 {
							ids = append(ids, 0)
						}
						offset = 0
					}
				}
				if fulltextSearch == true {
					// 使用了全文索引，拿到了ID
					tx = tx.Where("archives.`id` IN(?)", ids)
				} else {
					// 如果文章数量达到10万，则只能匹配开头，否则就模糊搜索
					var allArchives int64
					if args.Status == "ok" {
						allArchives = svc.site.GetExplainCount("SELECT id FROM archives")
					} else {
						allArchives = svc.site.GetExplainCount("SELECT id FROM archive_drafts")
					}
					if allArchives > 100000 {
						tx = tx.Where("`title` like ?", args.Keyword+"%")
					} else {
						tx = tx.Where("`title` like ?", "%"+args.Keyword+"%")
					}
				}
			}
			if args.Flag != "" {
				tx = tx.Joins("INNER JOIN archive_flags ON archives.id = archive_flags.archive_id and archive_flags.flag = ?", args.Flag)
			}
			return tx
		}, order, args.Page, args.PageSize, offset, isDraft)
		if err != nil {
			return "", fmt.Errorf("查询文档列表失败: %w", err)
		}
		if fulltextSearch {
			total = fulltextTotal
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 篇文档，当前第 %d 页：\n\n", total, args.Page))
		for _, a := range archives {
			b.WriteString(fmt.Sprintf("- [%d] %s (分类ID: %d)\n", a.Id, a.Title, a.CategoryId))
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_get",
		Desc: "获取单篇文档的完整详情，包括标题、内容、关键词、描述等。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "文档ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		archive, err := w.GetArchiveById(args.Id)
		if err != nil {
			archiveDraft, err := w.GetArchiveDraftById(args.Id)
			if err != nil {
				return "", fmt.Errorf("获取文档失败: %w", err)
			}
			archive = &archiveDraft.Archive
		}
		data, _ := w.GetArchiveDataById(args.Id)
		content := ""
		if data != nil {
			content = data.Content
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("标题: %s\n", archive.Title))
		b.WriteString(fmt.Sprintf("ID: %d\n", archive.Id))
		b.WriteString(fmt.Sprintf("模型ID: %d\n", archive.ModuleId))
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
		Desc: "创建新文档。必填字段：title（标题）、content（内容）、category_id（分类ID）。创建成功返回文档ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":       {Type: schema.String, Desc: "文档标题", Required: true},
			"content":     {Type: schema.String, Desc: "文档内容，支持Markdown格式", Required: true},
			"category_id": {Type: schema.Integer, Desc: "分类ID", Required: true},
			"keywords":    {Type: schema.String, Desc: "关键词，多个用逗号分隔"},
			"description": {Type: schema.String, Desc: "文档摘要/描述"},
			"logo":        {Type: schema.String, Desc: "封面图片URL"},
			"status":      {Type: schema.Integer, Desc: "状态：0=草稿，1=已发布，默认0（草稿）"},
			"tags":        {Type: schema.Array, Desc: "标签列表，JSON数组格式如 [\"tag1\",\"tag2\"]"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title       string   `json:"title"`
			Content     string   `json:"content"`
			CategoryID  uint     `json:"category_id"`
			Keywords    string   `json:"keywords"`
			Description string   `json:"description"`
			Logo        string   `json:"logo"`
			Status      int      `json:"status"`
			Tags        []string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" {
			return "错误：文档标题不能为空", nil
		}
		if args.Content == "" {
			return "错误：文档内容不能为空", nil
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
			Tags:        args.Tags,
		}
		if args.Logo != "" {
			req.Images = []string{args.Logo}
		}
		if args.Status == 1 {
			req.Draft = false
		} else {
			req.Draft = true
		}
		archive, err := w.SaveArchive(req)
		if err != nil {
			return "", fmt.Errorf("创建文档失败: %w", err)
		}
		return fmt.Sprintf("文档创建成功！ID: %d，标题: %s", archive.Id, archive.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_delete",
		Desc: "删除文档（软删除）。需要传入文档ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "要删除的文档ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		archive, err := w.GetArchiveById(args.Id)
		if err != nil {
			return "", fmt.Errorf("获取文档失败: %w", err)
		}
		if err := w.DeleteArchive(archive); err != nil {
			return "", fmt.Errorf("删除文档失败: %w", err)
		}
		return fmt.Sprintf("文档 [%d] %s 已成功删除", archive.Id, archive.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_publish",
		Desc: "发布或下架文档。传入archive_id和status（1=发布，0=下架）。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id":     {Type: schema.Integer, Desc: "文档ID", Required: true},
			"status": {Type: schema.Integer, Desc: "状态：1=发布，0=下架", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			ID     int64 `json:"id"`
			Status uint  `json:"status"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		archive, err := w.GetArchiveById(args.ID)
		if err != nil {
			return "", fmt.Errorf("获取文档失败: %w", err)
		}
		updateReq := &request.ArchivesUpdateRequest{
			Ids:    []int64{args.ID},
			Status: args.Status,
		}
		if err := w.UpdateArchiveStatus(updateReq); err != nil {
			return "", fmt.Errorf("更新文档状态失败: %w", err)
		}
		statusStr := "已发布"
		if args.Status == 0 {
			statusStr = "已下架"
		}
		return fmt.Sprintf("文档 [%d] %s %s", archive.Id, archive.Title, statusStr), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_update",
		Desc: "编辑已有文档。需要传入文档ID。仅传入的字段会被更新，未传入的字段保持不变。支持修改标题、内容、分类、关键词、描述、封面图、标签。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id":          {Type: schema.Integer, Desc: "文档ID", Required: true},
			"title":       {Type: schema.String, Desc: "文档标题"},
			"content":     {Type: schema.String, Desc: "文档内容，支持Markdown格式"},
			"category_id": {Type: schema.Integer, Desc: "分类ID"},
			"keywords":    {Type: schema.String, Desc: "关键词，多个用逗号分隔"},
			"description": {Type: schema.String, Desc: "文档摘要/描述"},
			"logo":        {Type: schema.String, Desc: "封面图片URL"},
			"status":      {Type: schema.Integer, Desc: "状态：0=草稿，1=已发布"},
			"tags":        {Type: schema.Array, Desc: "标签列表，JSON数组格式如 [\"tag1\",\"tag2\"]"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			ID          int64    `json:"id"`
			Title       string   `json:"title"`
			Content     string   `json:"content"`
			CategoryID  uint     `json:"category_id"`
			Keywords    string   `json:"keywords"`
			Description string   `json:"description"`
			Logo        string   `json:"logo"`
			Status      int      `json:"status"`
			Tags        []string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.ID == 0 {
			return "错误：请传入文档ID", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		// 获取已有文档
		archive, err := w.GetArchiveById(args.ID)
		if err != nil {
			return "", fmt.Errorf("获取文档失败: %w", err)
		}
		// 只覆盖传入的字段
		req := &request.Archive{
			Id: args.ID,
		}
		if args.Title != "" {
			req.Title = args.Title
		} else {
			req.Title = archive.Title
		}
		if args.Content != "" {
			req.Content = args.Content
		}
		if args.CategoryID > 0 {
			req.CategoryId = args.CategoryID
		} else {
			req.CategoryId = archive.CategoryId
		}
		if args.Keywords != "" {
			req.Keywords = args.Keywords
		} else {
			req.Keywords = archive.Keywords
		}
		if args.Description != "" {
			req.Description = args.Description
		} else {
			req.Description = archive.Description
		}
		if args.Logo != "" {
			req.Images = []string{args.Logo}
		}
		if args.Status == 1 {
			req.Draft = false
		} else if args.Status == 0 {
			req.Draft = true
		}
		if len(args.Tags) > 0 {
			req.Tags = args.Tags
		}
		archive, err = w.SaveArchive(req)
		if err != nil {
			return "", fmt.Errorf("更新文档失败: %w", err)
		}
		return fmt.Sprintf("文档 [%d] %s 更新成功！", archive.Id, archive.Title), nil
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
			b.WriteString(fmt.Sprintf("- [%d] %s (表: %s, URL别名: %s)", m.Id, m.Name, m.TableName, m.UrlToken))
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
			"id": {Type: schema.Integer, Desc: "模型ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		mod, err := w.GetModuleById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取模型失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("模型信息：\nID: %d\n名称: %s\n表名: %s\nURL别名: %s\n", mod.Id, mod.Title, mod.TableName, mod.UrlToken))
		b.WriteString(fmt.Sprintf("关键词: %s\n描述: %s\n", mod.Keywords, mod.Description))
		b.WriteString(fmt.Sprintf("标题字段名: %s\n", mod.Title))
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
		args.TableName = strings.ToLower(args.TableName)
		if args.UrlToken == "" {
			args.UrlToken = args.TableName
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
			"id": {Type: schema.Integer, Desc: "要删除的模型ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		mod, err := w.GetModuleById(uint(args.Id))
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
		Name: "category_list",
		Desc: "获取分类列表，返回所有分类的ID、标题和层级结构。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "要获取分类列表的模型ID", Required: false},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		categories, err := w.GetCategories(func(tx *gorm.DB) *gorm.DB {
			if args.Id > 0 {
				tx = tx.Where("module_id = ?", args.Id)
			}
			return tx.Where("type = ?", config.CategoryTypeArchive).Order("module_id asc,sort asc")
		}, 0, 1)
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
			"id": {Type: schema.Integer, Desc: "分类ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		cat, err := w.GetCategoryById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取分类失败: %w", err)
		}
		return fmt.Sprintf("分类信息：\nID: %d\n标题: %s\n父分类ID: %d\n描述: %s\nURL别名: %s",
			cat.Id, cat.Title, cat.ParentId, cat.Description, cat.UrlToken), nil
	})

	add(&schema.ToolInfo{
		Name: "category_create",
		Desc: "创建新分类。必填字段：title（分类名称）、module_id（模型ID）。创建成功返回分类ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":       {Type: schema.String, Desc: "分类名称", Required: true},
			"parent_id":   {Type: schema.Integer, Desc: "父分类ID，默认为0（顶级分类）"},
			"module_id":   {Type: schema.Integer, Desc: "所属模型ID，默认为1（文档分类）"},
			"description": {Type: schema.String, Desc: "分类描述"},
			"keywords":    {Type: schema.String, Desc: "分类关键词"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title       string `json:"title"`
			ParentID    uint   `json:"parent_id"`
			Description string `json:"description"`
			Keywords    string `json:"keywords"`
			ModuleID    uint   `json:"module_id"`
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
		req := &request.Category{
			Title:       args.Title,
			ParentId:    args.ParentID,
			Description: args.Description,
			Keywords:    args.Keywords,
			ModuleId:    args.ModuleID,
			Type:        config.CategoryTypeArchive,
			Status:      1,
		}
		// 检查是否重复了
		exist, err := w.GetCategoryByTitle(req.Title)
		if err == nil && exist.Id != 0 {
			return fmt.Sprintf("错误：分类名称重复, ID: %d", exist.Id), nil
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
			"id": {Type: schema.Integer, Desc: "要删除的分类ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		cat, err := w.GetCategoryById(uint(args.Id))
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

	// ---- Page Tools ----
	add(&schema.ToolInfo{
		Name:        "page_list",
		Desc:        "获取页面列表，返回所有页面的ID和标题。",
		ParamsOneOf: schema.NewParamsOneOfByParams(nil),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		categories, err := w.GetCategories(func(tx *gorm.DB) *gorm.DB {
			return tx.Where("type = ?", config.CategoryTypePage).Order("sort asc")
		}, 0, 1)
		if err != nil {
			return "", fmt.Errorf("获取页面列表失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 个页面：\n\n", len(categories)))
		for _, c := range categories {
			b.WriteString(fmt.Sprintf("- [%d] %s", c.Id, c.Title))
			b.WriteString("\n")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "page_get",
		Desc: "获取单个页面的详细信息。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "页面ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		cat, err := w.GetCategoryById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取页面失败: %w", err)
		}
		return fmt.Sprintf("页面信息：\nID: %d\n标题: %s\n描述: %s\nURL别名: %s",
			cat.Id, cat.Title, cat.Description, cat.UrlToken), nil
	})

	add(&schema.ToolInfo{
		Name: "page_create",
		Desc: "创建新页面。必填字段：title（页面名称）。创建成功返回页面ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":       {Type: schema.String, Desc: "页面名称", Required: true},
			"description": {Type: schema.String, Desc: "页面描述"},
			"keywords":    {Type: schema.String, Desc: "页面关键词"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Keywords    string `json:"keywords"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" {
			return "错误：页面名称不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		req := &request.Category{
			Title:       args.Title,
			Description: args.Description,
			Keywords:    args.Keywords,
			Type:        config.CategoryTypePage,
			Status:      1,
		}
		// 检查是否重复了
		exist, err := w.GetCategoryByTitle(req.Title)
		if err == nil && exist.Id != 0 {
			return fmt.Sprintf("错误：页面名称重复, ID: %d", exist.Id), nil
		}
		cat, err := w.SaveCategory(req)
		if err != nil {
			return "", fmt.Errorf("创建页面失败: %w", err)
		}
		return fmt.Sprintf("页面创建成功！ID: %d, 标题: %s", cat.Id, cat.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "page_delete",
		Desc: "删除页面。需要传入页面ID。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "要删除的页面ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		cat, err := w.GetCategoryById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取页面失败: %w", err)
		}
		err = w.DB.Unscoped().Delete(cat).Error
		if err != nil {
			return "", fmt.Errorf("删除页面失败: %w", err)
		}
		w.DeleteCacheCategories()
		return fmt.Sprintf("页面 [%d] %s 已成功删除", cat.Id, cat.Title), nil
	})

	// ---- Tag tools ----
	add(&schema.ToolInfo{
		Name: "tag_list",
		Desc: "获取标签列表，返回所有标签的ID和名称。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"page":        {Type: schema.Integer, Desc: "页码，从1开始，默认1"},
			"page_size":   {Type: schema.Integer, Desc: "每页数量，最大100，默认10"},
			"category_id": {Type: schema.Integer, Desc: "分类ID，筛选指定分类的文档"},
			"archive_id":  {Type: schema.Integer, Desc: "文档ID，筛选指定文档的标签"},
			"keyword":     {Type: schema.String, Desc: "关键词搜索，匹配标题和内容"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Page       int    `json:"page"`
			PageSize   int    `json:"page_size"`
			CategoryId uint   `json:"category_id"`
			ArchiveId  int64  `json:"archive_id"`
			Keyword    string `json:"keyword"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		if args.Page <= 0 {
			args.Page = 1
		}
		if args.PageSize <= 0 || args.PageSize > 100 {
			args.PageSize = 10
		}
		var catIds []uint
		if args.CategoryId > 0 {
			catIds = append(catIds, args.CategoryId)
		}
		tags, total, err := w.GetTagList(args.ArchiveId, args.Keyword, catIds, "", args.Page, args.PageSize, 0, "id ASC")
		if err != nil {
			return "", fmt.Errorf("获取标签列表失败: %w", err)
		}
		log.Printf("tags = %#v", tags)
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
			"id": {Type: schema.Integer, Desc: "标签ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		tag, err := w.GetTagById(uint(args.Id))
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
			"id": {Type: schema.Integer, Desc: "要删除的标签ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		tag, err := w.GetTagById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取标签失败: %w", err)
		}
		if err := w.DeleteTag(uint(args.Id)); err != nil {
			return "", fmt.Errorf("删除标签失败: %w", err)
		}
		return fmt.Sprintf("标签 [%d] %s 已成功删除", tag.Id, tag.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "archive_tag_update",
		Desc: "将标签绑定到文档。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"tags": {Type: schema.Array, Desc: "标签列表，JSON数组格式如 [\"tag1\",\"tag2\"]", Required: true},
			"ids":  {Type: schema.Array, Desc: "文档ID列表，JSON数组格式如 [1,2,3]", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Tags []string `json:"tags"`
			Ids  []int64  `json:"ids"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if len(args.Tags) == 0 || len(args.Ids) == 0 {
			return "错误：标签列表不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		err := w.UpdateArchiveTags(&request.ArchivesUpdateRequest{
			Ids:  args.Ids,
			Tags: args.Tags,
		})
		if err != nil {
			return "", fmt.Errorf("更新文档标签失败: %w", err)
		}
		return "文档标签更新成功", nil
	})

	// ---- Template tools ----
	add(&schema.ToolInfo{
		Name: "template_get_info",
		Desc: "获取当前启用的模板信息。返回模板名称、类型、模板路径、以及所有模板文件列表。用于了解当前站点使用哪个模板。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		designList := w.GetDesignList()
		typeName := "自适应"
		if w.System.TemplateType == config.TemplateTypeAdapt {
			typeName = "代码适配"
		} else if w.System.TemplateType == config.TemplateTypeSeparate {
			typeName = "电脑+手机"
		}
		var info = ""
		info += fmt.Sprintf("当前模板：%s\n", w.System.TemplateName)
		info += fmt.Sprintf("模板类型：%s\n", typeName)
		info += fmt.Sprintf("模板路径：%s\n", w.RootPath+"template/"+w.System.TemplateName+"/")
		info += fmt.Sprintf("可用模板数量：%d\n\n", len(designList))
		info += "所有可用模板列表：\n"
		for _, d := range designList {
			status := "未启用"
			if d.Status == 1 {
				status = "当前启用"
			}
			info += fmt.Sprintf("  - %s (%s) 版本: %s 状态: %s\n", d.Name, d.Package, d.Version, status)
		}
		// Show current template's files
		if w.System.TemplateName != "" {
			// Show template files
			tplFiles, err := w.GetDesignTemplateFiles(w.System.TemplateName)
			if err == nil && len(tplFiles) > 0 {
				info += fmt.Sprintf("\n当前模板文件列表（%d个）：\n", len(tplFiles))
				for _, f := range tplFiles {
					info += fmt.Sprintf("  - template/%s/%s\n", w.System.TemplateName, f.Path)
				}
			}
			// Show static asset files (CSS/JS/images)
			designInfo, err := w.GetDesignInfo(w.System.TemplateName, false)
			if err == nil && len(designInfo.StaticFiles) > 0 {
				info += fmt.Sprintf("\n静态资源文件列表（%d个）：\n", len(designInfo.StaticFiles))
				for _, f := range designInfo.StaticFiles {
					info += fmt.Sprintf("  - static/%s/%s\n", w.System.TemplateName, f.Path)
				}
			}
		}
		return info, nil
	})

	add(&schema.ToolInfo{
		Name: "template_get_file",
		Desc: "获取模板文件的具体内容和路径。需要传入模板包名和文件相对路径。例如：package='default' path='index.html'",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"package": {Type: schema.String, Desc: "模板包名，如 default", Required: true},
			"path":    {Type: schema.String, Desc: "模板文件相对路径，如 index.html 或 category/list.html", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Package string `json:"package"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Package == "" || args.Path == "" {
			return "错误：package 和 path 不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		// Use the design file detail method to get file content
		designFile, err := w.GetDesignTplFileDetail(args.Package, response.DesignFile{Path: args.Path})
		if err != nil {
			return "", fmt.Errorf("获取模板文件失败: %w", err)
		}
		if designFile == nil || designFile.Content == "" {
			return fmt.Sprintf("模板文件 template/%s/%s 不存在或内容为空", args.Package, args.Path), nil
		}
		result := fmt.Sprintf("文件路径：template/%s/%s\n文件大小：%d bytes\n最后修改：%d\n\n内容：\n%s",
			args.Package, args.Path, designFile.Size, designFile.LastMod, designFile.Content)
		return result, nil
	})

	add(&schema.ToolInfo{
		Name: "template_modify_file",
		Desc: "修改模板文件的内容。修改后需要通过 template_reload 工具重载模板才能生效。需要传入模板包名、文件相对路径和新的文件内容。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"package": {Type: schema.String, Desc: "模板包名，如 default", Required: true},
			"path":    {Type: schema.String, Desc: "模板文件相对路径，如 index.html 或 category/list.html", Required: true},
			"content": {Type: schema.String, Desc: "文件的新内容", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Package string `json:"package"`
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Package == "" || args.Path == "" {
			return "错误：package 和 path 不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		// Check template exists
		_, err := w.GetDesignInfo(args.Package, false)
		if err != nil {
			return "", fmt.Errorf("模板包 '%s' 不存在: %w", args.Package, err)
		}
		// Save the template file
		err = w.SaveDesignTplFile(request.SaveDesignFileRequest{
			Package: args.Package,
			Path:    args.Path,
			Content: args.Content,
		})
		if err != nil {
			return "", fmt.Errorf("保存模板文件失败: %w", err)
		}
		return fmt.Sprintf("模板文件 template/%s/%s 已成功修改。请使用 template_reload 工具重载模板以生效。", args.Package, args.Path), nil
	})

	add(&schema.ToolInfo{
		Name: "template_get_static",
		Desc: "获取静态资源文件（CSS/JS/字体等）的内容和路径。需要传入模板包名和文件相对路径。例如：package='default' path='css/style.css'",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"package": {Type: schema.String, Desc: "模板包名，如 default", Required: true},
			"path":    {Type: schema.String, Desc: "静态文件相对路径，如 css/style.css 或 js/app.js", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Package string `json:"package"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Package == "" || args.Path == "" {
			return "错误：package 和 path 不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		designFile, err := w.GetDesignStaticFileDetail(args.Package, response.DesignFile{Path: args.Path})
		if err != nil {
			return "", fmt.Errorf("获取静态文件失败: %w", err)
		}
		if designFile == nil || (designFile.Content == "" && designFile.Size == 0) {
			return fmt.Sprintf("静态文件 static/%s/%s 不存在", args.Package, args.Path), nil
		}
		// 对于二进制文件（图片等），仅返回元信息，不返回内容
		isBinary := strings.HasSuffix(args.Path, ".png") || strings.HasSuffix(args.Path, ".jpg") ||
			strings.HasSuffix(args.Path, ".jpeg") || strings.HasSuffix(args.Path, ".gif") ||
			strings.HasSuffix(args.Path, ".svg") || strings.HasSuffix(args.Path, ".ico") ||
			strings.HasSuffix(args.Path, ".webp") || strings.HasSuffix(args.Path, ".bmp") ||
			strings.HasSuffix(args.Path, ".ttf") || strings.HasSuffix(args.Path, ".woff") ||
			strings.HasSuffix(args.Path, ".woff2") || strings.HasSuffix(args.Path, ".eot") ||
			strings.HasSuffix(args.Path, ".zip") || strings.HasSuffix(args.Path, ".pdf")
		if isBinary {
			result := fmt.Sprintf("文件路径：static/%s/%s\n文件大小：%d bytes\n最后修改：%d\n\n此文件为二进制文件，内容未显示。如需修改请上传新文件。",
				args.Package, args.Path, designFile.Size, designFile.LastMod)
			return result, nil
		}
		result := fmt.Sprintf("文件路径：static/%s/%s\n文件大小：%d bytes\n最后修改：%d\n\n内容：\n%s",
			args.Package, args.Path, designFile.Size, designFile.LastMod, designFile.Content)
		return result, nil
	})

	add(&schema.ToolInfo{
		Name: "template_modify_static",
		Desc: "修改静态资源文件（CSS/JS/字体等）的内容。修改后立即生效，无需重载。支持 CSS、JS 等文本文件。需要传入模板包名、文件相对路径和新的文件内容。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"package": {Type: schema.String, Desc: "模板包名，如 default", Required: true},
			"path":    {Type: schema.String, Desc: "静态文件相对路径，如 css/style.css 或 js/app.js", Required: true},
			"content": {Type: schema.String, Desc: "文件的新内容", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Package string `json:"package"`
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Package == "" || args.Path == "" {
			return "错误：package 和 path 不能为空", nil
		}
		if args.Content == "" {
			return "错误：content 不能为空", nil
		}
		// 检查是否为文本文件（禁止修改图片等二进制文件）
		binaryExts := []string{".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".bmp",
			".ttf", ".woff", ".woff2", ".eot", ".zip", ".pdf"}
		for _, ext := range binaryExts {
			if strings.HasSuffix(strings.ToLower(args.Path), ext) {
				return fmt.Sprintf("错误：不支持修改二进制文件 %s，请通过后台界面上传", args.Path), nil
			}
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		// Check template exists
		_, err := w.GetDesignInfo(args.Package, false)
		if err != nil {
			return "", fmt.Errorf("模板包 '%s' 不存在: %w", args.Package, err)
		}
		// Save the static file
		err = w.SaveDesignStaticFile(request.SaveDesignFileRequest{
			Package: args.Package,
			Path:    args.Path,
			Content: args.Content,
		})
		if err != nil {
			return "", fmt.Errorf("保存静态文件失败: %w", err)
		}
		return fmt.Sprintf("静态文件 static/%s/%s 已成功修改，修改已立即生效。", args.Package, args.Path), nil
	})

	add(&schema.ToolInfo{
		Name: "template_reload",
		Desc: "重新加载模板。在修改了模板文件内容或切换模板后，需要调用此工具使更改生效。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil {
			return "错误：站点未初始化", nil
		}
		config.RestartChan <- 0
		return fmt.Sprintf("模板重载信号已发送，模板将在1秒内重新加载。当前模板：%s", w.System.TemplateName), nil
	})

	// ---- Category Update ----
	add(&schema.ToolInfo{
		Name: "category_update",
		Desc: "更新已有分类的标题、描述、关键词、父分类等信息。需要传入分类ID，至少传入一个要更新的字段。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id":          {Type: schema.Integer, Desc: "要更新的分类ID", Required: true},
			"title":       {Type: schema.String, Desc: "新的分类名称"},
			"description": {Type: schema.String, Desc: "新的分类描述"},
			"keywords":    {Type: schema.String, Desc: "新的分类关键词"},
			"parent_id":   {Type: schema.Integer, Desc: "新的父分类ID，0表示顶级分类"},
			"sort":        {Type: schema.Integer, Desc: "排序值，越小越靠前"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Id          uint   `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Keywords    string `json:"keywords"`
			ParentID    uint   `json:"parent_id"`
			Sort        uint   `json:"sort"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Id == 0 {
			return "错误：分类ID不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		cat, err := w.GetCategoryById(args.Id)
		if err != nil {
			return "", fmt.Errorf("获取分类失败: %w", err)
		}
		req := &request.Category{
			Id:        cat.Id,
			Title:     cat.Title,
			Keywords:  cat.Keywords,
			ParentId:  cat.ParentId,
			Sort:      cat.Sort,
			Status:    cat.Status,
			Type:      cat.Type,
			ModuleId:  cat.ModuleId,
			Images:    cat.Images,
			IsInherit: cat.IsInherit,
		}
		changed := false
		if args.Title != "" {
			req.Title = args.Title
			changed = true
		}
		if args.Description != "" {
			req.Description = args.Description
			changed = true
		}
		if args.Keywords != "" {
			req.Keywords = args.Keywords
			changed = true
		}
		req.ParentId = args.ParentID
		changed = true
		if args.Sort > 0 {
			req.Sort = args.Sort
			changed = true
		}
		if !changed {
			return "未提供要更新的字段", nil
		}
		cat, err = w.SaveCategory(req)
		if err != nil {
			return "", fmt.Errorf("更新分类失败: %w", err)
		}
		return fmt.Sprintf("分类 [%d] %s 已成功更新", cat.Id, cat.Title), nil
	})

	// ---- Tag Update ----
	add(&schema.ToolInfo{
		Name: "tag_update",
		Desc: "更新已有标签的标题、描述等信息。需要传入标签ID，至少传入一个要更新的字段。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id":          {Type: schema.Integer, Desc: "要更新的标签ID", Required: true},
			"title":       {Type: schema.String, Desc: "新的标签名称"},
			"description": {Type: schema.String, Desc: "新的标签描述"},
			"category_id": {Type: schema.Integer, Desc: "标签分类ID"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Id          uint   `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			CategoryID  uint   `json:"category_id"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Id == 0 {
			return "错误：标签ID不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		tag, err := w.GetTagById(args.Id)
		if err != nil {
			return "", fmt.Errorf("获取标签失败: %w", err)
		}
		req := &request.PluginTag{
			Id:          tag.Id,
			Title:       tag.Title,
			Description: tag.Description,
			CategoryId:  tag.CategoryId,
		}
		changed := false
		if args.Title != "" {
			req.Title = args.Title
			changed = true
		}
		if args.Description != "" {
			req.Description = args.Description
			changed = true
		}
		if args.CategoryID > 0 {
			req.CategoryId = args.CategoryID
			changed = true
		}
		if !changed {
			return "未提供要更新的字段", nil
		}
		tag, err = w.SaveTag(req)
		if err != nil {
			return "", fmt.Errorf("更新标签失败: %w", err)
		}
		return fmt.Sprintf("标签 [%d] %s 已成功更新", tag.Id, tag.Title), nil
	})

	// ---- Attachment Tools ----
	add(&schema.ToolInfo{
		Name: "attachment_list",
		Desc: "分页获取附件列表，支持按分类和关键词过滤。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"page":        {Type: schema.Integer, Desc: "页码，从1开始，默认1"},
			"page_size":   {Type: schema.Integer, Desc: "每页数量，最大100，默认10"},
			"category_id": {Type: schema.Integer, Desc: "附件分类ID，筛选指定分类"},
			"keyword":     {Type: schema.String, Desc: "搜索关键词，匹配文件名"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Page       int    `json:"page"`
			PageSize   int    `json:"page_size"`
			CategoryID uint   `json:"category_id"`
			Keyword    string `json:"keyword"`
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
		attachments, total, err := w.GetAttachmentList(args.CategoryID, args.Keyword, args.Page, args.PageSize)
		if err != nil {
			return "", fmt.Errorf("获取附件列表失败: %w", err)
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 个附件（当前页 %d 个）：\n\n", total, len(attachments)))
		for _, a := range attachments {
			a.GetThumb(w.PluginStorage.StorageUrl)
			imgType := "文件"
			if a.IsImage == 1 {
				imgType = "图片"
			}
			b.WriteString(fmt.Sprintf("- [%d] %s (%s, %d×%d) %s\n", a.Id, a.FileName, imgType, a.Width, a.Height, a.Logo))
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "attachment_get",
		Desc: "获取单个附件的详细信息，包括URL。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "附件ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		a, err := w.GetAttachmentById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取附件失败: %w", err)
		}
		a.GetThumb(w.PluginStorage.StorageUrl)
		imgType := "文件"
		if a.IsImage == 1 {
			imgType = "图片"
		}
		return fmt.Sprintf("附件信息：\nID: %d\n文件名: %s\n类型: %s\n尺寸: %d×%d\n大小: %d 字节\nURL: %s\n缩略图: %s",
			a.Id, a.FileName, imgType, a.Width, a.Height, a.FileSize, a.Logo, a.Thumb), nil
	})

	add(&schema.ToolInfo{
		Name: "attachment_upload",
		Desc: "通过远程URL上传附件（图片）到站点。AI无法直接上传本地文件，只能从网络URL下载。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"url":       {Type: schema.String, Desc: "图片的远程URL地址", Required: true},
			"file_name": {Type: schema.String, Desc: "保存的文件名（不含扩展名），可选"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			URL      string `json:"url"`
			FileName string `json:"file_name"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.URL == "" {
			return "错误：URL不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		attachment, err := w.DownloadRemoteImage(args.URL, args.FileName, 0)
		if err != nil {
			return "", fmt.Errorf("上传附件失败: %w", err)
		}
		attachment.GetThumb(w.PluginStorage.StorageUrl)
		return fmt.Sprintf("附件上传成功！ID: %d\n文件名: %s\nURL: %s", attachment.Id, attachment.FileName, attachment.Logo), nil
	})

	add(&schema.ToolInfo{
		Name: "attachment_delete",
		Desc: "删除附件。注意：此操作会同时删除物理文件，不可恢复。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "要删除的附件ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		attach, err := w.GetAttachmentById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取附件失败: %w", err)
		}
		err = w.DeleteAttachment(attach)
		if err != nil {
			return "", fmt.Errorf("删除附件失败: %w", err)
		}
		return fmt.Sprintf("附件 [%d] %s 已成功删除", attach.Id, attach.FileName), nil
	})

	// ---- Navigation tools ----
	add(&schema.ToolInfo{
		Name: "nav_list",
		Desc: "获取导航菜单列表，按树形结构返回。type_id=1 为主导航，其他值可能对应不同导航位置。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"type_id":   {Type: schema.Integer, Desc: "导航类型ID，默认1（主导航）"},
			"show_type": {Type: schema.String, Desc: "展示类型：list=平铺列表，children=树形结构（默认）"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			TypeId   uint   `json:"type_id"`
			ShowType string `json:"show_type"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.TypeId == 0 {
			args.TypeId = 1
		}
		if args.ShowType == "" {
			args.ShowType = "children"
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		navs, err := w.GetNavList(args.TypeId, args.ShowType)
		if err != nil {
			return "", fmt.Errorf("获取导航列表失败: %w", err)
		}
		var b strings.Builder
		if len(navs) == 0 {
			b.WriteString("暂无导航菜单")
		} else {
			b.WriteString(fmt.Sprintf("共 %d 个导航项（type_id=%d）：\n\n", len(navs), args.TypeId))
			var printNav func(navs []*model.Nav, depth int)
			printNav = func(navs []*model.Nav, depth int) {
				for _, n := range navs {
					prefix := ""
					for i := 0; i < depth; i++ {
						prefix += "  "
					}
					navType := "系统"
					switch n.NavType {
					case model.NavTypeCategory:
						navType = "分类"
					case model.NavTypeOutlink:
						navType = "外链"
					case model.NavTypeArchive:
						navType = "文档"
					}
					status := "启用"
					if n.Status == 0 {
						status = "禁用"
					}
					b.WriteString(fmt.Sprintf("%s- [%d] %s (类型:%s, 排序:%d, 状态:%s)\n", prefix, n.Id, n.Title, navType, n.Sort, status))
					if len(n.NavList) > 0 {
						printNav(n.NavList, depth+1)
					}
				}
			}
			printNav(navs, 0)
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "nav_create",
		Desc: "创建或编辑导航菜单。如果指定ID则更新已有导航，否则创建新导航。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id":          {Type: schema.Integer, Desc: "导航ID，留空表示创建新导航"},
			"title":       {Type: schema.String, Desc: "导航显示名称", Required: true},
			"sub_title":   {Type: schema.String, Desc: "副标题"},
			"description": {Type: schema.String, Desc: "导航描述"},
			"parent_id":   {Type: schema.Integer, Desc: "父导航ID，0表示顶级"},
			"nav_type":    {Type: schema.Integer, Desc: "导航类型：0=系统页,1=分类,2=外链,3=文档"},
			"page_id":     {Type: schema.Integer, Desc: "关联的页面ID（分类ID/文档ID等）"},
			"type_id":     {Type: schema.Integer, Desc: "导航位置类型ID，默认1"},
			"link":        {Type: schema.String, Desc: "外链URL（nav_type=2时必填）"},
			"sort":        {Type: schema.Integer, Desc: "排序值，越小越靠前"},
			"status":      {Type: schema.Integer, Desc: "状态：1=启用,0=禁用，默认1"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Id          uint   `json:"id"`
			Title       string `json:"title"`
			SubTitle    string `json:"sub_title"`
			Description string `json:"description"`
			ParentId    uint   `json:"parent_id"`
			NavType     uint   `json:"nav_type"`
			PageId      int64  `json:"page_id"`
			TypeId      uint   `json:"type_id"`
			Link        string `json:"link"`
			Sort        uint   `json:"sort"`
			Status      uint   `json:"status"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" {
			return "错误：导航显示名称不能为空", nil
		}
		if args.TypeId == 0 {
			args.TypeId = 1
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		req := &request.NavConfig{
			Id:          args.Id,
			Title:       args.Title,
			SubTitle:    args.SubTitle,
			Description: args.Description,
			ParentId:    args.ParentId,
			NavType:     args.NavType,
			PageId:      args.PageId,
			TypeId:      args.TypeId,
			Link:        args.Link,
			Sort:        args.Sort,
			Status:      args.Status,
		}
		if req.Status == 0 {
			req.Status = 1
		}
		nav, err := w.SaveNav(req)
		if err != nil {
			return "", fmt.Errorf("保存导航失败: %w", err)
		}
		w.DeleteCacheNavs()
		return fmt.Sprintf("导航 [%d] %s 已成功保存", nav.Id, nav.Title), nil
	})

	add(&schema.ToolInfo{
		Name: "nav_delete",
		Desc: "删除导航菜单。注意：此操作不可恢复。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id": {Type: schema.Integer, Desc: "要删除的导航ID", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args ArgId
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		nav, err := w.GetNavById(uint(args.Id))
		if err != nil {
			return "", fmt.Errorf("获取导航失败: %w", err)
		}
		err = nav.Delete(w.DB)
		if err != nil {
			return "", fmt.Errorf("删除导航失败: %w", err)
		}
		w.DeleteCacheNavs()
		return fmt.Sprintf("导航 [%d] %s 已成功删除", nav.Id, nav.Title), nil
	})

	// ---- Friend link tools ----
	add(&schema.ToolInfo{
		Name: "friendlink_list",
		Desc: "获取友情链接列表，按排序值排列。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		links, err := w.GetLinkList()
		if err != nil {
			return "", fmt.Errorf("获取友链列表失败: %w", err)
		}
		var b strings.Builder
		if len(links) == 0 {
			b.WriteString("暂无友情链接")
		} else {
			b.WriteString(fmt.Sprintf("共 %d 条友情链接：\n\n", len(links)))
			for _, l := range links {
				status := "待审"
				if l.Status == model.LinkStatusOk {
					status = "正常"
				} else if l.Status == model.LinkStatusNofollow {
					status = "nofollow"
				} else if l.Status == model.LinkStatusNotTitle {
					status = "缺少标题"
				} else if l.Status == model.LinkStatusNotMatch {
					status = "不匹配"
				}
				b.WriteString(fmt.Sprintf("- [%d] %s -> %s (状态:%s, 排序:%d)\n", l.Id, l.Title, l.Link, status, l.Sort))
			}
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "friendlink_create",
		Desc: "添加或更新友情链接。如果相同链接已存在则更新，否则创建新链接。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"title":     {Type: schema.String, Desc: "站点名称", Required: true},
			"link":      {Type: schema.String, Desc: "站点URL", Required: true},
			"back_link": {Type: schema.String, Desc: "回链URL（可选）"},
			"my_title":  {Type: schema.String, Desc: "我方显示标题"},
			"my_link":   {Type: schema.String, Desc: "我方链接"},
			"contact":   {Type: schema.String, Desc: "联系方式（QQ/邮箱）"},
			"remark":    {Type: schema.String, Desc: "备注"},
			"nofollow":  {Type: schema.Integer, Desc: "是否nofollow：1=是,0=否"},
			"sort":      {Type: schema.Integer, Desc: "排序值，越小越靠前"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Title    string `json:"title"`
			Link     string `json:"link"`
			BackLink string `json:"back_link"`
			MyTitle  string `json:"my_title"`
			MyLink   string `json:"my_link"`
			Contact  string `json:"contact"`
			Remark   string `json:"remark"`
			Nofollow uint   `json:"nofollow"`
			Sort     uint   `json:"sort"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Title == "" || args.Link == "" {
			return "错误：站点名称和URL不能为空", nil
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		friendLink, err := w.GetLinkByLinkAndTitle(args.Link, args.Title)
		if err != nil {
			friendLink = &model.Link{Status: 0}
		}
		friendLink.Title = args.Title
		friendLink.Link = args.Link
		friendLink.BackLink = args.BackLink
		if args.MyTitle != "" {
			friendLink.MyTitle = args.MyTitle
		}
		if args.MyLink != "" {
			friendLink.MyLink = args.MyLink
		}
		if args.Contact != "" {
			friendLink.Contact = args.Contact
		}
		if args.Remark != "" {
			friendLink.Remark = args.Remark
		}
		friendLink.Nofollow = args.Nofollow
		if args.Sort > 0 {
			friendLink.Sort = args.Sort
		}
		if friendLink.Status == model.LinkStatusOk {
			// 重新申请审核
			friendLink.Status = 0
		}
		err = friendLink.Save(w.DB)
		if err != nil {
			return "", fmt.Errorf("保存友链失败: %w", err)
		}
		w.DeleteCacheIndex()
		return fmt.Sprintf("友链 [%d] %s -> %s 已成功保存", friendLink.Id, friendLink.Title, friendLink.Link), nil
	})

	add(&schema.ToolInfo{
		Name: "friendlink_delete",
		Desc: "删除友情链接。可以通过ID或URL+标题删除。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"id":    {Type: schema.Integer, Desc: "友链ID（与link二选一）"},
			"link":  {Type: schema.String, Desc: "友链URL（与id二选一）"},
			"title": {Type: schema.String, Desc: "友链标题（配合link使用）"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Id    uint   `json:"id"`
			Link  string `json:"link"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		w := svc.site
		if w == nil || w.DB == nil {
			return "错误：站点未初始化", nil
		}
		var friendLink *model.Link
		var err error
		if args.Id > 0 {
			friendLink, err = w.GetLinkById(args.Id)
		} else if args.Link != "" {
			friendLink, err = w.GetLinkByLinkAndTitle(args.Link, args.Title)
		} else {
			return "错误：请提供友链ID或URL", nil
		}
		if err != nil {
			return "", fmt.Errorf("获取友链失败: %w", err)
		}
		err = friendLink.Delete(w.DB)
		if err != nil {
			return "", fmt.Errorf("删除友链失败: %w", err)
		}
		w.DeleteCacheIndex()
		return fmt.Sprintf("友链 [%d] %s 已成功删除", friendLink.Id, friendLink.Title), nil
	})

	// ---- Website info tool ----
	add(&schema.ToolInfo{
		Name: "website_info",
		Desc: "获取当前站点基本信息，包括站点名称、Logo、联系方式、备案号等。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		w := svc.site
		if w == nil {
			return "错误：站点未初始化", nil
		}
		s := w.System
		c := w.Content
		contact := w.Contact
		index := w.Index
		return fmt.Sprintf(`站点信息：
━━━━━━━━━━━━━━━━━━━━━
名称: %s
Logo: %s
网址: %s
IPC备案: %s
版权信息: %s
邮箱: %s
电话: %s
地址: %s
━━━━━━━━━━━━━━━━━━━━━
内容设置：
远程下载: %d
过滤外链: %d
URL模式: %d
默认模板: %s
━━━━━━━━━━━━━━━━━━━━━
SEO信息：
标题: %s
关键词: %s
描述: %s`,
			s.SiteName, s.SiteLogo, s.BaseUrl, s.SiteIcp,
			s.SiteCopyright, contact.Email, contact.Cellphone, contact.Address,
			c.RemoteDownload, c.FilterOutlink, c.UrlTokenType, s.TemplateName,
			index.SeoTitle, index.SeoKeywords, index.SeoDescription), nil
	})

	// ---- Skill tools (progressive disclosure) ----
	add(skillListTool())
	add(skillGetTool())
	add(skillReloadTool())

	return tools, handlers
}
