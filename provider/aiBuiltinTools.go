package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/cloudwego/eino/schema"
)

// ---- Arg types for built-in tools ----

type fileReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type fileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fileEditArgs struct {
	Path    string `json:"path"`
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

type searchReplaceArgs struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
	Glob    string `json:"glob"`
	Regex   bool   `json:"regex"`
}

type bashArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type grepArgs struct {
	Pattern string `json:"pattern"`
	Glob    string `json:"glob"`
	Context int    `json:"context"`
}

type globArgs struct {
	Pattern string `json:"pattern"`
}

type listDirArgs struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

type webFetchArgs struct {
	URL string `json:"url"`
}

type webSearchArgs struct {
	Query string `json:"query"`
}

type symbolArgs struct {
	File    string `json:"file"`
	Package string `json:"package"`
	Symbol  string `json:"symbol"`
}

type importArgs struct {
	File string `json:"file"`
}

// ---- Project root (safe boundary) ----

var projectRoot string

func init() {
	wd, err := os.Getwd()
	if err == nil {
		projectRoot = wd
	}
}

func safePath(path string) (string, error) {
	if projectRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("无法获取工作目录: %w", err)
		}
		projectRoot = wd
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectRoot, p)
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("无法解析路径: %w", err)
	}
	if !strings.HasPrefix(p, projectRoot) {
		return "", fmt.Errorf("路径超出项目目录范围: %s", p)
	}
	return p, nil
}

