// ProjectRuleForm — 项目「检查规则」的结构化编辑表单（v1）。
//
// 一条检查规则 = 命中条件 (when) + 通知文案 + 冷却/上下文窗口参数。命中后平台把
// 「触发记录 + 各方向触发前最近 N 条解码记录」下发给抓到该数据的探针本地展示。
//
// 条件支持两种录入：
//   · 简单模式：单叶子条件（path + op + value），覆盖绝大多数场景；
//   · JSON 高级：直接填 when 对象/数组（all/any 组合），不做可视化树。
//
// 提交前做即时校验，最终以后端 set_project_rules 的整表校验为准（错误文案回显）。
import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import type { ProjectRule, RuleOp, RulePredicate } from "@/types/project";

// 算子闭集（与 sdk/rule.Predicate 的 Op 一致）及中文标签。
const OP_OPTIONS: { value: RuleOp; label: string }[] = [
  { value: "eq", label: "等于 (eq)" },
  { value: "neq", label: "不等于 (neq)" },
  { value: "exists", label: "存在 (exists)" },
  { value: "not_exists", label: "不存在 (not_exists)" },
  { value: "gt", label: "大于 (gt)" },
  { value: "gte", label: "大于等于 (gte)" },
  { value: "lt", label: "小于 (lt)" },
  { value: "lte", label: "小于等于 (lte)" },
  { value: "in", label: "在列表中 (in)" },
  { value: "not_in", label: "不在列表中 (not_in)" },
  { value: "contains", label: "包含 (contains)" },
  { value: "prefix", label: "前缀 (prefix)" },
  { value: "suffix", label: "后缀 (suffix)" },
];

// 需要 value 字面量的算子（exists/not_exists 不需要）。
const NEEDS_VALUE = new Set<RuleOp>([
  "eq",
  "neq",
  "gt",
  "gte",
  "lt",
  "lte",
  "in",
  "not_in",
  "contains",
  "prefix",
  "suffix",
]);
// value 必须是数组的算子。
const NEEDS_ARRAY = new Set<RuleOp>(["in", "not_in"]);

