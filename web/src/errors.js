const messages = {
  SESSION_UNAUTHORIZED: '账户信息无效或登录已失效，请检查账户后重新登录。',
  ACCOUNT_NOT_BOUND: '此账户不是该实例绑定的作者。',
  PASSWORD_LOGIN_UNSUPPORTED: '此 Memos 实例不支持密码登录，请检查 Memos 的登录设置。',
  LOGIN_RATE_LIMITED: 'Memos 登录尝试过于频繁，请稍后再试。',
  RATE_LIMITED: '登录尝试过于频繁，请稍后再试。',
  UPSTREAM_UNAVAILABLE: 'Memos 服务暂时无法连接，请稍后重试。',
  UPSTREAM_AUTH: 'Memos 身份校验失败。',
  SESSION_UNAVAILABLE: '会话服务暂时不可用，请稍后重试。',
  CSRF_REJECTED: '会话校验失败，请退出后重新登录。',
  ORIGIN_REJECTED: '访问地址不匹配。',
  AUTHORIZATION_REJECTED: '请使用 Memos 账户登录。',
  WRITE_OUTCOME_UNKNOWN: '保存结果尚未确认。',
  IDEMPOTENCY_CONFLICT: '保存内容不一致。',
  WRITE_CONFLICT: '笔记内容存在冲突。',
  INVALID_PAGE_TOKEN: '分页已失效，请刷新时间线。',
  SOURCE_NOT_FOUND: '这条笔记已不存在或无法访问，请刷新时间线。',
  IDENTITY_MISMATCH: 'Memos 身份校验失败。',
  CORE_UNAVAILABLE: '服务暂时无法连接，请稍后重试。',
  NETWORK_ERROR: '网络连接中断，请稍后重试。',
  REQUEST_TIMEOUT: '请求超时，保存结果可能尚未确认。',
  CONTENT_EMPTY: '请先写下笔记正文。',
  CONTENT_TOO_LARGE: '正文不能超过 64 KiB。',
  SESSION_RESPONSE_INVALID: '服务返回的会话信息不完整，请重试登录。',
  NOTE_LIST_INVALID: '服务返回的笔记列表格式无效，请刷新重试。',
  NOTE_SAVE_UNCONFIRMED: '保存结果尚未确认。',
};

export function errorText(err) {
  return messages[err.code] || '请求未能完成，请稍后重试。';
}
