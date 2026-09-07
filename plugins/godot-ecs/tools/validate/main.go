// Command validate 对 plugin.yaml 跑契约校验（manifest 形态 + 语义规则声明）。
// 输出 "contract-conformant" 表示注册时宿主校验会通过。
package main

import (
	"fmt"
	"os"

	sdk "github.com/OwnSecurityGuard/gt-plugin-sdk"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/contract"
	"github.com/OwnSecurityGuard/gt-plugin-sdk/rule"
)

func main() {
	data, err := os.ReadFile("plugin.yaml")
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	m, err := sdk.ParseManifest(data)
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	if err := sdk.ValidateManifest(m); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	// 语义规则声明层校验（gt.semantic.* 命名空间，与宿主 PluginChecker 同源）。
	if issues := rule.RulesReport(m.SemanticRules); len(issues) > 0 {
		for _, iss := range issues {
			fmt.Printf("FAIL: %s: %s (%s, %s)\n", iss.Path, iss.Message, iss.RuleID, iss.Severity)
		}
		os.Exit(1)
	}
	_ = contract.ManifestSchemaIndex(m) // schemas 声明可投影（字段缺失时由 ValidateManifest 兜底）
	fmt.Println("contract-conformant")
}
