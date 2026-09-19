package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillFrontmatter(t *testing.T) {
	ok := `---
name: "decoder-plugin-guide"
description: "指引用户编写解码插件。当用户要为自定义协议写解码插件时调用。"
---

# 正文
`
	info, valid := parseSkillFrontmatter("/skills/decoder-plugin-guide", ok)
	if !valid {
		t.Fatalf("expected valid skill, got invalid")
	}
	if info.Name != "decoder-plugin-guide" {
		t.Errorf("name = %q, want decoder-plugin-guide", info.Name)
	}
	if info.Description != "指引用户编写解码插件。当用户要为自定义协议写解码插件时调用。" {
		t.Errorf("description = %q", info.Description)
	}

	// frontmatter 缺 description → 技能条目无效。
	noDesc := "---\nname: foo\n---\nbody\n"
	if _, valid := parseSkillFrontmatter("/skills/foo", noDesc); valid {
		t.Errorf("expected invalid when description missing")
	}

	// 无 frontmatter 围栏 → 无效。
	if _, valid := parseSkillFrontmatter("/skills/bar", "# no frontmatter\n"); valid {
		t.Errorf("expected invalid without frontmatter fence")
	}

	// frontmatter 未闭合 → 无效。
	if _, valid := parseSkillFrontmatter("/skills/baz", "---\nname: x\n"); valid {
		t.Errorf("expected invalid with unclosed fence")
	}

	// 目录名兜底：frontmatter 无 name 时用目录名。单引号值也可解析。
	noName := "---\ndescription: '单引号描述'\n---\n"
	info, valid = parseSkillFrontmatter("/skills/from-dir", noName)
	if !valid {
		t.Fatalf("expected valid")
	}
	if info.Name != "from-dir" {
		t.Errorf("fallback name = %q, want from-dir", info.Name)
	}
	if info.Description != "单引号描述" {
		t.Errorf("description = %q, want 单引号描述", info.Description)
	}
}

func TestLoadSkillCatalogFrom(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "alpha-desc")
	writeSkill(t, root, "beta", "beta-desc")
	// 无 SKILL.md 的目录应被忽略。
	if err := os.MkdirAll(filepath.Join(root, "no-md"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 隐藏目录应被忽略。
	writeSkill(t, root, ".hidden", "ignored")

	skills := loadSkillCatalogFrom(root)
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2: %+v", len(skills), skills)
	}
	got := map[string]string{}
	for _, s := range skills {
		got[s.Name] = s.Description
	}
	if got["alpha"] != "alpha-desc" || got["beta"] != "beta-desc" {
		t.Errorf("catalog = %v", got)
	}

	// 目录不存在 → 空切片而非报错。
	if skills := loadSkillCatalogFrom(filepath.Join(root, "nope")); len(skills) != 0 {
		t.Errorf("expected empty catalog for missing dir, got %+v", skills)
	}
}

func TestLoadSkillCatalogPriorityAndDedupe(t *testing.T) {
	high := t.TempDir()
	lowRoot := t.TempDir()
	low := filepath.Join(lowRoot, "skills")
	// 低优先级目录里包含同名 + 独有技能。
	writeSkill(t, low, "shared", "low-version")
	writeSkill(t, low, "only-low", "only-low-desc")

	t.Setenv("GT_SKILLS_DIR", high)
	t.Setenv("GT_AGENT_SRC_DIR", lowRoot)
	// ./skills 解析路径：go test 的 cwd 是 cmd/gt-mcp，仓库根 skills 不会串进来。

	writeSkill(t, high, "shared", "high-version")

	skills := loadSkillCatalog()
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2: %+v", len(skills), skills)
	}
	var shared, onlyLow *SkillInfo
	for i, s := range skills {
		switch s.Name {
		case "shared":
			shared = &skills[i]
		case "only-low":
			onlyLow = &skills[i]
		}
	}
	if shared == nil || onlyLow == nil {
		t.Fatalf("catalog = %+v", skills)
	}
	// 同名技能以高优先级目录为准。
	if shared.Description != "high-version" {
		t.Errorf("shared.Description = %q, want high-version (priority)", shared.Description)
	}
	// 独有技能仍从低优先级目录补充。
	if onlyLow.Description != "only-low-desc" {
		t.Errorf("onlyLow.Description = %q", onlyLow.Description)
	}

	// 结果按名称排序。
	if skills[0].Name > skills[1].Name {
		t.Errorf("catalog not sorted: %v, %v", skills[0].Name, skills[1].Name)
	}
}

// TestBuildSkillInstructions 验证 instructions 生成与 skill resource URI 约定。
func TestBuildSkillInstructions(t *testing.T) {
	skills := []SkillInfo{
		{Name: "protocol-analysis", Description: "d", URI: skillResourceURI("protocol-analysis")},
		{Name: "loadtest-generation", Description: "d", URI: skillResourceURI("loadtest-generation")},
	}
	got := buildSkillInstructions(skills)
	for _, want := range []string{
		"You are connected to GameTrace MCP.",
		"Available agent workflows:",
		"- protocol-analysis",
		"gametrace://skills/protocol-analysis",
		"- loadtest-generation",
		"gametrace://skills/loadtest-generation",
		"When a user request matches a workflow,\nread the corresponding skill resource before execution.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions missing %q\n%s", want, got)
		}
	}

	// 空目录降级：不报错并明确提示无可用技能。
	empty := buildSkillInstructions(nil)
	if !strings.Contains(empty, "No skill workflows are available") {
		t.Errorf("empty instructions = %q", empty)
	}
}

func TestSkillResourceURI(t *testing.T) {
	if got := skillResourceURI("protocol-analysis"); got != "gametrace://skills/protocol-analysis" {
		t.Errorf("skillResourceURI = %q", got)
	}
}

func TestReadSkillMarkdown(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "alpha", "alpha-desc")
	skill, ok := parseSkillDir(filepath.Join(root, "alpha"))
	if !ok {
		t.Fatal("parseSkillDir failed")
	}
	md, err := readSkillMarkdown(skill)
	if err != nil {
		t.Fatalf("readSkillMarkdown: %v", err)
	}
	if !strings.Contains(md, "alpha-desc") {
		t.Errorf("markdown = %q", md)
	}

	// 目录不存在 → 报错而非 panic。
	if _, err := readSkillMarkdown(SkillInfo{Path: filepath.Join(root, "nope")}); err == nil {
		t.Errorf("expected error for missing dir")
	}
}

// writeSkill 在 root/<name>/ 下写入 SKILL.md（含 frontmatter）。
func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: \"" + name + "\"\ndescription: \"" + desc + "\"\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}