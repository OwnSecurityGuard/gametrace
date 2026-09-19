/** 自助注册用户条目（list_users 返回；不含 token —— 凭证只在创建时展示一次）。 */
export interface GtaUser {
  owner: string;
  is_admin?: boolean;
  tenant_id?: string;
  created_by?: string;
  created_at: string;
}

/** list_users 返回：成员账号列表 + env bootstrap 身份（仅 global admin）。 */
export interface ListUsersResult {
  ok?: boolean;
  error?: string;
  users: GtaUser[];
  /** env bootstrap（GT_AUTH_TOKENS）身份名：不在 users 表、不可撤销，仅展示。 */
  bootstrap_owners?: string[];
}

/** revoke_user 返回。 */
export interface RevokeUserResult {
  ok?: boolean;
  error?: string;
  owner: string;
  revoked?: boolean;
}