export const attachmentLimit = 8 * 1024 * 1024;
export const attachmentCountLimit = 10;
const byteSize = item => Math.floor(item.content.length * 3 / 4) -
  (item.content.endsWith('==') ? 2 : item.content.endsWith('=') ? 1 : 0);

export function validateAttachments(attachments) {
  if (attachments.length > attachmentCountLimit) {
    throw Object.assign(new Error('最多添加 10 个附件。'), { code: 'ATTACHMENT_COUNT' });
  }
  const bytes = attachments.reduce((sum, item) => sum + byteSize(item), 0);
  if (bytes > attachmentLimit) {
    throw Object.assign(new Error('新增附件总大小不能超过 8 MiB。'), { code: 'ATTACHMENT_SIZE' });
  }
}

export async function readAttachments(files, existing = []) {
  if (files.length + existing.length > attachmentCountLimit) {
    throw Object.assign(new Error('最多添加 10 个附件。'), { code: 'ATTACHMENT_COUNT' });
  }
  validateAttachments(existing);
  const existingBytes = existing.reduce((sum, item) => sum + byteSize(item), 0);
  if (Array.from(files).reduce((sum, file) => sum + file.size, existingBytes) > attachmentLimit) {
    throw Object.assign(new Error('新增附件总大小不能超过 8 MiB。'), { code: 'ATTACHMENT_SIZE' });
  }
  const result = [];
  for (const file of files) {
    const bytes = new Uint8Array(await file.arrayBuffer());
    let binary = '';
    for (let i = 0; i < bytes.length; i += 8192) binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
    result.push({ filename: file.name, type: file.type || 'application/octet-stream', content: btoa(binary) });
  }
  validateAttachments([...existing, ...result]);
  return result;
}

export class Draft {
  content = '';
  attachments = [];
  pending = null;

  get locked() {
    return !!this.pending;
  }

  begin(extra = {}, { retained = 0 } = {}) {
    if (!this.pending) {
      if (!this.content.trim() && !this.attachments.length && !retained) {
        throw Object.assign(new Error('请写下正文或添加附件。'), { code: 'CONTENT_EMPTY' });
      }
      if (new TextEncoder().encode(this.content).length > 65536) {
        throw Object.assign(new Error('正文不能超过 64 KiB。'), { code: 'CONTENT_TOO_LARGE' });
      }
      validateAttachments(this.attachments);
      const attachments = Object.freeze(this.attachments.map(item => Object.freeze({ ...item })));
      const body = Object.freeze({
        ...extra, content: this.content, attachments,
        ...(extra.remove_attachments ? { remove_attachments: Object.freeze([...extra.remove_attachments]) } : {}),
      });
      this.pending = Object.freeze({ key: crypto.randomUUID(), content: this.content, attachments, body });
    }
    return this.pending;
  }

  confirm(attempt) {
    if (this.pending !== attempt) return;
    if (this.content === attempt.content) this.content = '';
    if (JSON.stringify(this.attachments) === JSON.stringify(attempt.attachments)) this.attachments = [];
    this.pending = null;
  }

  reject(attempt, code) {
    if (this.pending !== attempt) return;
    if (['INVALID_ARGUMENT', 'UNSUPPORTED_MEDIA_TYPE', 'ATTACHMENT_REFERENCED', 'ATTACHMENT_DELETE_REJECTED'].includes(code)) {
      this.abandon();
    }
  }

  abandon() {
    this.pending = null;
  }

  clear() {
    this.content = '';
    this.attachments = [];
    this.pending = null;
  }
}
