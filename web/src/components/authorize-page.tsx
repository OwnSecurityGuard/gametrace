/**
 * OAuth 授权页：AI agent 无 token 调 MCP 时由客户端自动拉起的浏览器深链接
 * （/oauth/authorize）。三态：登录卡（输入现有令牌）→ 确认卡（同意/拒绝）→
 * 跳回 agent 的本地回调。仅允许已有用户（无页内注册——新用户先去主应用注册）。
 *
 * 登录即复用 lib/auth 的 token 存储：授权完成后同一浏览器打开主应用也已登录。
 */
import { useEffect, useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { getToken, setToken } from "@/lib/auth";
import {
  buildCallbackUrl,
  fetchAuthorizeInfo,
  postApprove,
  OAuthBadRequestError,
  OAuthUnauthorizedError,
  type ApproveResult,
  type AuthorizeInfo,
} from "@/lib/oauth";
import { KeyRound, ShieldAlert, ShieldCheck, Loader2, Bot, ExternalLink } from "lucide-react";

/** 页面状态机：加载 → (致命错误 | 登录卡 | 确认卡)。 */
type Phase = "loading" | "error" | "login" | "confirm";

/** 从 authorize URL 提取并原样转交 approve 的参数（服务端会重新校验）。 */
function readAuthParams(): {
  client_id: string;
  redirect_uri: string;
  code_challenge: string;
  code_challenge_method: string;
  state: string;
} {
  const q = new URLSearchParams(window.location.search);
  return {
    client_id: q.get("client_id") ?? "",
    redirect_uri: q.get("redirect_uri") ?? "",
    code_challenge: q.get("code_challenge") ?? "",
    code_challenge_method: q.get("code_challenge_method") ?? "S256",
    state: q.get("state") ?? "",
  };
}

export function AuthorizePage() {
  const [phase, setPhase] = useState<Phase>("loading");
  const [info, setInfo] = useState<AuthorizeInfo | null>(null);
  const [fatalMessage, setFatalMessage] = useState("");
  const [tokenInput, setTokenInput] = useState("");
  const [loginError, setLoginError] = useState("");
  const [busy, setBusy] = useState(false);
  const authParams = readAuthParams();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const data = await fetchAuthorizeInfo(
          new URLSearchParams(window.location.search),
          getToken(),
        );
        if (cancelled) return;
        setInfo(data);
        setPhase(data.owner ? "confirm" : "login");
      } catch (e) {
        if (cancelled) return;
        setFatalMessage(e instanceof Error ? e.message : String(e));
        setPhase("error");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  /** 登录卡提交：保存 token 后用 authorize-info 验证有效性。 */
  async function handleLogin(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const token = tokenInput.trim();
    if (!token || busy) return;
    setBusy(true);
    setLoginError("");
    setToken(token);
    try {
      const data = await fetchAuthorizeInfo(
        new URLSearchParams(window.location.search),
        token,
      );
      if (!data.owner) {
        // 无效凭证：清掉刚保存的 token，留在登录卡。
        setToken(null);
        setLoginError("令牌无效或已失效，请检查后重试");
        return;
      }
      setInfo(data);
      setPhase("confirm");
    } catch (err) {
      setToken(null);
      setLoginError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  /** 同意/拒绝：提交后端 → 拼回调 URL 跳回 agent 本地回调。 */
  async function handleAction(action: "approve" | "deny") {
    if (busy) return;
    setBusy(true);
    try {
      const result: ApproveResult = await postApprove(
        { action, ...authParams },
        getToken() ?? "",
      );
      if ("code" in result) {
        window.location.href = buildCallbackUrl(result.redirect_uri, {
          code: result.code,
          state: result.state,
        });
      } else {
        window.location.href = buildCallbackUrl(result.redirect_uri, {
          error: result.error,
          error_description: result.error_description,
          state: result.state,
        });
      }
    } catch (e) {
      if (e instanceof OAuthUnauthorizedError) {
        setToken(null);
        setPhase("login");
        setLoginError("登录已失效，请重新输入令牌");
      } else if (e instanceof OAuthBadRequestError) {
        setFatalMessage(e.message);
        setPhase("error");
      } else {
        setFatalMessage(e instanceof Error ? e.message : String(e));
        setPhase("error");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-6 text-foreground">
      <div className="w-full max-w-md space-y-5 rounded-lg border border-border bg-card p-6 shadow-sm">
        {phase === "loading" && (
          <div className="flex items-center justify-center gap-2 py-8 text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            正在校验授权请求…
          </div>
        )}

        {phase === "error" && (
          <div className="space-y-3">
            <div className="flex items-center gap-2 text-destructive">
              <ShieldAlert className="h-5 w-5" />
              <h1 className="text-base font-semibold">无法完成授权</h1>
            </div>
            <p className="text-sm text-muted-foreground">{fatalMessage}</p>
            <p className="text-xs text-muted-foreground">
              请回到 AI 客户端重新发起连接（授权请求可能已过期或参数无效）。
            </p>
          </div>
        )}

        {phase === "login" && (
          <form onSubmit={handleLogin} className="space-y-4">
            <div className="space-y-1">
              <h1 className="flex items-center gap-2 text-base font-semibold">
                <Bot className="h-5 w-5 text-muted-foreground" />
                {info?.client_name || "AI 客户端"} 请求访问 GameTrace
              </h1>
              <p className="text-sm text-muted-foreground">
                登录并同意后，该客户端将以你的身份调用平台的 MCP 工具。
              </p>
            </div>
            <div>
              <label className="flex items-center gap-1.5 text-sm font-medium" htmlFor="oauth-token">
                <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
                访问令牌
              </label>
              <Input
                id="oauth-token"
                type="password"
                value={tokenInput}
                onChange={(e) => setTokenInput(e.target.value)}
                placeholder="形如 gt_… 的访问令牌"
                autoComplete="off"
                className="mt-1.5 font-mono"
                autoFocus
              />
              {loginError && <p className="mt-1.5 text-xs text-destructive">{loginError}</p>}
              <p className="mt-1.5 text-xs text-muted-foreground">
                没有令牌？可
                <a
                  href="/"
                  target="_blank"
                  rel="noreferrer"
                  className="mx-1 inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline"
                >
                  打开平台首页
                  <ExternalLink className="h-3 w-3" />
                </a>
                在「设置」中注册身份，然后回到本页登录授权。
              </p>
            </div>
            <Button type="submit" className="w-full" disabled={busy || !tokenInput.trim()}>
              {busy ? "验证中…" : "登录"}
            </Button>
          </form>
        )}

        {phase === "confirm" && info && (
          <div className="space-y-4">
            <div className="space-y-1">
              <h1 className="flex items-center gap-2 text-base font-semibold">
                <Bot className="h-5 w-5 text-muted-foreground" />
                授权请求
              </h1>
              <p className="text-sm text-muted-foreground">
                AI 客户端 <span className="font-medium text-foreground">{info.client_name || "未知客户端"}</span>{" "}
                请求以 <span className="font-medium text-foreground">{info.owner}</span>{" "}
                的身份访问 GameTrace。
              </p>
            </div>
            <div className="rounded-md border border-border bg-background p-3 text-xs text-muted-foreground">
              同意后该客户端将获得与 <span className="font-medium">{info.owner}</span>{" "}
              完全相同的权限（查看会话、创建项目、管理插件等），令牌仅保存在该客户端本地。
            </div>
            {info.is_admin && (
              <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive">
                <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" />
                注意：该身份是管理员，授权后此 AI 客户端将拥有全局管理权限（用户管理、撤销等）。
              </div>
            )}
            <div className="flex gap-2">
              <Button
                variant="outline"
                className="flex-1"
                onClick={() => void handleAction("deny")}
                disabled={busy}
              >
                拒绝
              </Button>
              <Button className="flex-1" onClick={() => void handleAction("approve")} disabled={busy}>
                {busy ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <ShieldCheck className="h-4 w-4" />
                )}
                {busy ? "提交中…" : "同意授权"}
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
