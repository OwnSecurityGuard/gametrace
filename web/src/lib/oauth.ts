/**
 * MCP OAuth 授权页客户端：与后端 /oauth/authorize-info、/oauth/approve 通信
 * （oauth.go）。授权页由平台自身（gt-mcp :8781）以 SPA 深链接形式服务，
 * 因此一律走同源相对路径；开发期由 Vite proxy /oauth 转发。
 */

/** /oauth/authorize-info 响应：授权页渲染所需的 client 信息与当前登录态。 */
export interface AuthorizeInfo {
  client_id: string;
  client_name: string;
  /** 空串 = 未登录或凭证无效（前端显示登录卡）。 */
  owner: string;
  is_admin: boolean;
}

/** /oauth/approve 请求载荷：authorize URL 参数 + 同意/拒绝动作。 */
export interface ApprovePayload {
  action: "approve" | "deny";
  client_id: string;
  redirect_uri: string;
  code_challenge: string;
  code_challenge_method: string;
  state: string;
}

/** approve 成功：拿到一次性授权码，前端拼 redirect_uri?code&state 跳回 agent。 */
export interface ApproveSuccess {
  code: string;
  state?: string;
  redirect_uri: string;
}

/** approve 拒绝：前端拼 redirect_uri?error=access_denied&state 跳回 agent。 */
export interface ApproveDenied {
  error: string;
  error_description?: string;
  state?: string;
  redirect_uri: string;
}

export type ApproveResult = ApproveSuccess | ApproveDenied;

/** 400 终态：授权请求参数无效（未知 client / redirect 不匹配等），不可重试。 */
export class OAuthBadRequestError extends Error {
  readonly errorCode: string;
  constructor(errorCode: string, message: string) {
    super(message);
    this.name = "OAuthBadRequestError";
    this.errorCode = errorCode;
  }
}

/** 401：未登录或凭证已失效，前端回到登录卡。 */
export class OAuthUnauthorizedError extends Error {
  constructor() {
    super("未登录或凭证已失效");
    this.name = "OAuthUnauthorizedError";
  }
}

/** 后端 OAuth 错误体（RFC 6749 §5.2 形态）。 */
interface OAuthErrorBody {
  error?: string;
  error_description?: string;
}

async function readErrorBody(res: Response): Promise<OAuthErrorBody> {
  try {
    return (await res.json()) as OAuthErrorBody;
  } catch {
    return {};
  }
}

/**
 * 拉取授权页数据：client 名称 + 当前登录态。
 * token 传 null 表示匿名探测（后端不报 401，owner 返回空串）。
 */
export async function fetchAuthorizeInfo(
  params: URLSearchParams,
  token: string | null,
): Promise<AuthorizeInfo> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`/oauth/authorize-info?${params.toString()}`, { headers });
  if (res.status === 400) {
    const body = await readErrorBody(res);
    throw new OAuthBadRequestError(
      body.error ?? "invalid_request",
      body.error_description ?? "授权请求参数无效",
    );
  }
  if (!res.ok) throw new Error(`服务器错误（HTTP ${res.status}）`);
  return (await res.json()) as AuthorizeInfo;
}

/** 提交同意/拒绝；成功返回跳转载荷（approve 码或 deny 错误）。 */
export async function postApprove(
  payload: ApprovePayload,
  token: string,
): Promise<ApproveResult> {
  const res = await fetch("/oauth/approve", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
    body: JSON.stringify(payload),
  });
  if (res.status === 401) throw new OAuthUnauthorizedError();
  if (res.status === 400) {
    const body = await readErrorBody(res);
    throw new OAuthBadRequestError(
      body.error ?? "invalid_request",
      body.error_description ?? "授权请求无效",
    );
  }
  if (!res.ok) throw new Error(`服务器错误（HTTP ${res.status}）`);
  return (await res.json()) as ApproveResult;
}

/** 向 redirect_uri 追加回调参数（code / error + state），返回完整跳转地址。 */
export function buildCallbackUrl(
  base: string,
  params: Record<string, string | undefined>,
): string {
  const u = new URL(base);
  for (const [key, value] of Object.entries(params)) {
    if (value) u.searchParams.set(key, value);
  }
  return u.toString();
}
