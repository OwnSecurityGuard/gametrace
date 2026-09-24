#!/bin/sh
# bench_compare.sh —— 当前基准 vs 提交基线（bench/baseline.txt）的回归门禁。
#
# 背景：本项目核心卖点是"抓包不太掉性能"，而解码热路径（parser / reassembler /
# 语义规则求值 / 事件落库）的退化靠肉眼 review 根本发现不了。这里用 benchstat
# 做分布级比较（不是单点对单点），任一基准 vs base 增幅超过阈值即失败。
#
# 阈值：BENCH_MAX_REGRESSION（默认 +20%）。CI runner 噪声大，宁可放过小抖动。
# 可比性：baseline 必须与对比方同类机器采集；GitHub Actions 固定 ubuntu-latest，
# 首次由 maintainer 在 Linux 上 `make bench-save` 提交后此门禁才真正有意义
# （机器不匹配时数字差是环境差，不是回归）。
#
# 用法：sh scripts/bench_compare.sh   （make bench-compare）
set -eu
cd "$(dirname "$0")/.."

MAX=${BENCH_MAX_REGRESSION:-20}

if [ ! -f bench/baseline.txt ]; then
	echo "bench/baseline.txt 不存在：先 make bench-save（在与 CI 同类机器上）生成基线。"
	exit 1
fi
if ! command -v benchstat >/dev/null 2>&1; then
	echo "需要 benchstat：go install golang.org/x/perf/cmd/benchstat@latest"
	exit 1
fi

sh scripts/bench.sh

# benchstat CSV 列：,old,CI,new,CI,vs base,P —— "vs base" 形如 "+10.83%" / "-3.95%" / "~"。
benchstat -format=csv bench/baseline.txt bench/current.txt 2>/dev/null > bench/compare.csv

if awk -F, -v max="$MAX" '
NR > 1 {
	d = $6
	if (d ~ /^\+[0-9.]+%/) {
		gsub(/[%+]/, "", d)
		if (d + 0 > max) {
			printf "REGRESSION %-40s +%s%% (阈值 +%s%%)\n", $1, d, max
			bad = 1
		}
	}
}
END { exit bad ? 1 : 0 }
' bench/compare.csv; then
	echo "bench compare passed (阈值 +${MAX}%)."
	rm -f bench/compare.csv
else
	echo
	echo "基准回归：见上表。若为预期内的功能代价，review 后 make bench-save 更新基线并注明原因。"
	rm -f bench/compare.csv
	exit 1
fi
