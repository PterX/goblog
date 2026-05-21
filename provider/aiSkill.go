package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"kandaoni.com/anqicms/config"
)

// ---------------------------------------------------------------------------
// Skill types
// ---------------------------------------------------------------------------

// SkillFrontMatter is the YAML frontmatter metadata parsed from SKILL.md.
type SkillFrontMatter struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Category    string   `yaml:"category" json:"category"`
	Version     string   `yaml:"version" json:"version"`
	Author      string   `yaml:"author" json:"author"`
	Tags        []string `yaml:"tags" json:"tags"`
}

// Skill is a loaded skill with full content.
type Skill struct {
	SkillFrontMatter
	Content       string    `json:"content"`
	BaseDirectory string    `json:"base_directory"`
	UpdatedAt     time.Time `json:"updated_at"`
	SourcePath    string    `json:"source_path"`
}

// SkillBackend is the interface for loading skills from storage.
type SkillBackend interface {
	List(ctx context.Context) ([]SkillFrontMatter, error)
	Get(ctx context.Context, name string) (*Skill, error)
	Reload(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// FilesystemBackend — scans data/skills/*/SKILL.md
// ---------------------------------------------------------------------------

type FilesystemSkillBackend struct {
	mu     sync.RWMutex
	skills map[string]*Skill // keyed by name
	dir    string            // absolute path to data/skills/
}

// NewFilesystemSkillBackend creates a backend that loads skills from data/skills/.
func NewFilesystemSkillBackend() *FilesystemSkillBackend {
	dir := filepath.Join(strings.TrimSuffix(config.ExecPath, "/"), "data", "skills")
	b := &FilesystemSkillBackend{
		skills: make(map[string]*Skill),
		dir:    dir,
	}
	if err := b.Reload(context.Background()); err != nil {
		log.Printf("[skill] initial reload failed: %v", err)
	}
	log.Printf("[skill] backend initialized, dir=%s, count=%d", dir, len(b.skills))
	return b
}

func (b *FilesystemSkillBackend) List(_ context.Context) ([]SkillFrontMatter, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	list := make([]SkillFrontMatter, 0, len(b.skills))
	for _, s := range b.skills {
		list = append(list, s.SkillFrontMatter)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list, nil
}

func (b *FilesystemSkillBackend) Get(_ context.Context, name string) (*Skill, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	s, ok := b.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	clone := *s
	return &clone, nil
}

func (b *FilesystemSkillBackend) Reload(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	newSkills := make(map[string]*Skill)

	// Ensure dir exists
	if err := os.MkdirAll(b.dir, 0755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}

	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return fmt.Errorf("read skills dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(b.dir, entry.Name(), "SKILL.md")
		info, err := os.Stat(skillPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.Printf("[skill] stat %s: %v", skillPath, err)
			continue
		}

		raw, err := os.ReadFile(skillPath)
		if err != nil {
			log.Printf("[skill] read %s: %v", skillPath, err)
			continue
		}

		content := string(raw)
		fm, body := parseSkillFrontmatter(content)

		// If name is not set in frontmatter, use directory name
		if fm.Name == "" {
			fm.Name = entry.Name()
		}

		// If description is not set, use first paragraph
		if fm.Description == "" {
			fm.Description = firstParagraph(body)
		}

		skill := &Skill{
			SkillFrontMatter: fm,
			Content:          body,
			BaseDirectory:    filepath.Join(b.dir, entry.Name()),
			UpdatedAt:        info.ModTime(),
			SourcePath:       skillPath,
		}
		newSkills[fm.Name] = skill
	}

	b.skills = newSkills
	return nil
}

// ---------------------------------------------------------------------------
// Frontmatter parser (simple line-based, no yaml dependency needed)
// ---------------------------------------------------------------------------

func parseSkillFrontmatter(content string) (SkillFrontMatter, string) {
	var fm SkillFrontMatter

	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return fm, content
	}

	// Find closing ---
	rest := content[4:] // skip "---\n" or "---\r\n"
	nl := 2
	if strings.HasPrefix(content, "---\n") {
		rest = content[4:]
		nl = 4 // \n---
	} else {
		rest = content[5:]
		nl = 5 // \n---\r\n or \r\n---
	}

	closeIdx := strings.Index(rest, "\n---")
	if closeIdx < 0 {
		return fm, content
	}

	fmText := rest[:closeIdx]
	body := rest[closeIdx+nl:]

	for _, line := range strings.Split(fmText, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "name:") {
			fm.Name = strings.TrimSpace(line[5:])
		} else if strings.HasPrefix(line, "description:") {
			fm.Description = strings.TrimSpace(line[12:])
		} else if strings.HasPrefix(line, "category:") {
			fm.Category = strings.TrimSpace(line[9:])
		} else if strings.HasPrefix(line, "version:") {
			fm.Version = strings.TrimSpace(line[8:])
		} else if strings.HasPrefix(line, "author:") {
			fm.Author = strings.TrimSpace(line[7:])
		} else if strings.HasPrefix(line, "tags:") {
			// Tags can be inline: tags: [seo, analysis] or multi-line
			tagPart := strings.TrimSpace(line[5:])
			tagPart = strings.Trim(tagPart, "[]")
			for _, t := range strings.Split(tagPart, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					fm.Tags = append(fm.Tags, t)
				}
			}
		}
	}

	// Strip quotes from values
	fm.Name = strings.Trim(fm.Name, "\"'")
	fm.Description = strings.Trim(fm.Description, "\"'")
	fm.Category = strings.Trim(fm.Category, "\"'")
	fm.Version = strings.Trim(fm.Version, "\"'")
	fm.Author = strings.Trim(fm.Author, "\"'")

	return fm, body
}

