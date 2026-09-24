#!/bin/sh
# bench.sh —— 采集全仓基准套件到 bench/current.txt（--save 覆盖基线）。
#
# 基准面 = "抓包性能下降"类故障的高发区：
#   sdk/framing        ExtractL7 解封装 + Reassembler 乱序重排（每帧一次）
#   sdk/rule           语义规则求值（每条事件一次：GJSON→Predicate→Effect）
#   pkg/store          事件批量落库（pipeline 每秒 flush 的写路径）
#   examples/ws-decoder 帧解析（插件侧 parser 热路径代表）
#
# 可比性约定：baseline 与对比必须在同一类机器采集（CI 固定 ubuntu-latest）。
# 换机器/换环境后先 `make bench-save` 重录基线，再谈回归。
set -eu
cd "$(dirname "$0")/.."
mkdir -p bench

# benchcount：CI 用 6 次取分布（benchstat 需要样本量>1），本地快跑可 BENCH_COUNT=2。
BENCH_COUNT=${BENCH_COUNT:-6}

# run <目录> <go test 包参数...>：只保留纯数值的 Benchmark 结果行
# （pkg/store 等包会往 stdout 打 slog 日志，混进行内会破坏 benchstat 解析）。
run() {
	dir=$1; shift
	(cd "$dir" && go test -run '^$' -bench . -benchmem -count="$BENCH_COUNT" "$@" 2>/dev/null) |
		grep -E '^Benchmark[^ ]*[[:space:]]+[0-9]+[[:space:]]+[0-9.]+ ns/op' || true
}

out=bench/current.txt
[ "${1:-}" = "--save" ] && out=bench/baseline.txt

{
	run . ./pkg/store/
	run ./sdk ./rule/ ./framing/
	run ./examples/ws-decoder .
} > "$out"

echo "wrote $out ($(grep -c '^Benchmark' "$out") benchmark results)"
