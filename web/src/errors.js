const messages = {
  SESSION_UNAUTHORIZED: '账户信息无效或登录已失效，请检查账户后重新登录。',
  ACCOUNT_NOT_BOUND: '此账户不是该实例绑定的作者。',
  PASSWORD_LOGIN_UNSUPPORTED: '此 Memos 实例不支持密码登录，请检查 Memos 的登录设置。',
  LOGIN_RATE_LIMITED: 'Memos 登录尝试过于频繁，请稍后再试。',
  RATE_LIMITED: '登录尝试过于频繁，请稍后再试。',
  UPSTREAM_UNAVAILABLE: 'Memos 服务暂时无法连接，请稍后重试。',
  UPSTREAM_AUTH: 'Memos 拒绝了来源凭据，请检查 Core 的来源配置。',
  SESSION_UNAVAILABLE: '会话服务暂时不可用，请稍后重试。',
  CSRF_REJECTED: '会话校验失败，请退出后重新登录。',
  ORIGIN_REJECTED: '访问地址与服务配置不一致，请检查公开地址。',
  AUTHORIZATION_REJECTED: '客户端 API 仅使用登录会话，请移除 Authorization 请求头。',
  WRITE_OUTCOME_UNKNOWN: '保存结果尚未确认，请使用原提交重试。',
  IDEMPOTENCY_CONFLICT: '提交标识与正文不匹配，请先确认原提交的保存结果。',
  WRITE_CONFLICT: '原提交与来源记录存在冲突，请先确认来源内容。',
  INVALID_PAGE_TOKEN: '分页已失效，请刷新时间线。',
  SOURCE_NOT_FOUND: '这条笔记已不存在或无法访问，请刷新时间线。',
  IDENTITY_MISMATCH: '来源作者与实例绑定不一致，请检查 Core 的来源配置。',
  CORE_UNAVAILABLE: '服务暂时无法连接，请稍后重试。',
  NETWORK_ERROR: '网络连接中断，请稍后重试。',
  REQUEST_TIMEOUT: '请求超时，保存结果可能尚未确认。',
  CONTENT_EMPTY: '请先写下笔记正文。',
  CONTENT_TOO_LARGE: '正文不能超过 64 KiB。',
  SESSION_RESPONSE_INVALID: '服务返回的会话信息不完整，请重试登录。',
  NOTE_LIST_INVALID: '服务返回的笔记列表格式无效，请刷新重试。',
  NOTE_SAVE_UNCONFIRMED: '保存响应不完整，请使用原提交重试。',
};

export function errorText(err) {
  return [
    messages[err.code] || '请求未能完成，请稍后重试。',
    err.code ? `（${err.code}）` : '',
    err.requestId ? `请求编号：${err.requestId}` : '',
  ].filter(Boolean).join(' ');
}