func firstParagraph(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return trimmed
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Global skill backend instance
// ---------------------------------------------------------------------------

var (
	globalSkillBackend     *FilesystemSkillBackend
	globalSkillBackendOnce sync.Once
)

// GetSkillBackend returns the global singleton skill backend.
func GetSkillBackend() *FilesystemSkillBackend {
	globalSkillBackendOnce.Do(func() {
		globalSkillBackend = NewFilesystemSkillBackend()
	})
	return globalSkillBackend
}

// ---------------------------------------------------------------------------
// Skill tool handlers for Eino
// ---------------------------------------------------------------------------

// skillListTool returns the tool info and handler for listing available skills.
func skillListTool() (*schema.ToolInfo, toolHandler) {
	return &schema.ToolInfo{
		Name: "skill_list",
		Desc: "列出所有可用的技能(SKILL)。返回每个技能的名称(name)、描述(description)和分类(category)。当用户的任务需要专业指导时，应先用此工具查看可用技能，再调用 skill_get 加载具体内容。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		backend := GetSkillBackend()
		list, err := backend.List(ctx)
		if err != nil {
			return "", fmt.Errorf("获取技能列表失败: %w", err)
		}
		if len(list) == 0 {
			return "当前没有可用的技能。", nil
		}
		var sb strings.Builder
		sb.WriteString("## 📋 可用技能列表\n\n")
		for _, s := range list {
			sb.WriteString(fmt.Sprintf("### %s\n", s.Name))
			sb.WriteString(fmt.Sprintf("- **描述**: %s\n", s.Description))
			if s.Category != "" {
				sb.WriteString(fmt.Sprintf("- **分类**: %s\n", s.Category))
			}
			if s.Version != "" {
				sb.WriteString(fmt.Sprintf("- **版本**: %s\n", s.Version))
			}
			if len(s.Tags) > 0 {
				sb.WriteString(fmt.Sprintf("- **标签**: %s\n", strings.Join(s.Tags, ", ")))
			}
			sb.WriteString("\n")
		}
		return sb.String(), nil
	}
}

// skillGetTool returns the tool info and handler for loading a specific skill's content.
func skillGetTool() (*schema.ToolInfo, toolHandler) {
	return &schema.ToolInfo{
		Name: "skill_get",
		Desc: "获取指定技能(SKILL)的完整内容。参数: name=技能名称(必填)。返回技能完整说明文档，包含使用步骤、最佳实践和示例。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Type: schema.String,
				Desc: "技能名称，必填。应先使用 skill_list 查看可用技能的名称。",
			},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Name == "" {
			return "", fmt.Errorf("参数 name 为必填")
		}

		backend := GetSkillBackend()
		skill, err := backend.Get(ctx, args.Name)
		if err != nil {
			// Try fuzzy match
			list, _ := backend.List(ctx)
			var similar []string
			for _, s := range list {
				if strings.Contains(strings.ToLower(s.Name), strings.ToLower(args.Name)) {
					similar = append(similar, s.Name)
				}
			}
			if len(similar) > 0 {
				return fmt.Sprintf("未找到技能 %q。相似技能: %s", args.Name, strings.Join(similar, ", ")), nil
			}
			return fmt.Sprintf("未找到技能 %q。请先用 skill_list 查看可用技能列表。", args.Name), nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# %s\n\n", skill.Name))
		if skill.Description != "" {
			sb.WriteString(fmt.Sprintf("> %s\n\n", skill.Description))
		}
		if skill.Category != "" {
			sb.WriteString(fmt.Sprintf("**分类**: %s  ", skill.Category))
		}
		if skill.Version != "" {
			sb.WriteString(fmt.Sprintf("**版本**: %s  ", skill.Version))
		}
		if !skill.UpdatedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("**更新**: %s", skill.UpdatedAt.Format("2006-01-02")))
		}
		sb.WriteString("\n\n---\n\n")
		sb.WriteString(skill.Content)

		return sb.String(), nil
	}
}

// skillReloadTool returns the tool info and handler for reloading skills from disk.
func skillReloadTool() (*schema.ToolInfo, toolHandler) {
	return &schema.ToolInfo{
		Name: "skill_reload",
		Desc: "重新加载所有技能(SKILL)，从 data/skills/ 目录重新扫描 SKILL.md 文件。管理员编辑或新增技能后调用此工具使其生效。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		backend := GetSkillBackend()
		if err := backend.Reload(ctx); err != nil {
			return "", fmt.Errorf("重新加载技能失败: %w", err)
		}
		list, _ := backend.List(ctx)
		return fmt.Sprintf("技能已重新加载，当前有 %d 个可用技能。", len(list)), nil
	}
}

// BuildSkillSystemPrompt returns the skill usage instructions to inject into system prompt.
func BuildSkillSystemPrompt() string {
	return `
## 技能系统 (Skills)

本系统内置了专业技能(SKILL)，可在处理特定任务时提供结构化指导。

使用方法：
1. 当用户的任务涉及专业领域（如SEO分析、内容规划、模板定制等）时，先用 skill_list 查看可用技能
2. 如果找到匹配的技能，调用 skill_get 加载完整内容并遵循其中的步骤
3. 技能内容可能引用本系统的其他工具，按需调用即可

注意：不要只提技能名称而不实际调用 skill_get 加载。先确定任务，再调 skill_get 获取指导。
`
}

// BuildSkillToolDescription returns tool descriptions for the skill tools in MCP format.
func BuildSkillToolDescription() string {
	listTool, _ := skillListTool()
	getTool, _ := skillGetTool()
	return fmt.Sprintf("- %s: %s\n- %s: %s", listTool.Name, listTool.Desc, getTool.Name, getTool.Desc)
}
