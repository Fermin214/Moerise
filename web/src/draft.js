export class Draft {
  content = '';
  pending = null;

  begin() {
    if (!this.pending) {
      if (!this.content.trim()) throw Object.assign(new Error('请先写下笔记正文。'), { code: 'CONTENT_EMPTY' });
      if (new TextEncoder().encode(this.content).length > 65536) {
        throw Object.assign(new Error('正文不能超过 64 KiB。'), { code: 'CONTENT_TOO_LARGE' });
      }
      this.pending = Object.freeze({ key: crypto.randomUUID(), content: this.content });
    }
    return this.pending;
  }

  confirm(attempt) {
    if (this.pending !== attempt) return;
    if (this.content === attempt.content) this.content = '';
    this.pending = null;
  }

  abandon() {
    this.pending = null;
  }
}
