package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skillURIPrefix 是技能 resource 的 URI 前缀：每个技能注册为
// gametrace://skills/<name>，AI agent 可在任务匹配时读取其 SKILL.md 内容。
const skillURIPrefix = "gametrace://skills/"

// SkillInfo 是 skills/ 目录下一个技能（SKILL.md）的目录条目，
// 由 get_capabilities 作为 Skill Catalog 暴露给 AI agent，
// 让它在接入初始化阶段就了解平台有哪些可复用的方法论。
type SkillInfo struct {
	// Name 是技能名，取自目录名（<skills>/<name>/），可被 frontmatter 的 name 覆盖。
	Name string `json:"name"`
	// Path 是该技能 SKILL.md 所在目录的绝对路径。
	Path string `json:"path"`
	// Description 是 frontmatter 的 description（含名称、用途与触发场景）。
	Description string `json:"description"`
	// URI 是 MCP resource 地址（gametrace://skills/<name>）。
	URI string `json:"uri"`
}

// skillResourceURI 返回技能对应的 MCP resource URI。
func skillResourceURI(name string) string {
	return skillURIPrefix + name
}

// readSkillMarkdown 读取技能的 SKILL.md 原文（resource 内容）。
func readSkillMarkdown(skill SkillInfo) (string, error) {
	b, err := os.ReadFile(filepath.Join(skill.Path, "SKILL.md"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildSkillInstructions 依据 Skill Catalog 生成 MCP 服务器的初始化 instructions：
// 概览可用技能工作流及对应 resource URI，指引 AI agent 在任务匹配时先读技能。
//
// instructions 里恒定声明插件源码的归属：插件源码由 Agent 在自己的 workspace 创建，
// 平台既不保存也不代写 —— 这是「平台侧是否持有用户插件代码」这个问题的直接答案，
// 必须在初始化阶段就说清楚，而不是等 Agent 调了工具才猜。
func buildSkillInstructions(skills []SkillInfo) string {
	var b strings.Builder
	b.WriteString("You are connected to GameTrace MCP.")
	b.WriteString("\n\nPlugin source code is created in YOUR workspace: GameTrace does not store plugin source code,")
	b.WriteString("\nnor does it compile or launch plugins. Plugins run on your machine and register out to the platform.")
	if len(skills) == 0 {
		b.WriteString("\n\nNo skill workflows are available on this server.")
		return b.String()
	}
	b.WriteString("\n\nAvailable agent workflows:")
	for _, s := range skills {
		fmt.Fprintf(&b, "\n\n- %s\n  Resource:\n  %s", s.Name, skillResourceURI(s.Name))
	}
	b.WriteString("\n\nWhen a user request matches a workflow,\nread the corresponding skill resource before execution.")
	return b.String()
}

// skillDirs 按优先级返回候选的技能根目录：
//  1. GT_SKILLS_DIR：显式配置（跨机 / 自定义技能仓库时覆盖）；
//  2. $GT_AGENT_SRC_DIR/skills：Docker 镜像内源码根 /src/skills（runtime 阶段
//     COPY --from=builder /src /src 已包含，无需再打包）；
//  3. skills：本地开发（cwd 为仓库根）。
//
// 命中顺序即优先级：同名技能以高优先级目录为准。
func skillDirs() []string {
	var dirs []string
	add := func(d string) {
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	add(os.Getenv("GT_SKILLS_DIR"))
	add(filepath.Join(os.Getenv("GT_AGENT_SRC_DIR"), "skills"))
	add("skills")
	return dirs
}

// loadSkillCatalog 扫描候选技能根目录，为每个含 SKILL.md 的子目录生成目录条目。
// 所有目录都缺失 / 无法解析时返回空切片（不报错）：get_capabilities 降级为
// 仅暴露工具目录，不影响其他功能。
func loadSkillCatalog() []SkillInfo {
	var skills []SkillInfo
	seen := make(map[string]bool)
	for _, dir := range skillDirs() {
		for _, info := range loadSkillCatalogFrom(dir) {
			if seen[info.Name] {
				continue
			}
			info.URI = skillResourceURI(info.Name)
			skills = append(skills, info)
			seen[info.Name] = true
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

// loadSkillCatalogFrom 扫描单个技能根目录；root 不存在或不可读时返回空切片。
func loadSkillCatalogFrom(root string) []SkillInfo {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var skills []SkillInfo
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, ok := parseSkillDir(filepath.Join(root, e.Name())); ok {
			skills = append(skills, info)
		}
	}
	return skills
}

// parseSkillDir 读取 <skillDir>/SKILL.md 并解析 frontmatter；
// SKILL.md 缺失或 frontmatter 不完整时返回 false。
func parseSkillDir(skillDir string) (SkillInfo, bool) {
	b, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return SkillInfo{}, false
	}
	info, ok := parseSkillFrontmatter(skillDir, string(b))
	if ok {
		info.Path = skillDir
	}
	return info, ok
}

// parseSkillFrontmatter 解析 SKILL.md 首部的 `---` 围栏：
//   - name：frontmatter 提供时覆盖目录名；
//   - description：缺失视为无效技能条目（无描述无助于 agent 决策）。
func parseSkillFrontmatter(skillDir, content string) (SkillInfo, bool) {
	info := SkillInfo{Name: filepath.Base(skillDir)}
	rest := strings.TrimLeft(strings.TrimPrefix(content, "\ufeff"), " \t\r\n")
	if !strings.HasPrefix(rest, "---") {
		return info, false
	}
	rest = rest[3:]
	idx := strings.Index(rest, "---")
	if idx < 0 {
		return info, false
	}
	section := rest[:idx]
	info.Name = frontmatterScalar(section, "name", info.Name)
	info.Description = frontmatterScalar(section, "description", "")
	return info, info.Description != ""
}

// frontmatterScalar 提取 `key: value` 行中的值，去掉首尾空白与成对引号。
func frontmatterScalar(section, key, fallback string) string {
	prefix := key + ":"
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		v = strings.Trim(v, `"'`)
		if v != "" {
			return v
		}
	}
	return fallback
}