// getBuiltinEinoTools returns the built-in file/system/code tools for AnQiCMS.
func (svc *AiChatService) getBuiltinEinoTools() ([]*schema.ToolInfo, map[string]toolHandler) {
	tools := make([]*schema.ToolInfo, 0)
	handlers := make(map[string]toolHandler)

	add := func(ti *schema.ToolInfo, fn toolHandler) {
		tools = append(tools, ti)
		handlers[ti.Name] = fn
	}

	// ================================================================
	//  File & Shell tools
	// ================================================================

	add(&schema.ToolInfo{
		Name: "read_file",
		Desc: "读取项目内文件的内容。支持 offset（起始行号，从1开始）和 limit（最大行数）参数分段读取大文件。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":   {Type: schema.String, Desc: "文件路径，相对项目根目录或绝对路径", Required: true},
			"offset": {Type: schema.Integer, Desc: "起始行号（从1开始），可选"},
			"limit":  {Type: schema.Integer, Desc: "最大读取行数，可选"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args fileReadArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Path == "" {
			return "错误：文件路径不能为空", nil
		}
		fullPath, err := safePath(args.Path)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Sprintf("错误：文件不存在: %s", args.Path), nil
			}
			return "", fmt.Errorf("访问文件失败: %w", err)
		}
		if info.IsDir() {
			return fmt.Sprintf("错误：%s 是一个目录，请使用 list_directory 查看目录内容", args.Path), nil
		}
		if info.Size() > 5*1024*1024 {
			return "错误：文件超过 5MB 限制，无法读取", nil
		}

		f, err := os.Open(fullPath)
		if err != nil {
			return "", fmt.Errorf("打开文件失败: %w", err)
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

		var b strings.Builder
		lineNum := 0
		maxLines := 10000
		startLine := args.Offset
		if startLine <= 0 {
			startLine = 1
		}
		for scanner.Scan() {
			lineNum++
			if lineNum < startLine {
				continue
			}
			b.WriteString(fmt.Sprintf("%6d| %s\n", lineNum, scanner.Text()))
			if args.Limit > 0 && lineNum >= startLine+args.Limit-1 {
				break
			}
			if lineNum >= startLine+maxLines-1 {
				b.WriteString(fmt.Sprintf("\n... (文件较大，仅显示前 %d 行)", maxLines))
				break
			}
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("读取文件失败: %w", err)
		}
		result := b.String()
		if result == "" {
			result = "(空文件或起始行超出范围)"
		}
		relPath, _ := filepath.Rel(projectRoot, fullPath)
		return fmt.Sprintf("文件: %s\n总大小: %d 字节\n\n%s", relPath, info.Size(), result), nil
	})

	add(&schema.ToolInfo{
		Name: "write_file",
		Desc: "写入或创建文件。如果文件已存在则覆盖。会自动创建父目录。注意：只能操作项目目录内的文件。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":    {Type: schema.String, Desc: "文件路径，相对项目根目录或绝对路径", Required: true},
			"content": {Type: schema.String, Desc: "文件内容", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args fileWriteArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Path == "" {
			return "错误：文件路径不能为空", nil
		}
		fullPath, err := safePath(args.Path)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		// Create parent directories
		parent := filepath.Dir(fullPath)
		if err := os.MkdirAll(parent, 0755); err != nil {
			return "", fmt.Errorf("创建目录失败: %w", err)
		}
		if err := os.WriteFile(fullPath, []byte(args.Content), 0644); err != nil {
			return "", fmt.Errorf("写入文件失败: %w", err)
		}
		relPath, _ := filepath.Rel(projectRoot, fullPath)
		return fmt.Sprintf("文件写入成功: %s (%d 字节)", relPath, len(args.Content)), nil
	})

	add(&schema.ToolInfo{
		Name: "edit_file",
		Desc: "在单个文件中搜索文本并替换为新的文本。使用 old_string 精确匹配要替换的内容，new_string 指定替换后的内容。支持文件路径参数。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":    {Type: schema.String, Desc: "文件路径", Required: true},
			"search":  {Type: schema.String, Desc: "要搜索的旧文本（精确匹配）", Required: true},
			"replace": {Type: schema.String, Desc: "替换后的新文本", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args fileEditArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Path == "" || args.Search == "" {
			return "错误：文件路径和搜索文本不能为空", nil
		}
		fullPath, err := safePath(args.Path)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Sprintf("错误：文件不存在: %s", args.Path), nil
			}
			return "", fmt.Errorf("读取文件失败: %w", err)
		}
		oldStr := args.Search
		newStr := args.Replace
		if !strings.Contains(string(data), oldStr) {
			return "错误：未找到匹配的文本，请检查搜索内容", nil
		}
		result := strings.ReplaceAll(string(data), oldStr, newStr)
		if err := os.WriteFile(fullPath, []byte(result), 0644); err != nil {
			return "", fmt.Errorf("写入文件失败: %w", err)
		}
		count := strings.Count(string(data), oldStr)
		relPath, _ := filepath.Rel(projectRoot, fullPath)
		return fmt.Sprintf("文件 %s 已更新，共替换 %d 处", relPath, count), nil
	})

	add(&schema.ToolInfo{
		Name: "search_replace",
		Desc: "在多个文件中搜索并替换文本。支持 glob 模式匹配文件和正则表达式搜索。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"search":  {Type: schema.String, Desc: "要搜索的文本（或正则表达式）", Required: true},
			"replace": {Type: schema.String, Desc: "替换后的文本", Required: true},
			"glob":    {Type: schema.String, Desc: "文件匹配模式，如 '**/*.go'、'*.html'，默认 '**/*'"},
			"regex":   {Type: schema.Boolean, Desc: "是否将 search 视为正则表达式，默认 false"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args searchReplaceArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Search == "" {
			return "错误：搜索文本不能为空", nil
		}
		if args.Glob == "" {
			args.Glob = "**/*"
		}

		// Find matching files
		_, err := filepath.Glob(filepath.Join(projectRoot, args.Glob))
		if err != nil {
			return "", fmt.Errorf("文件匹配失败: %w", err)
		}
		// filepath.Glob doesn't support ** — do manual walk
		var allFiles []string
		err = filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil // skip inaccessible
			}
			if fi.IsDir() {
				// Skip hidden dirs and vendor/node_modules
				base := fi.Name()
				if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(projectRoot, path)
			matched, err := filepath.Match(args.Glob, rel)
			if err != nil {
				return nil
			}
			if matched || args.Glob == "**/*" {
				// Also check ** matching
				if strings.Contains(args.Glob, "**") {
					parts := strings.Split(args.Glob, "**")
					if len(parts) == 2 {
						if strings.HasPrefix(rel, strings.TrimRight(parts[0], "/")) &&
							strings.HasSuffix(rel, strings.TrimLeft(parts[1], "/")) {
							allFiles = append(allFiles, path)
						}
					}
				} else if matched {
					allFiles = append(allFiles, path)
				}
			}
			if args.Glob == "**/*" {
				allFiles = append(allFiles, path)
			}
			return nil
		})
		if args.Glob == "**/*" {
			// Already collected everything — need to redo properly
			allFiles = nil
			filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					if fi != nil && fi.IsDir() {
						base := fi.Name()
						if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
							return filepath.SkipDir
						}
					}
					return nil
				}
				allFiles = append(allFiles, path)
				return nil
			})
		}
		if err != nil {
			return "", fmt.Errorf("遍历文件失败: %w", err)
		}

		if len(allFiles) > 200 {
			return fmt.Sprintf("匹配文件过多 (%d)，请缩小 glob 范围", len(allFiles)), nil
		}
		if len(allFiles) == 0 {
			return "未找到匹配的文件", nil
		}

		var matchedFiles []string
		totalReplacements := 0
		limit := 20 // max files to modify

		var searchBytes []byte
		var re *regexp.Regexp
		if args.Regex {
			re, err = regexp.Compile(args.Search)
			if err != nil {
				return "", fmt.Errorf("正则表达式编译失败: %w", err)
			}
		} else {
			searchBytes = []byte(args.Search)
		}

		for _, fp := range allFiles {
			if len(matchedFiles) >= limit {
				break
			}
			data, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			var newData []byte
			var count int
			if args.Regex {
				matches := re.FindAll(data, -1)
				count = len(matches)
				if count > 0 {
					newData = re.ReplaceAll(data, []byte(args.Replace))
				}
			} else {
				count = bytes.Count(data, searchBytes)
				if count > 0 {
					newData = bytes.ReplaceAll(data, searchBytes, []byte(args.Replace))
				}
			}
			if count > 0 {
				if err := os.WriteFile(fp, newData, 0644); err != nil {
					continue
				}
				relPath, _ := filepath.Rel(projectRoot, fp)
				matchedFiles = append(matchedFiles, fmt.Sprintf("  - %s (%d 处)", relPath, count))
				totalReplacements += count
			}
		}

		if len(matchedFiles) == 0 {
			return "未找到匹配的内容", nil
		}
		result := fmt.Sprintf("搜索替换完成，共修改 %d 个文件，替换 %d 处：\n\n", len(matchedFiles), totalReplacements)
		result += strings.Join(matchedFiles, "\n")
		if len(allFiles) > limit {
			result += fmt.Sprintf("\n\n(还有 %d 个文件未处理，请缩小搜索范围)", len(allFiles)-limit)
		}
		return result, nil
	})

	add(&schema.ToolInfo{
		Name: "bash",
		Desc: "在项目根目录执行 shell 命令。用于运行构建、测试、代码生成等开发命令。注意：不能使用交互式命令。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"command": {Type: schema.String, Desc: "要执行的 shell 命令", Required: true},
			"timeout": {Type: schema.Integer, Desc: "超时时间（秒），默认 30，最大 120"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args bashArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Command == "" {
			return "错误：命令不能为空", nil
		}
		if args.Timeout <= 0 || args.Timeout > 120 {
			args.Timeout = 30
		}

		// Security checks
		lower := strings.ToLower(strings.TrimSpace(args.Command))
		dangerous := []string{
			"rm -rf /", "rm -rf ~", "rm -rf .",
			"dd if=", "mkfs", "fdisk", "mkswap",
			"sudo", "su ", "chmod 777", "chown",
			":(){ :|:& };:", "reboot", "shutdown", "halt",
			"poweroff", "init 0", "init 6",
		}
		for _, d := range dangerous {
			if strings.Contains(lower, d) {
				return fmt.Sprintf("错误：检测到危险命令 '%s'，已阻止执行", d), nil
			}
		}

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/C", args.Command)
		} else {
			cmd = exec.Command("sh", "-c", args.Command)
		}
		cmd.Dir = projectRoot

		timeout := time.Duration(args.Timeout) * time.Second
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		var b strings.Builder
		b.WriteString(fmt.Sprintf("$ %s\n", args.Command))
		if stdout.Len() > 0 {
			out := stdout.String()
			if len(out) > 50000 {
				out = out[:50000] + "\n... (输出截断，超过 50000 字符)"
			}
			b.WriteString(out)
		}
		if stderr.Len() > 0 {
			errStr := stderr.String()
			if len(errStr) > 10000 {
				errStr = errStr[:10000] + "\n... (错误输出截断)"
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString("STDERR:\n" + errStr)
		}
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("命令执行超时（%d秒）", args.Timeout)
			}
			b.WriteString(fmt.Sprintf("\n退出码: %v", err))
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "grep",
		Desc: "在项目文件中搜索文本或正则表达式。支持指定文件匹配模式和上下文行数。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {Type: schema.String, Desc: "搜索模式文本", Required: true},
			"glob":    {Type: schema.String, Desc: "文件匹配模式，如 '*.go'、'*.html'，默认所有文件"},
			"context": {Type: schema.Integer, Desc: "上下文行数（包含匹配行前后各 N 行），默认 0"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args grepArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Pattern == "" {
			return "错误：搜索模式不能为空", nil
		}
		if args.Context < 0 {
			args.Context = 0
		}

		re, err := regexp.Compile(args.Pattern)
		if err != nil {
			return "", fmt.Errorf("正则表达式编译失败: %w", err)
		}

		type match struct {
			File    string
			Line    int
			Content string
			Before  []string
			After   []string
		}

		var matches []match
		maxResults := 100

		filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				if fi != nil && fi.IsDir() {
					base := fi.Name()
					if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
						return filepath.SkipDir
					}
				}
				return nil
			}
			// Check glob
			if args.Glob != "" {
				rel, _ := filepath.Rel(projectRoot, path)
				matched, _ := filepath.Match(args.Glob, fi.Name())
				matchedRel, _ := filepath.Match(args.Glob, rel)
				if !matched && !matchedRel {
					return nil
				}
			}
			// Skip binary files
			if fi.Size() > 1024*1024 {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()

			var lines []string
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if len(lines) > 5000 {
				return nil // skip large files
			}

			relPath, _ := filepath.Rel(projectRoot, path)
			for i, line := range lines {
				if re.MatchString(line) {
					if len(matches) >= maxResults {
						return fmt.Errorf("reached max results")
					}
					m := match{File: relPath, Line: i + 1, Content: line}
					// Before context
					start := i - args.Context
					if start < 0 {
						start = 0
					}
					for j := start; j < i; j++ {
						m.Before = append(m.Before, fmt.Sprintf("  %d| %s", j+1, lines[j]))
					}
					// After context
					end := i + args.Context + 1
					if end > len(lines) {
						end = len(lines)
					}
					for j := i + 1; j < end; j++ {
						m.After = append(m.After, fmt.Sprintf("  %d| %s", j+1, lines[j]))
					}
					matches = append(matches, m)
				}
			}
			return nil
		})

		if len(matches) == 0 {
			return "未找到匹配的内容", nil
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("共找到 %d 处匹配：\n\n", len(matches)))
		for _, m := range matches {
			b.WriteString(fmt.Sprintf("%s:%d\n", m.File, m.Line))
			for _, before := range m.Before {
				b.WriteString(before + "\n")
			}
			b.WriteString(fmt.Sprintf("  → %s\n", strings.TrimSpace(m.Content)))
			for _, after := range m.After {
				b.WriteString(after + "\n")
			}
			b.WriteString("\n")
		}
		if len(matches) >= maxResults {
			b.WriteString("... (结果过多，仅显示前 100 条)")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "glob",
		Desc: "按文件名模式查找文件。支持通配符：* 匹配任意字符，** 匹配任意目录层级。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {Type: schema.String, Desc: "文件匹配模式，如 '**/*.go'、'template/**'、'*.html'", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args globArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Pattern == "" {
			return "错误：文件匹配模式不能为空", nil
		}

		var results []string
		maxResults := 200

		_ = filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(projectRoot, path)
			// Skip hidden dirs
			if fi.IsDir() {
				base := fi.Name()
				if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
					return filepath.SkipDir
				}
				matched, err := filepath.Match(args.Pattern, rel)
				if err == nil && matched {
					results = append(results, rel+"/")
				}
				return nil
			}

			if strings.Contains(args.Pattern, "**") {
				parts := strings.Split(args.Pattern, "**")
				if len(parts) == 2 {
					prefix := strings.TrimRight(parts[0], "/")
					suffix := strings.TrimLeft(parts[1], "/")
					if (prefix == "" || strings.HasPrefix(rel, prefix)) &&
						(strings.HasSuffix(rel, suffix) || suffix == "") {
						results = append(results, rel)
					}
				}
			} else {
				matched, err := filepath.Match(args.Pattern, fi.Name())
				if err == nil && matched {
					results = append(results, rel)
				}
			}
			if len(results) > maxResults {
				return fmt.Errorf("too many results")
			}
			return nil
		})

		if len(results) == 0 {
			return "未找到匹配的文件", nil
		}

		sort.Strings(results)
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共找到 %d 个文件/目录：\n\n", len(results)))
		for _, r := range results {
			b.WriteString(r + "\n")
		}
		if len(results) > maxResults {
			b.WriteString("... (结果过多，仅显示前 200 条)")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "list_directory",
		Desc: "列出目录结构和文件。可指定目录路径和递归深度。隐藏目录会自动跳过。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":  {Type: schema.String, Desc: "目录路径，相对项目根目录或绝对路径，默认根目录"},
			"depth": {Type: schema.Integer, Desc: "递归深度，默认 2，最大 5"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args listDirArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		basePath := projectRoot
		if args.Path != "" {
			var err error
			basePath, err = safePath(args.Path)
			if err != nil {
				return "错误：" + err.Error(), nil
			}
		}
		depth := args.Depth
		if depth <= 0 {
			depth = 2
		}
		if depth > 5 {
			depth = 5
		}

		info, err := os.Stat(basePath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Sprintf("错误：目录不存在: %s", args.Path), nil
			}
			return "", fmt.Errorf("访问目录失败: %w", err)
		}
		if !info.IsDir() {
			return fmt.Sprintf("错误：%s 是一个文件，不是目录", args.Path), nil
		}

		var b strings.Builder
		rel, _ := filepath.Rel(projectRoot, basePath)
		if rel == "." {
			rel = projectRoot
		}
		b.WriteString(fmt.Sprintf("📁 %s/\n", rel))

		var walk func(path string, prefix string, remainingDepth int)
		walk = func(path string, prefix string, remainingDepth int) {
			entries, err := os.ReadDir(path)
			if err != nil {
				return
			}
			// Sort: dirs first, then files
			sort.Slice(entries, func(i, j int) bool {
				if entries[i].IsDir() != entries[j].IsDir() {
					return entries[i].IsDir()
				}
				return entries[i].Name() < entries[j].Name()
			})
			for i, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") {
					continue
				}
				isLast := i == len(entries)-1
				connector := "├── "
				if isLast {
					connector = "└── "
				}
				if entry.IsDir() {
					b.WriteString(fmt.Sprintf("%s%s📁 %s/\n", prefix, connector, entry.Name()))
					if remainingDepth > 1 {
						childPrefix := prefix
						if isLast {
							childPrefix += "    "
						} else {
							childPrefix += "│   "
						}
						walk(filepath.Join(path, entry.Name()), childPrefix, remainingDepth-1)
					}
				} else {
					fi, _ := entry.Info()
					size := ""
					if fi != nil {
						size = fmt.Sprintf(" (%d B)", fi.Size())
					}
					b.WriteString(fmt.Sprintf("%s%s📄 %s%s\n", prefix, connector, entry.Name(), size))
				}
			}
		}
		walk(basePath, "", depth)
		return b.String(), nil
	})

	// ================================================================
	//  Web tools
	// ================================================================

	add(&schema.ToolInfo{
		Name: "web_fetch",
		Desc: "获取指定URL的网页内容并返回纯文本。用于查看网页信息、API文档等。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"url": {Type: schema.String, Desc: "要获取的网页URL", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args webFetchArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.URL == "" {
			return "错误：URL 不能为空", nil
		}

		// Validate URL
		parsedURL, err := url.Parse(args.URL)
		if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return "错误：URL 格式不正确，仅支持 http/https", nil
		}

		// Block private IPs and localhost
		host := parsedURL.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" ||
			strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "172.16.") ||
			strings.HasPrefix(host, "192.168.") || host == "::1" {
			return "错误：不允许访问内网地址", nil
		}

		client := &http.Client{Timeout: 15 * time.Second}
		req, err := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
		if err != nil {
			return "", fmt.Errorf("创建请求失败: %w", err)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AnQiCMS AI Bot)")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("请求失败: %w", err)
		}
		defer resp.Body.Close()

		// Limit response size
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		if err != nil {
			return "", fmt.Errorf("读取响应失败: %w", err)
		}

		// Parse HTML and extract text
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
		if err != nil {
			// Not HTML, return raw text
			return fmt.Sprintf("URL: %s\n状态码: %d\n大小: %d 字节\n\n%s",
				args.URL, resp.StatusCode, len(body), string(body)), nil
		}

		// Remove script, style, nav, footer, header
		doc.Find("script, style, nav, footer, header, aside, noscript, iframe, svg, form").Remove()

		var textParts []string
		doc.Find("p, h1, h2, h3, h4, h5, h6, li, td, th, blockquote, pre, code, div.text, div.content, article, section").Each(func(i int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if len(text) > 20 {
				textParts = append(textParts, text)
			}
		})
		if len(textParts) == 0 {
			// Fallback: get body text
			textParts = append(textParts, strings.TrimSpace(doc.Find("body").Text()))
		}

		title := doc.Find("title").Text()
		joined := strings.Join(textParts, "\n\n")
		if len(joined) > 30000 {
			joined = joined[:30000] + "\n\n... (内容截断，超过 30000 字符)"
		}

		return fmt.Sprintf("URL: %s\n状态码: %d\n标题: %s\n\n%s",
			args.URL, resp.StatusCode, title, joined), nil
	})

	add(&schema.ToolInfo{
		Name: "web_search",
		Desc: "搜索互联网获取最新信息。使用 DuckDuckGo 搜索引擎，不需要 API Key。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Desc: "搜索关键词", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args webSearchArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Query == "" {
			return "错误：搜索关键词不能为空", nil
		}

		searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(args.Query))
		client := &http.Client{Timeout: 15 * time.Second}
		req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
		if err != nil {
			return "", fmt.Errorf("创建请求失败: %w", err)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AnQiCMS AI Bot)")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("搜索请求失败: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		if err != nil {
			return "", fmt.Errorf("读取响应失败: %w", err)
		}

		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("解析搜索结果失败: %w", err)
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("搜索结果: %s\n\n", args.Query))

		count := 0
		doc.Find(".result").Each(func(i int, s *goquery.Selection) {
			if count >= 10 {
				return
			}
			title := strings.TrimSpace(s.Find(".result__title a").Text())
			link, _ := s.Find(".result__url").Attr("href")
			snippet := strings.TrimSpace(s.Find(".result__snippet").Text())

			if title == "" {
				// Alternative selectors
				title = strings.TrimSpace(s.Find("h2 a").Text())
				link, _ = s.Find("h2 a").Attr("href")
				snippet = strings.TrimSpace(s.Find(".result__snippet, .snippet").Text())
			}
			// Clean up DuckDuckGo redirect URL
			if strings.Contains(link, "//duckduckgo.com/l/?uddg=") {
				u, err := url.Parse(link)
				if err == nil {
					if decoded := u.Query().Get("uddg"); decoded != "" {
						link = decoded
					}
				}
			}
			if title != "" {
				b.WriteString(fmt.Sprintf("%d. %s\n", count+1, title))
				if link != "" {
					b.WriteString(fmt.Sprintf("   %s\n", link))
				}
				if snippet != "" {
					b.WriteString(fmt.Sprintf("   %s\n", snippet))
				}
				b.WriteString("\n")
				count++
			}
		})

		if count == 0 {
			// Fallback: try simpler selectors
			doc.Find(".results_links").Each(func(i int, s *goquery.Selection) {
				if count >= 10 {
					return
				}
				title := strings.TrimSpace(s.Find(".results_links_title a").Text())
				link, _ := s.Find(".results_links_title a").Attr("href")
				snippet := strings.TrimSpace(s.Find(".results_links_snippet").Text())
				if title != "" {
					b.WriteString(fmt.Sprintf("%d. %s\n", count+1, title))
					if link != "" {
						b.WriteString(fmt.Sprintf("   %s\n", link))
					}
					if snippet != "" {
						b.WriteString(fmt.Sprintf("   %s\n", snippet))
					}
					b.WriteString("\n")
					count++
				}
			})
		}

		if count == 0 {
			return fmt.Sprintf("未找到关于 \"%s\" 的搜索结果，请尝试其他关键词", args.Query), nil
		}
		return b.String(), nil
	})

	// ================================================================
	//  Code Intelligence tools
	// ================================================================

	add(&schema.ToolInfo{
		Name: "list_symbols",
		Desc: "列出 Go 文件或包中的导出符号（函数、类型、变量、常量）。可以指定文件路径或包名。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file":    {Type: schema.String, Desc: "Go 文件路径，如 provider/aiTools.go"},
			"package": {Type: schema.String, Desc: "包路径，如 kandaoni.com/anqicms/provider，如果不指定则解析 file 所在的包"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args symbolArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.File == "" && args.Package == "" {
			return "错误：请指定 file 或 package 参数", nil
		}

		var files []string
		if args.File != "" {
			fullPath, err := safePath(args.File)
			if err != nil {
				return "错误：" + err.Error(), nil
			}
			if !strings.HasSuffix(fullPath, ".go") {
				return "错误：文件必须为 .go 文件", nil
			}
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				return fmt.Sprintf("错误：文件不存在: %s", args.File), nil
			}
			files = append(files, fullPath)
		} else {
			// Find all .go files in the package directory
			pkgPath := filepath.Join(projectRoot, strings.ReplaceAll(args.Package, "kandaoni.com/anqicms/", ""))
			if _, err := os.Stat(pkgPath); os.IsNotExist(err) {
				// Try as-is
				pkgPath = filepath.Join(projectRoot, args.Package)
				if _, err := os.Stat(pkgPath); os.IsNotExist(err) {
					return fmt.Sprintf("错误：包路径不存在: %s", args.Package), nil
				}
			}
			entries, err := os.ReadDir(pkgPath)
			if err != nil {
				return "", fmt.Errorf("读取目录失败: %w", err)
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
					files = append(files, filepath.Join(pkgPath, e.Name()))
				}
			}
		}

		if len(files) == 0 {
			return "未找到 Go 文件", nil
		}

		type symbolInfo struct {
			Kind string
			Name string
			File string
			Line int
			Doc  string
		}
		var symbols []symbolInfo

		fset := token.NewFileSet()
		for _, file := range files {
			f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
			if err != nil {
				continue
			}
			relPath, _ := filepath.Rel(projectRoot, file)

			// Collect comments
			comments := make(map[token.Pos]string)
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					comments[c.Slash] = strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
				}
			}

			ast.Inspect(f, func(n ast.Node) bool {
				switch decl := n.(type) {
				case *ast.GenDecl:
					if decl.Tok == token.TYPE {
						for _, spec := range decl.Specs {
							if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.IsExported() {
								doc := ""
								if decl.Doc != nil {
									doc = decl.Doc.Text()
								}
								symbols = append(symbols, symbolInfo{
									Kind: "type",
									Name: ts.Name.Name,
									File: relPath,
									Line: fset.Position(ts.Pos()).Line,
									Doc:  strings.TrimSpace(doc),
								})
							}
						}
					}
					if decl.Tok == token.VAR {
						for _, spec := range decl.Specs {
							if vs, ok := spec.(*ast.ValueSpec); ok {
								for _, name := range vs.Names {
									if name.IsExported() {
										doc := ""
										if decl.Doc != nil {
											doc = decl.Doc.Text()
										}
										symbols = append(symbols, symbolInfo{
											Kind: "var",
											Name: name.Name,
											File: relPath,
											Line: fset.Position(name.Pos()).Line,
											Doc:  strings.TrimSpace(doc),
										})
									}
								}
							}
						}
					}
					if decl.Tok == token.CONST {
						for _, spec := range decl.Specs {
							if vs, ok := spec.(*ast.ValueSpec); ok {
								for _, name := range vs.Names {
									if name.IsExported() {
										doc := ""
										if decl.Doc != nil {
											doc = decl.Doc.Text()
										}
										symbols = append(symbols, symbolInfo{
											Kind: "const",
											Name: name.Name,
											File: relPath,
											Line: fset.Position(name.Pos()).Line,
											Doc:  strings.TrimSpace(doc),
										})
									}
								}
							}
						}
					}
				case *ast.FuncDecl:
					if decl.Name.IsExported() {
						doc := ""
						if decl.Doc != nil {
							doc = decl.Doc.Text()
						}
						recv := ""
						if decl.Recv != nil {
							for _, field := range decl.Recv.List {
								switch t := field.Type.(type) {
								case *ast.StarExpr:
									if ident, ok := t.X.(*ast.Ident); ok {
										recv = ident.Name
									}
								case *ast.Ident:
									recv = t.Name
								}
							}
							recv = "(" + recv + ")"
						}
						name := recv + decl.Name.Name
						symbols = append(symbols, symbolInfo{
							Kind: "func",
							Name: name,
							File: relPath,
							Line: fset.Position(decl.Pos()).Line,
							Doc:  strings.TrimSpace(doc),
						})
					}
				}
				return true
			})
		}

		if len(symbols) == 0 {
			return "未找到导出的符号", nil
		}

		// Group by kind
		var b strings.Builder
		b.WriteString(fmt.Sprintf("共 %d 个导出符号：\n\n", len(symbols)))

		groups := make(map[string][]symbolInfo)
		for _, s := range symbols {
			groups[s.Kind] = append(groups[s.Kind], s)
		}
		for _, kind := range []string{"type", "func", "var", "const"} {
			if syms, ok := groups[kind]; ok {
				b.WriteString(fmt.Sprintf("── %s (%d) ──\n", kind, len(syms)))
				for _, s := range syms {
					b.WriteString(fmt.Sprintf("  %s (%s:%d)", s.Name, s.File, s.Line))
					if s.Doc != "" {
						doc := strings.Split(s.Doc, "\n")[0]
						if len(doc) > 60 {
							doc = doc[:60] + "..."
						}
						b.WriteString(fmt.Sprintf("  // %s", doc))
					}
					b.WriteString("\n")
				}
				b.WriteString("\n")
			}
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "read_symbol",
		Desc: "读取 Go 代码中某个符号（函数、类型等）的定义。可以指定符号名称和文件路径，如果不指定文件则全局搜索。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {Type: schema.String, Desc: "要查找的符号名称（函数名、类型名等）", Required: true},
			"file":   {Type: schema.String, Desc: "Go 文件路径，如果不指定则在所有文件中搜索"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args symbolArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Symbol == "" {
			return "错误：符号名称不能为空", nil
		}

		var searchFiles []string
		if args.File != "" {
			fullPath, err := safePath(args.File)
			if err != nil {
				return "错误：" + err.Error(), nil
			}
			searchFiles = append(searchFiles, fullPath)
		} else {
			// Walk the project and find all .go files
			filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					if fi != nil && fi.IsDir() {
						base := fi.Name()
						if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
							return filepath.SkipDir
						}
					}
					return nil
				}
				if strings.HasSuffix(path, ".go") {
					// Skip generated files
					if strings.HasSuffix(path, "_test.go") {
						return nil
					}
					searchFiles = append(searchFiles, path)
				}
				return nil
			})
			if len(searchFiles) > 200 {
				return fmt.Sprintf("错误：项目中有 %d 个 Go 文件，请指定 file 参数缩小搜索范围", len(searchFiles)), nil
			}
		}

		fset := token.NewFileSet()
		for _, file := range searchFiles {
			f, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
			if err != nil {
				continue
			}

			var found bool
			var resultBuf bytes.Buffer
			ast.Inspect(f, func(n ast.Node) bool {
				if found {
					return false
				}
				switch decl := n.(type) {
				case *ast.FuncDecl:
					if decl.Name.Name == args.Symbol || strings.HasSuffix(decl.Name.Name, "."+args.Symbol) {
						if decl.Doc != nil {
							resultBuf.WriteString(decl.Doc.Text() + "\n")
						}
						format.Node(&resultBuf, fset, decl)
						relPath, _ := filepath.Rel(projectRoot, file)
						resultBuf.WriteString(fmt.Sprintf("\n// 文件: %s\n", relPath))
						found = true
						return false
					}
				case *ast.GenDecl:
					for _, spec := range decl.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if s.Name.Name == args.Symbol {
								if decl.Doc != nil {
									resultBuf.WriteString(decl.Doc.Text() + "\n")
								}
								format.Node(&resultBuf, fset, decl)
								relPath, _ := filepath.Rel(projectRoot, file)
								resultBuf.WriteString(fmt.Sprintf("\n// 文件: %s\n", relPath))
								found = true
								return false
							}
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if name.Name == args.Symbol {
									if decl.Doc != nil {
										resultBuf.WriteString(decl.Doc.Text() + "\n")
									}
									format.Node(&resultBuf, fset, decl)
									relPath, _ := filepath.Rel(projectRoot, file)
									resultBuf.WriteString(fmt.Sprintf("\n// 文件: %s\n", relPath))
									found = true
									return false
								}
							}
						}
					}
				}
				return true
			})
			if found {
				return resultBuf.String(), nil
			}
		}

		return fmt.Sprintf("未找到符号 \"%s\" 的定义", args.Symbol), nil
	})

	add(&schema.ToolInfo{
		Name: "find_references",
		Desc: "查找某个符号在项目代码中的引用位置。支持指定符号名称。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {Type: schema.String, Desc: "要查找的符号名称", Required: true},
			"file":   {Type: schema.String, Desc: "限制在特定文件中搜索，可选"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args symbolArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Symbol == "" {
			return "错误：符号名称不能为空", nil
		}

		// Use grep-like approach: find lines where symbol is referenced,
		// excluding its own definition.
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(args.Symbol) + `\b`)

		type ref struct {
			File    string
			Line    int
			Content string
		}
		var refs []ref
		maxResults := 50

		filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() {
				if fi != nil && fi.IsDir() {
					base := fi.Name()
					if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if args.File != "" {
				if !strings.Contains(path, args.File) {
					return nil
				}
			}
			if fi.Size() > 1024*1024 {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()

			relPath, _ := filepath.Rel(projectRoot, path)
			scanner := bufio.NewScanner(f)
			lineNum := 0
			for scanner.Scan() {
				lineNum++
				line := scanner.Text()
				if re.MatchString(line) {
					// Skip if it's the definition (func ...)
					trimmed := strings.TrimSpace(line)
					if strings.HasPrefix(trimmed, "func ") &&
						strings.Contains(trimmed, args.Symbol+"(") ||
						strings.HasPrefix(trimmed, "type ") &&
							strings.Contains(trimmed, args.Symbol+" ") {
						continue
					}
					refs = append(refs, ref{
						File:    relPath,
						Line:    lineNum,
						Content: strings.TrimSpace(line),
					})
					if len(refs) >= maxResults {
						return fmt.Errorf("max results")
					}
				}
			}
			return nil
		})

		if len(refs) == 0 {
			return fmt.Sprintf("未找到 \"%s\" 的引用", args.Symbol), nil
		}

		var b strings.Builder
		b.WriteString(fmt.Sprintf("符号 \"%s\" 的引用（共 %d 处）：\n\n", args.Symbol, len(refs)))
		for _, r := range refs {
			b.WriteString(fmt.Sprintf("%s:%d  %s\n", r.File, r.Line, r.Content))
		}
		if len(refs) >= maxResults {
			b.WriteString("\n... (结果过多，仅显示前 50 条)")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "file_deps",
		Desc: "查看 Go 文件的导入依赖关系。列出文件导入的外部包和内部包。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file": {Type: schema.String, Desc: "Go 文件路径", Required: true},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args importArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.File == "" {
			return "错误：文件路径不能为空", nil
		}
		fullPath, err := safePath(args.File)
		if err != nil {
			return "错误：" + err.Error(), nil
		}
		if !strings.HasSuffix(fullPath, ".go") {
			return "错误：文件必须为 .go 文件", nil
		}
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			return fmt.Sprintf("错误：文件不存在: %s", args.File), nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, fullPath, nil, parser.ImportsOnly)
		if err != nil {
			return "", fmt.Errorf("解析文件失败: %w", err)
		}

		relPath, _ := filepath.Rel(projectRoot, fullPath)
		var b strings.Builder
		b.WriteString(fmt.Sprintf("文件: %s\n包: %s\n\n", relPath, f.Name.Name))

		var stdLibs, internalPkgs, externalPkgs []string
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, "\"")
			if imp.Name != nil {
				path = imp.Name.Name + " " + path
			}
			if strings.HasPrefix(path, "kandaoni.com/anqicms") || strings.HasPrefix(path, "kandaoni.com/anqicms/") {
				internalPkgs = append(internalPkgs, path)
			} else if strings.Contains(path, ".") {
				externalPkgs = append(externalPkgs, path)
			} else {
				stdLibs = append(stdLibs, path)
			}
		}

		if len(stdLibs) > 0 {
			b.WriteString(fmt.Sprintf("标准库 (%d)：\n", len(stdLibs)))
			for _, p := range stdLibs {
				b.WriteString(fmt.Sprintf("  - %s\n", p))
			}
			b.WriteString("\n")
		}
		if len(internalPkgs) > 0 {
			b.WriteString(fmt.Sprintf("内部包 (%d)：\n", len(internalPkgs)))
			for _, p := range internalPkgs {
				b.WriteString(fmt.Sprintf("  - %s\n", p))
			}
			b.WriteString("\n")
		}
		if len(externalPkgs) > 0 {
			b.WriteString(fmt.Sprintf("外部依赖 (%d)：\n", len(externalPkgs)))
			for _, p := range externalPkgs {
				b.WriteString(fmt.Sprintf("  - %s\n", p))
			}
			b.WriteString("\n")
		}
		return b.String(), nil
	})

	add(&schema.ToolInfo{
		Name: "call_graph",
		Desc: "分析 Go 函数的调用关系。显示一个函数调用了哪些函数（被调用者），以及哪些函数调用了它（调用者）。基于代码文本分析。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"symbol": {Type: schema.String, Desc: "函数名", Required: true},
			"file":   {Type: schema.String, Desc: "Go 文件路径，如果不指定则在所有文件中搜索"},
		}),
	}, func(ctx context.Context, argsJSON string) (string, error) {
		var args symbolArgs
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("无法解析参数: %w", err)
		}
		if args.Symbol == "" {
			return "错误：符号名称不能为空", nil
		}

		// Find the function definition
		fset := token.NewFileSet()
		type funcInfo struct {
			File    string
			Line    int
			Calls   []string
			Content string
		}
		var targetFunc *funcInfo

		filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				if fi != nil && fi.IsDir() {
					base := fi.Name()
					if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if args.File != "" && !strings.Contains(path, args.File) {
				return nil
			}
			if targetFunc != nil {
				return fmt.Errorf("done")
			}

			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil
			}

			relPath, _ := filepath.Rel(projectRoot, path)
			for _, decl := range f.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					if fd.Name.Name == args.Symbol {
						info := &funcInfo{
							File: relPath,
							Line: fset.Position(fd.Pos()).Line,
						}

						// Extract function body text
						var buf bytes.Buffer
						if fd.Doc != nil {
							buf.WriteString(fd.Doc.Text())
						}
						buf.WriteString(renderNode(fset, fd))

						info.Content = buf.String()
						targetFunc = info
						return fmt.Errorf("done")
					}
				}
			}
			return nil
		})

		if targetFunc == nil {
			return fmt.Sprintf("未找到函数 \"%s\" 的定义", args.Symbol), nil
		}

		// Extract function calls from the body using AST
		fset2 := token.NewFileSet()
		funcFile, _ := parser.ParseFile(fset2, filepath.Join(projectRoot, targetFunc.File), nil, 0)

		var callees []string
		seen := make(map[string]bool)
		for _, decl := range funcFile.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == args.Symbol {
				if fd.Body != nil {
					ast.Inspect(fd.Body, func(n ast.Node) bool {
						if ce, ok := n.(*ast.CallExpr); ok {
							switch fun := ce.Fun.(type) {
							case *ast.Ident:
								name := fun.Name
								// Skip builtins and control flow
								if !seen[name] && name != "len" && name != "cap" &&
									name != "append" && name != "copy" &&
									name != "make" && name != "new" &&
									name != "delete" && name != "close" &&
									name != "panic" && name != "recover" &&
									name != "print" && name != "println" &&
									name != "error" && name != "string" {
									// Check if it's a function call (not a type conversion)
									if !ast.IsExported(name) || len(fun.Name) > 1 {
										callees = append(callees, name)
										seen[name] = true
									}
								}
							case *ast.SelectorExpr:
								if ident, ok := fun.X.(*ast.Ident); ok {
									callees = append(callees, ident.Name+"."+fun.Sel.Name)
									seen[ident.Name+"."+fun.Sel.Name] = true
								}
							}
						}
						return true
					})
				}
			}
		}

		// Find callers (functions that call this symbol)
		type callerInfo struct {
			File string
			Line int
			Name string
		}
		var callers []callerInfo
		_ = filepath.Walk(projectRoot, func(path string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				if fi != nil && fi.IsDir() {
					base := fi.Name()
					if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if len(callers) >= 30 {
				return fmt.Errorf("max")
			}

			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return nil
			}

			relPath, _ := filepath.Rel(projectRoot, path)
			for _, decl := range f.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
					if fd.Name.Name == args.Symbol {
						continue // skip the definition itself
					}
					found := false
					ast.Inspect(fd.Body, func(n ast.Node) bool {
						if ce, ok := n.(*ast.CallExpr); ok {
							switch fun := ce.Fun.(type) {
							case *ast.Ident:
								if fun.Name == args.Symbol {
									callers = append(callers, callerInfo{
										File: relPath,
										Line: fset.Position(ce.Pos()).Line,
										Name: fd.Name.Name,
									})
									found = true
									return false
								}
							case *ast.SelectorExpr:
								if fun.Sel.Name == args.Symbol {
									callers = append(callers, callerInfo{
										File: relPath,
										Line: fset.Position(ce.Pos()).Line,
										Name: fmt.Sprintf("%s.%s", fd.Name.Name, args.Symbol),
									})
									found = true
									return false
								}
							}
						}
						return !found
					})
				}
			}
			return nil
		})

		var b strings.Builder
		b.WriteString(fmt.Sprintf("函数: %s\n文件: %s:%d\n\n", args.Symbol, targetFunc.File, targetFunc.Line))

		if len(callees) > 0 {
			b.WriteString(fmt.Sprintf("被调用者 (调用了 %d 个函数/方法)：\n", len(callees)))
			for _, c := range callees {
				b.WriteString(fmt.Sprintf("  → %s\n", c))
			}
		} else {
			b.WriteString("被调用者：无内部函数调用\n")
		}

		b.WriteString("\n")

		if len(callers) > 0 {
			b.WriteString(fmt.Sprintf("调用者 (%d 处)：\n", len(callers)))
			for _, c := range callers {
				b.WriteString(fmt.Sprintf("  ← %s (%s:%d)\n", c.Name, c.File, c.Line))
			}
		} else {
			b.WriteString("调用者：未找到\n")
		}

		return b.String(), nil
	})

	return tools, handlers
}

// renderNode renders an AST node back to formatted Go source string
func renderNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return fmt.Sprintf("%v", node)
	}
	return buf.String()
}
