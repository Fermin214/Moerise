export const pages = new Map([
  ['/', '主页'], ['/archived', '归档笔记'], ['/review', '每日回顾'],
  ['/walk', '随机漫步'], ['/insights', 'AI 洞察'], ['/map', '认知地图'], ['/settings', '设置'],
]);
export const notePath = /^\/notes\/([A-Za-z0-9_-]+)$/;

export function validDate(value) {
  return /^\d{4}-\d{2}-\d{2}$/.test(value || '') &&
    !Number.isNaN(Date.parse(value)) && new Date(value).toISOString().slice(0, 10) === value;
}

export function filters(search) {
  const query = new URLSearchParams(search);
  return {
    query: query.get('query') || '',
    tags: [...new Set(query.getAll('tag').filter(Boolean))],
    date: validDate(query.get('date')) ? query.get('date') : '',
  };
}

export function timelineURL(path, values) {
  const query = new URLSearchParams();
  if (values.query) query.set('query', values.query);
  for (const tag of values.tags || []) query.append('tag', tag);
  if (validDate(values.date)) query.set('date', values.date);
  return `${path === '/archived' ? path : '/'}${query.size ? `?${query}` : ''}`;
}

export function listQuery(path, values, token = '') {
  const query = new URLSearchParams({
    state: path === '/archived' ? 'ARCHIVED' : 'NORMAL', order: 'created_desc', page_size: '20',
  });
  if (values.query) query.set('query', values.query);
  for (const tag of values.tags) query.append('tag', tag);
  if (values.date) query.set('date', values.date);
  if (token) query.set('page_token', token);
  return query;
}

// Only application pages can be a detail's return destination.
export function safeFrom(value) {
  if (!value || !value.startsWith('/') || value.startsWith('//') || /[\\#\u0000-\u0020%]/.test(value.split('?')[0])) return '/';
  if (value.split('?')[0].split('/').some(part => part === '.' || part === '..')) return '/';
  try {
    const url = new URL(value, 'https://moerise.invalid');
    if (url.origin !== 'https://moerise.invalid' || !pages.has(url.pathname) || url.hash) return '/';
    return url.pathname === '/' || url.pathname === '/archived'
      ? timelineURL(url.pathname, filters(url.search)) : url.pathname;
  } catch { return '/'; }
}

export function noteID(name) {
  const value = String(name).replace(/^memos\//, '');
  return /^[A-Za-z0-9_-]+$/.test(value) ? value : null;
}

export function detailURL(name, from) {
  const id = noteID(name);
  if (!id) throw new Error('Invalid note name');
  return `/notes/${id}?${new URLSearchParams({ from: safeFrom(from) })}`;
}

export function attachmentURL(note, attachment) {
  const id = noteID(note.name);
  const match = /^attachments\/([A-Za-z0-9_-]+)$/.exec(attachment.name || '');
  const expected = id && match ? `/api/app/v1/notes/${id}/attachments/${match[1]}/content` : '';
  return expected && attachment.content_url === expected ? expected : '';
}

export function rasterImage(type) {
  return /^(image\/(?:png|jpeg|gif|webp|avif))$/.test(type || '');
}