function uid(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) return crypto.randomUUID();
  return `rule-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

// parseValueLiteral 把 value 文本解析成字面量：优先 JSON（数字/布尔/数组/对象/null），
// 失败回退为原始字符串。in/not_in 期望数组，非数组时返回错误。
function parseValueLiteral(
  text: string,
  op: RuleOp,
): { value?: unknown; error?: string } {
  const trimmed = text.trim();
  if (!NEEDS_VALUE.has(op)) return {};
  if (trimmed === "") return { error: "该算子需要填写比较值" };
  let parsed: unknown = trimmed;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    parsed = trimmed; // 非 JSON：当作字符串字面量
  }
  if (NEEDS_ARRAY.has(op) && !Array.isArray(parsed)) {
    return { error: `${op} 的值必须是 JSON 数组，例如 ["a","b"]` };
  }
  return { value: parsed };
}

// predicateFromSimple 由简单模式字段拼出叶子谓词。
function leafPredicate(path: string, op: RuleOp, value?: unknown): RulePredicate {
  const p: RulePredicate = { path: path.trim(), op };
  if (NEEDS_VALUE.has(op)) p.value = value;
  return p;
}

export interface ProjectRuleFormProps {
  /** 编辑既有规则时传入；新增时为 undefined。 */
  initial?: ProjectRule;
  pending?: boolean;
  /** 提交一条组装好的规则（父组件负责整表替换 + 调用 mutation）。 */
  onSubmit: (rule: ProjectRule) => void;
  onCancel?: () => void;
}

export function ProjectRuleForm({ initial, pending, onSubmit, onCancel }: ProjectRuleFormProps) {
  const editing = !!initial;
  // 既有规则的 when 若含 all/any（非单叶子），默认进 JSON 模式。
  const initialWhen = initial?.when;
  const isLeaf =
    !!initialWhen && !initialWhen.all?.length && !initialWhen.any?.length && !!initialWhen.op;

  const [name, setName] = useState(initial?.name ?? "");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [mode, setMode] = useState<"simple" | "json">(isLeaf ? "simple" : initialWhen ? "json" : "simple");
  const [path, setPath] = useState(isLeaf ? (initialWhen?.path ?? "") : "");
  const [op, setOp] = useState<RuleOp>(isLeaf ? (initialWhen?.op as RuleOp) : "eq");
  const [valueText, setValueText] = useState(
    isLeaf && initialWhen?.value !== undefined ? stringifyValue(initialWhen.value) : "",
  );
  const [whenJson, setWhenJson] = useState(
    initialWhen && !isLeaf ? JSON.stringify(initialWhen, null, 2) : "",
  );
  const [title, setTitle] = useState(initial?.title ?? "");
  const [message, setMessage] = useState(initial?.message ?? "");
  const [cooldown, setCooldown] = useState(
    initial?.cooldown_sec != null ? String(initial.cooldown_sec) : "",
  );
  const [context, setContext] = useState(
    initial?.context_per_direction != null ? String(initial.context_per_direction) : "",
  );
  const [err, setErr] = useState("");

  function handleSubmit() {
    setErr("");
    const trimmedName = name.trim();
    if (!trimmedName) {
      setErr("请填写规则名称");
      return;
    }

    let when: RulePredicate;
    if (mode === "simple") {
      if (!path.trim()) {
        setErr("请填写字段路径 (path)，例如 type 或 data.targetId");
        return;
      }
      const { value, error } = parseValueLiteral(valueText, op);
      if (error) {
        setErr(error);
        return;
      }
      when = leafPredicate(path, op, value);
    } else {
      const raw = whenJson.trim();
      if (!raw) {
        setErr("JSON 高级条件不能为空（或切回简单模式）");
        return;
      }
      try {
        when = JSON.parse(raw) as RulePredicate;
      } catch (e) {
        setErr(`JSON 条件解析失败：${e instanceof Error ? e.message : String(e)}`);
        return;
      }
      if (typeof when !== "object" || when === null || Array.isArray(when)) {
        // 允许数组简写（隐式 all）——后端 UnmarshalJSON 支持。
        if (!Array.isArray(when)) {
          setErr("JSON 条件必须是对象或数组");
          return;
        }
      }
    }

    const rule: ProjectRule = {
      id: initial?.id || uid(),
      name: trimmedName,
      enabled,
      when,
    };
    if (title.trim()) rule.title = title.trim();
    if (message.trim()) rule.message = message.trim();
    if (cooldown.trim() !== "") {
      const n = Number(cooldown.trim());
      if (!Number.isFinite(n) || n < 0) {
        setErr("冷却秒数必须是非负数字");
        return;
      }
      rule.cooldown_sec = n;
    }
    if (context.trim() !== "") {
      const n = Number(context.trim());
      if (!Number.isFinite(n) || n < 0 || !Number.isInteger(n)) {
        setErr("每方向上下文条数必须是非负整数");
        return;
      }
      rule.context_per_direction = n;
    }
    onSubmit(rule);
  }

  const needsValue = NEEDS_VALUE.has(op);

  return (
    <div className="mt-3 space-y-3 border-t border-border pt-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-foreground">
          {editing ? "编辑检查规则" : "新增检查规则"}
        </span>
        <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
            className="h-3.5 w-3.5"
          />
          启用
        </label>
      </div>

      <div className="grid gap-2 sm:grid-cols-2">
        <label className="grid gap-1">
          <span className="text-xs text-muted-foreground">规则名称</span>
          <Input
            name="rule-name"
            size="sm"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：登录消息命中"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-xs text-muted-foreground">条件模式</span>
          <Select
            size="sm"
            value={mode}
            onChange={(e) => setMode(e.target.value as "simple" | "json")}
          >
            <option value="simple">简单条件（单字段）</option>
            <option value="json">JSON 高级条件（all/any）</option>
          </Select>
        </label>
      </div>

      {mode === "simple" ? (
        <div className="grid gap-2 sm:grid-cols-[1fr_auto]">
          <label className="grid gap-1">
            <span className="text-xs text-muted-foreground">字段路径 (GJSON path)</span>
            <Input
              name="rule-path"
              size="sm"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="type / data.targetId / _meta.msg_name"
            />
          </label>
          <div className="grid gap-2 sm:grid-cols-2 sm:col-span-1">
            <label className="grid gap-1">
              <span className="text-xs text-muted-foreground">算子</span>
              <Select
                size="sm"
                value={op}
                onChange={(e) => setOp(e.target.value as RuleOp)}
              >
                {OP_OPTIONS.map((o) => (
                  <option key={o.value} value={o.value}>
                    {o.label}
                  </option>
                ))}
              </Select>
            </label>
            <label className="grid gap-1">
              <span className="text-xs text-muted-foreground">
                比较值{needsValue ? "" : "（此算子无需填）"}
              </span>
              <Input
                name="rule-value"
                size="sm"
                value={valueText}
                disabled={!needsValue}
                onChange={(e) => setValueText(e.target.value)}
                placeholder={
                  NEEDS_ARRAY.has(op) ? '["a","b"]' : '"login" / 42 / true'
                }
              />
            </label>
          </div>
        </div>
      ) : (
        <label className="grid gap-1">
          <span className="text-xs text-muted-foreground">
            when 条件 JSON（对象或数组；数组为隐式 all）
          </span>
          <textarea
            value={whenJson}
            onChange={(e) => setWhenJson(e.target.value)}
            rows={5}
            spellCheck={false}
            className="w-full rounded-md border border-input bg-transparent p-2 font-mono text-xs shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            placeholder={'{\n  "all": [\n    { "path": "type", "op": "eq", "value": "login" },\n    { "path": "data.code", "op": "neq", "value": 0 }\n  ]\n}'}
          />
        </label>
      )}

      <div className="grid gap-2 sm:grid-cols-2">
        <label className="grid gap-1">
          <span className="text-xs text-muted-foreground">通知标题（可选）</span>
          <Input
            name="rule-title"
            size="sm"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="留空回退规则名称"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-xs text-muted-foreground">通知正文（可选）</span>
          <Input
            name="rule-message"
            size="sm"
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder="留空用默认文案"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-xs text-muted-foreground">冷却秒数（同规则·同会话）</span>
          <Input
            name="rule-cooldown"
            size="sm"
            inputMode="numeric"
            value={cooldown}
            onChange={(e) => setCooldown(e.target.value)}
            placeholder="默认 30"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-xs text-muted-foreground">每方向上下文条数</span>
          <Input
            name="rule-context"
            size="sm"
            inputMode="numeric"
            value={context}
            onChange={(e) => setContext(e.target.value)}
            placeholder="默认 3"
          />
        </label>
      </div>

      {err && <p className="text-xs text-destructive">{err}</p>}

      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={handleSubmit}
          disabled={pending}
          className="inline-flex h-9 items-center justify-center rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground disabled:opacity-50"
        >
          {editing ? "保存修改" : "添加规则"}
        </button>
        {onCancel && (
          <button
            type="button"
            onClick={onCancel}
            disabled={pending}
            className="inline-flex h-9 items-center justify-center rounded-md border border-border px-3 text-sm text-muted-foreground hover:text-foreground disabled:opacity-50"
          >
            取消
          </button>
        )}
      </div>
    </div>
  );
}

// stringifyValue 把已有 value 字面量还原成输入框文本（字符串加引号以便区分类型）。
function stringifyValue(v: unknown): string {
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}
