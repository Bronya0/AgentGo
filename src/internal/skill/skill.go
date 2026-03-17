// Package skill 实现 SKILL.md 加载和注入。
//
// Skill 是通过 Markdown 文件定义的能力描述，包含 YAML frontmatter 元数据。
// Agent 在构建 system prompt 时，将匹配的 skill 描述注入提示词。
//
// SKILL.md 格式：
//
//	---
//	name: my-skill
//	description: 一句话描述
//	always: false
//	requires:
//	  bins: [git]
//	---
//	<Markdown 正文：详细指令>
package skill

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Metadata 是 SKILL.md 的 frontmatter 结构。
type Metadata struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Always      bool     `yaml:"always"`       // 是否始终注入 system prompt
	Requires    Requires `yaml:"requires"`
}

// Requires 描述 skill 的运行前置条件。
type Requires struct {
	Bins []string `yaml:"bins"` // 必须存在的可执行文件
	Env  []string `yaml:"env"`  // 必须非空的环境变量
}

// Skill 是一个解析后的 skill。
type Skill struct {
	Metadata Metadata
	Body     string // Markdown 正文
	FilePath string // 源文件路径
}

// LoadDir 从目录中加载所有 SKILL.md 文件。
func LoadDir(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skill dir %q: %w", dir, err)
	}

	var skills []Skill
	for _, e := range entries {
		if e.IsDir() {
			// 子目录中查找 SKILL.md
			fp := filepath.Join(dir, e.Name(), "SKILL.md")
			if s, err := LoadFile(fp); err == nil {
				if s.Metadata.Name == "" {
					s.Metadata.Name = e.Name()
				}
				skills = append(skills, *s)
			}
		} else if strings.EqualFold(e.Name(), "SKILL.md") {
			fp := filepath.Join(dir, e.Name())
			if s, err := LoadFile(fp); err == nil {
				skills = append(skills, *s)
			}
		}
	}
	return skills, nil
}

// LoadFile 从单个文件解析 SKILL.md。
func LoadFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	meta, body, err := parseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}

	return &Skill{
		Metadata: meta,
		Body:     body,
		FilePath: path,
	}, nil
}

// parseFrontmatter 分离 YAML frontmatter 和 Markdown 正文。
func parseFrontmatter(content string) (Metadata, string, error) {
	var meta Metadata

	if !strings.HasPrefix(content, "---") {
		// 没有 frontmatter，整体作为 body
		return meta, content, nil
	}

	// 找到第二个 ---
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return meta, content, nil
	}

	yamlPart := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:])

	if err := yaml.Unmarshal([]byte(yamlPart), &meta); err != nil {
		return meta, body, fmt.Errorf("yaml parse: %w", err)
	}

	return meta, body, nil
}

// CheckEligibility 检查 skill 的前置条件是否满足。
func (s *Skill) CheckEligibility() error {
	for _, bin := range s.Metadata.Requires.Bins {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("required binary %q not found", bin)
		}
	}
	for _, env := range s.Metadata.Requires.Env {
		if os.Getenv(env) == "" {
			return fmt.Errorf("required env var %q not set", env)
		}
	}
	return nil
}

// BuildPromptSection 将多个 skill 构建为 system prompt 中的 skills 区块。
// maxChars 限制总字符数。
func BuildPromptSection(skills []Skill, maxChars int) string {
	var sb strings.Builder
	totalChars := 0

	for _, s := range skills {
		// 检查前置条件
		if err := s.CheckEligibility(); err != nil {
			continue
		}

		entry := fmt.Sprintf("### %s\n%s\n\n%s\n\n", s.Metadata.Name, s.Metadata.Description, s.Body)
		if totalChars+len(entry) > maxChars && totalChars > 0 {
			break
		}
		sb.WriteString(entry)
		totalChars += len(entry)
	}

	return sb.String()
}

// FilterAlways 返回 always=true 的 skill 列表。
func FilterAlways(skills []Skill) []Skill {
	var out []Skill
	for _, s := range skills {
		if s.Metadata.Always {
			out = append(out, s)
		}
	}
	return out
}
