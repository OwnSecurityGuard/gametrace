#!/bin/sh
# coverage_gate.sh —— 核心包覆盖率地板门禁（防回退）。
#
# 定位：不是"唯覆盖率论"，而是防止核心包测试被删/被旁路后无人察觉。
# 门槛 = 当前实测地板（约留 5 个百分点缓冲），提升目标参考：
#   sdk/rule、pkg/event 语义规则/事件层 → 80%+
#   pkg/plugin 插件运行时 → 70%+
# 已知欠账（暂不入表，补测试后逐个加入）：
#   pkg/event 0%（纯类型包，宿主事件模型无独立单测）
#   sdk/event 8%、pkg/store 38%（大 IO 面，短期只设低保）
#
# 用法：sh scripts/coverage_gate.sh   （CI 与本地同一入口：make coverage-gate）
set -u
cd "$(dirname "$0")/.."

fail=0

# check <模块目录> <build tags> <包> <门槛%>
check() {
	dir=$1; tags=$2; pkg=$3; min=$4
	out=$(cd "$dir" && go test -tags "$tags" -cover "$pkg" 2>/dev/null | grep -oE 'coverage: [0-9.]+' | grep -oE '[0-9.]+')
	if [ -z "$out" ]; then
		echo "FAIL $dir $pkg: 取不到覆盖率（构建/测试失败？）"
		fail=1
		return
	fi
	ok=$(awk -v got="$out" -v min="$min" 'BEGIN{print (got+0 >= min+0) ? 1 : 0}')
	if [ "$ok" = "1" ]; then
		echo "ok   $dir $pkg: ${out}% >= ${min}%"
	else
		echo "FAIL $dir $pkg: ${out}% < ${min}%"
		fail=1
	fi
}

# ---- 根模块（长期运行核心：插件运行时 / 解码调度 / spool / 存储）----
check . pcap ./pkg/plugin/ 70
check . pcap ./pkg/decode/ 75
check . pcap ./pkg/spool/ 75
check . pcap ./pkg/store/ 33

# ---- SDK 子模块（外部插件消费的全部契约面）----
# sdk 根包：注册循环/隧道/清单校验，TestSimulate 之外的插件侧热区。
check ./sdk . ./ 74
check ./sdk . ./rule/ 75
check ./sdk . ./framing/ 68
check ./sdk . ./contract/ 50

# ---- 示例插件模块（SDK 用户模板，必须保持可编译可测试）----
for m in examples/http-decoder examples/lp-decoder examples/ws-decoder sdk/examples/http-stream-decoder; do
	out=$(cd "$m" && go test -cover ./... 2>/dev/null | grep -oE 'coverage: [0-9.]+' | grep -oE '[0-9.]+')
	if [ -z "$out" ]; then
		echo "FAIL $m: 测试未通过或取不到覆盖率"
		fail=1
	else
		echo "ok   $m: coverage ${out}%"
	fi
done

if [ "$fail" != "0" ]; then
	echo
	echo "coverage gate 失败：核心包覆盖率跌破地板，或相关模块测试不通过。"
	exit 1
fi
echo
echo "coverage gate passed."
