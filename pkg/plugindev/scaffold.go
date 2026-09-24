package plugindev

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates/scaffold_plugin/*.tmpl
var scaffoldTemplates embed.FS

// scaffoldTemplateName 是脚手架模板的标识，随结果返回给调用方。
const scaffoldTemplateName = "decoder-plugin-template"

// scaffoldOutputs 固定「模板文件 -> 输出文件」映射，顺序即返回的 Files 顺序。
var scaffoldOutputs = []struct{ tmpl, file string }{
	{"go.mod.tmpl", "go.mod"},
	{"main.go.tmpl", "main.go"},
	{"plugin.yaml.tmpl", "plugin.yaml"},
}

// renderScaffoldTemplates 渲染脚手架模板，返回 模板文件名 -> 渲染后内容。
//
// 模板数据键：Name、Protocol、ProtocolVersion（可选）、Hints（可选 []string）、
// SDKVersion、FramingAvailable。生成的项目只依赖已发布的
// github.com/OwnSecurityGuard/gametrace/sdk 模块，不含任何 source-relative
// replace，因此只要 SDK 模块可达就能在任意位置构建。
func renderScaffoldTemplates(data map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(scaffoldOutputs))
	for _, o := range scaffoldOutputs {
		raw, err := scaffoldTemplates.ReadFile("templates/scaffold_plugin/" + o.tmpl)
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", o.tmpl, err)
		}
		t, err := template.New(o.tmpl).Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", o.tmpl, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("render template %s: %w", o.tmpl, err)
		}
		out[o.tmpl] = buf.String()
	}
	return out, nil
}

// ScaffoldPlugin 渲染解码插件脚手架，并**只返回文件内容，不写任何文件**。
//
// 平台不持有用户插件源码：调用方（Agent）必须把 Contents 写入自己的 workspace，
// 再在本地构建、运行插件，最后用 connect_plugin 告知平台「插件已经跑起来了」。
// 返回的 Files 是相对路径（go.mod / main.go / plugin.yaml），顺序稳定。
func ScaffoldPlugin(name, protocol, protocolVersion string, hints []string) (*ScaffoldResult, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if protocol == "" {
		return nil, fmt.Errorf("protocol is required")
	}

	rendered, err := renderScaffoldTemplates(map[string]any{
		"Name":             name,
		"Protocol":         protocol,
		"ProtocolVersion":  protocolVersion,
		"Hints":            hints,
		"SDKVersion":       SDKVersion,
		"FramingAvailable": FramingAvailable,
	})
	if err != nil {
		return nil, err
	}

	res := &ScaffoldResult{
		Template:         scaffoldTemplateName,
		Files:            make([]string, 0, len(scaffoldOutputs)),
		Contents:         make(map[string]string, len(scaffoldOutputs)),
		SDKVersion:       SDKVersion,
		FramingAvailable: FramingAvailable,
	}
	for _, o := range scaffoldOutputs {
		res.Files = append(res.Files, o.file)
		res.Contents[o.file] = rendered[o.tmpl]
	}
	return res, nil
}