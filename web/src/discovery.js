import { l, setAttr } from './i18n.js';
import { el, button } from './dom.js';
import { noteID, listQuery } from './routes.js';
import { validNote } from './notes.js';
import { errorMessage as errorText } from './errors.js';
import { animateMotion, reducedMotion } from './motion.js';
import { walkSummaryBody } from './markdown.js';

function discoveryText(value) {
  const code = typeof value === 'string' ? value : value?.code;
  return {
    EMBEDDING_DISABLED: l("请在设置中启用 Embedding。"),
    EMBEDDING_UNAVAILABLE: l("Embedding 暂时无法连接，请重试或在设置中检查连接。"),
    EMBEDDING_RATE_LIMITED: l("Embedding 调用受限，请稍后重试。"),
    EMBEDDING_PROTOCOL: l("Embedding 返回格式或维度与配置不符，请在设置中检查连接。"),
    INDEX_NOT_READY: l("语义索引尚未就绪，请在设置中查看构建进度后重试。"),
    NO_SOURCE_VECTORS: l("这条笔记没有可用的正文向量，请从其他笔记开始。"),
    SEMANTIC_UNAVAILABLE: l("语义索引暂时不可用，请稍后重试。"),
    WALK_RESOURCE_LIMIT: l("漫步暂时达到技术资源限制，请重试。"),
    WALK_ARCHIVED: l("归档笔记不参与漫步，请选择其他笔记。"),
    RESOURCE_LIMIT: l("漫步暂时达到技术资源限制，请重试。"),
    WALK_RESPONSE_INVALID: l("服务返回的漫步内容不完整，请重试。"),
    REVIEW_RESPONSE_INVALID: l("服务返回的回顾内容不完整，请重试。"),
    REQUEST_TIMEOUT: l("读取超时，请重试。"),
  }[code] || (typeof value === 'string' ? l("服务提示：{0}", { 0: value }) : errorText(value));
}

function walkSummary(note, body = walkSummaryBody(note.content, note.tags || [])) {
  const chars = [...(body || note.attachments?.[0]?.filename || '')];
  if (!chars.length) return l("笔记");
  return chars.length > 64 ? `${chars.slice(0, 64).join('')}…` : chars.join('');
}

export function createDiscovery({ request, sessionRequest, getIdentity, localToday, formatDate, discoveryCard, startFromNote, expire }) {
  let reviewView = null, walkView = null, walkConfiguration = null, walkFloaters = null;
  function createPage(kind, { key: pageKey, seedName }) {
    let alive = true, sequence = 0, controller = null, busy = false, failure = null, retry = null;
    let modelHint = '', configured = walkConfiguration?.enabled === true, modelController = null;
    let floaters = walkFloaters || [], floaterController, floaterFailure, preparedNext = null;
    const narrow = matchMedia('(max-width:960px)');
    let cancelVisual = () => {};
    function resizePreviews() { if (content.querySelector('.walk-start')) render(); }
    let positions, positionedBatch, moveReview;
    let seedPending = kind === 'walk' && !!seedName && walkView?.key !== pageKey;
    const root = el('div', `stage discovery-page ${kind}-page`);
    root.id = 'discovery-page';
    const heading = el('header', 'discovery-heading');
    heading.append(el('h1', 'vt', kind === 'review' ? l("回顾") : l("漫步")));
    const content = el('div', 'discovery-content'), controls = el('div', 'discovery-controls');
    root.append(heading, content, controls);
    const active = ticket => alive && ticket === sequence;
    function discardNext() {
      preparedNext?.controller.abort(); preparedNext = null;
    }
    function cancel() {
      cancelVisual();
      sequence++; controller?.abort(); controller = null; busy = false; discardNext();
    }
    function message(text, error = false) {
      const node = el('p', error ? 'error' : 'sub', text);
      node.setAttribute('role', error ? 'alert' : 'status'); content.append(node);
    }
    function updateBusy() {
      root.setAttribute('aria-busy', String(kind === 'review' ? busy && !reviewView :
        busy || (!!modelController && !walkConfiguration)));
    }
    function render(keepWalkPosition = false) {
      if (!alive) return;
      const scroll = keepWalkPosition ? window.scrollY : null;
      cancelVisual();
      content.replaceChildren();
      for (const child of [...controls.children]) {
        if (child !== positions || positionedBatch !== reviewView?.batch) child.remove();
      }
      updateBusy();
      if (kind === 'review') renderReview();
      else renderWalk();
      controls.hidden = !controls.childElementCount;
      if (failure) {
        message(discoveryText(failure), true);
        content.append(button(l("重试"), 'ghost discovery-retry', () => retry?.()));
      }
      if (scroll !== null) window.scrollTo({ top: scroll, behavior: 'instant' });
    }
    function renderReview() {
      const batch = reviewView?.batch;
      if (!batch) {
        if (busy) message(l("正在加载回顾…"));
        return;
      }
      if (!batch.items.length) { message(l("今天没有可回顾的旧笔记。")); return; }
      const selected = reviewView.position;
      const stack = el('div', 'review-stack');
      for (let i = 0; i < Math.min(2, batch.items.length - selected - 1); i++) {
        const peek = el('div', `peek peek-${i + 1}`); peek.setAttribute('aria-hidden', 'true'); stack.append(peek);
      }
      batch.items.forEach((item, index) => {
        let card;
        if (item.unavailable) {
          card = el('article', 'card big rv discovery-note');
          card.append(el('p', 'sub', l("这条笔记已不存在或无法访问。")));
        } else card = discoveryCard(item.note, true);
        card.hidden = index !== selected; card.dataset.reviewIndex = String(index); stack.append(card);
      });
      content.append(stack);
      if (!positions?.isConnected) {
        positionedBatch = batch;
        positions = el('div', 'pg review-positions');
        positions.setAttribute('role', 'group'); setAttr(positions, 'aria-label', l("回顾条目"));
        batch.items.forEach((_, index) => {
          const dot = button('', 'dt', () => moveReview(index, index > reviewView.position ? -1 : 1, index));
          setAttr(dot, 'aria-label', l("第 {0} / {1} 条", { 0: index + 1, 1: batch.items.length }));
          positions.append(dot);
        });
        controls.append(positions);
      }
      for (const [index, dot] of [...positions.children].entries()) {
        dot.classList.toggle('on', index === selected);
        dot.setAttribute('aria-pressed', String(index === selected));
      }
      const actions = el('div', 'discovery-actions'), current = batch.items[selected];
      if (!current.unavailable) {
        if (current.note.state === 'NORMAL') actions.append(button(l("从这条漫步"), 'secondary', () => startFromNote(current.note)));
      }
      actions.append(button(selected + 1 < batch.items.length ? l("下一条") : l("再看一遍"), 'save', () => {
        move((selected + 1) % batch.items.length, -1, 'next');
      }));
      controls.append(actions);
      const card = stack.querySelector(`[data-review-index="${selected}"]`);
      let drag, animation;
      moveReview = move;
      function move(index, direction, focus, from = 0) {
        if (selected === index || !alive || !stack.isConnected) return;
        const scroll = card.scrollTop;
        reviewView.position = index; render();
        const nextStack = content.querySelector('.review-stack');
        const next = nextStack.querySelector(`[data-review-index="${index}"]`);
        const visual = el('div', card.className);
        visual.classList.remove('discovery-note');
        visual.classList.add('review-outgoing');
        visual.inert = true; visual.setAttribute('aria-hidden', 'true');
        const outgoingContent = el('div', 'review-outgoing-content');
        outgoingContent.append(...card.childNodes);
        visual.append(outgoingContent);
        nextStack.append(visual);
        visual.scrollTop = scroll;
        const reduce = reducedMotion(), destination = direction * nextStack.clientWidth * .92;
        const angle = direction * 2.5;
        const leaving = animateMotion(visual, reduce ? [{ opacity: 1 }, { opacity: 0 }] : [
          { transform: `translateX(${from}px) rotate(0deg)` },
          { transform: `translateX(${destination}px) rotate(${angle}deg)` },
        ], 'responsive', { fill: 'both', easing: 'linear' });
        const fade = reduce ? null : animateMotion(outgoingContent, [
          { opacity: 1 }, { opacity: 1, offset: .7 }, { opacity: 0 },
        ], 'responsive', { fill: 'both', easing: 'linear' });
        let entering, canceled = false;
        if (reduce) next.style.opacity = '0';
        else entering = animateMotion(next, [{ transform: 'translateY(8px) scale(.96)', opacity: .92 }, { transform: 'none', opacity: 1 }]);
        cancelVisual = () => { canceled = true; leaving.cancel(); fade?.cancel(); entering?.cancel(); visual.remove(); next.style.opacity = ''; };
        leaving.finished.then(() => {
          visual.remove();
          if (reduce && !canceled) {
            entering = animateMotion(next, [{ opacity: 0 }, { opacity: 1 }], 'tactile', { fill: 'backwards' });
            next.style.opacity = '';
          }
        }, () => {});
        if (focus === 'next') root.querySelector('.discovery-actions .save')?.focus({ preventScroll: true });
        else if (focus != null) root.querySelectorAll('.review-positions button')[focus]?.focus({ preventScroll: true });
      }
      function startDrag(target, x, y, id) {
        if (batch.items.length < 2 || target.closest('button,a,input,pre')) return;
        cancelVisual(); animation?.cancel();
        drag = { id, x, y, offset: 0 };
      }
      function dragTo(x, y, id) {
        if (!drag || drag.id !== id) return false;
        const offset = x - drag.x, vertical = y - drag.y;
        if (drag.offset === 0 && Math.abs(vertical) > Math.abs(offset) && Math.abs(vertical) > 10) { drag = null; return false; }
        if (Math.abs(offset) < 9 && drag.offset === 0) return false;
        if (drag.offset === 0 && !reducedMotion()) {
          const index = (selected - Math.sign(offset) + batch.items.length) % batch.items.length;
          drag.preview = stack.querySelector(`[data-review-index="${index}"]`).cloneNode(true);
          drag.preview.hidden = false; drag.preview.inert = true;
          drag.preview.classList.remove('discovery-note'); drag.preview.classList.add('review-preview');
          drag.preview.removeAttribute('data-review-index');
          stack.prepend(drag.preview);
        }
        drag.offset = offset;
        if (!reducedMotion()) {
          const progress = Math.min(1, Math.abs(offset) / stack.clientWidth);
          card.style.transform = `translateX(${offset}px)`;
          if (drag.preview) drag.preview.style.transform = `translateY(${8 * (1 - progress)}px) scale(${.96 + .04 * progress})`;
        }
        return true;
      }
      function settle(id, released) {
        if (!drag || drag.id !== id) return;
        const { offset, preview } = drag; drag = null;
        preview?.remove();
        const direction = Math.sign(offset);
        const next = (selected - direction + batch.items.length) % batch.items.length;
        if (released && Math.abs(offset) >= Math.min(96, stack.clientWidth / 4)) move(next, direction, null, offset);
        else if (offset) {
          card.style.transform = '';
          animation = animateMotion(card, [{ transform: `translateX(${offset}px)` }, { transform: 'none' }], 'tactile');
        }
      }
      stack.addEventListener('pointerdown', event => {
        if (event.pointerType === 'touch' || event.button !== 0 || event.target.closest('.note-content')) return;
        startDrag(event.target, event.clientX, event.clientY, event.pointerId);
      });
      stack.addEventListener('pointermove', event => {
        if (event.pointerType !== 'touch' && dragTo(event.clientX, event.clientY, event.pointerId)) stack.setPointerCapture(event.pointerId);
      });
      stack.addEventListener('pointerup', event => { if (event.pointerType !== 'touch') settle(event.pointerId, true); });
      stack.addEventListener('pointercancel', event => { if (event.pointerType !== 'touch') settle(event.pointerId, false); });
      stack.addEventListener('touchstart', event => {
        if (event.touches.length !== 1) { if (drag) settle(drag.id, false); return; }
        const touch = event.touches[0];
        startDrag(event.target, touch.clientX, touch.clientY, touch.identifier);
      }, { passive: true });
      stack.addEventListener('touchmove', event => {
        if (!drag || event.touches.length !== 1) return;
        const touch = event.touches[0];
        if (dragTo(touch.clientX, touch.clientY, touch.identifier) && event.cancelable) event.preventDefault();
      }, { passive: false });
      for (const type of ['touchend', 'touchcancel']) stack.addEventListener(type, event => {
        for (const touch of event.changedTouches) settle(touch.identifier, type === 'touchend');
      }, { passive: true });
    }
    async function loadReview() {
      const started = performance.now();
      cancel();
      const ticket = sequence;
      if (reviewView?.batch.date !== localToday() || reviewView?.batch.timezone !== getIdentity()?.timezone) reviewView = null;
      controller = new AbortController(); busy = true; failure = null; retry = loadReview; render();
      let changed = false;
      try {
        const batch = await request('/reviews/daily', { controller });
        if (!active(ticket)) return;
        if (!batch?.id || !/^\d{4}-\d{2}-\d{2}$/.test(batch.date) || !batch.timezone || !Array.isArray(batch.items) ||
            batch.items.some(item => !item || !noteID(item.name) || typeof item.unavailable !== 'boolean' ||
              (!item.unavailable && (!validNote(item.note) || item.note.name !== item.name)))) {
          throw Object.assign(new Error('Invalid daily review'), { code: 'REVIEW_RESPONSE_INVALID' });
        }
        changed = JSON.stringify(batch) !== JSON.stringify(reviewView?.batch);
        usage('review', 'success', performance.now() - started);
        const position = reviewView?.batch.id === batch.id ? reviewView.position : 0;
        reviewView = { batch, position: Math.min(position, Math.max(0, batch.items.length - 1)) };
      } catch (err) {
        usage('review', resultOf(err), performance.now() - started);
        if (!active(ticket) || err.name === 'AbortError') return;
        if (err.status === 401) { expire(err); return; }
        failure = err;
      } finally {
        if (active(ticket)) {
          controller = null; busy = false;
          if (changed || failure || !reviewView) render();
          else root.setAttribute('aria-busy', 'false');
        }
      }
    }
    function stopWalk() {
      cancel(); seedPending = false; walkView = { key: pageKey, result: null, previous: null };
      failure = retry = null; render();
    }
    function renderWalk() {
      if (modelHint) {
        message(modelHint);
        const settings = el('a', 'dlink', l("打开设置")); settings.href = '/settings'; settings.dataset.route = ''; content.append(settings);
      }
      const result = walkView?.result;
      if (result?.item) {
        const notes = el('div', 'walk-cards');
        if (walkView.previous) {
          const previous = el('div', 'walk-trail');
          const arrow = el('span', 'sub', '↑'); arrow.setAttribute('aria-hidden', 'true');
          previous.append(arrow, el('span', 'walk-trail-text', walkSummary(walkView.previous)));
          notes.append(previous);
        }
        notes.append(discoveryCard(result.item.note)); content.append(notes);
      } else if (!result?.exhausted) {
        const start = el('div', 'walk-start');
        const control = button(busy || (modelController && !walkConfiguration) ? l("翻找中…") : l("随机开始"), 'save', () => walkRequest('/walks/start', {}));
        control.disabled = busy || !configured;
        start.append(control);
        if (floaters.length) {
          const list = el('div', 'walk-start-list');
          for (const { note, body } of floaters.slice(0, narrow.matches ? 4 : 6)) {
            const preview = walkSummary(note, body);
            const floater = button('', 'walk-floater', () => walkRequest('/walks/start', { name: note.name }));
            const date = el('time', '', formatDate(note.create_time));
            date.dateTime = note.create_time;
            floater.append(el('span', '', preview), date);
            floater.dataset.name = note.name;
            setAttr(floater, 'aria-label', l("从这条笔记开始：{0}", { 0: preview }));
            floater.disabled = busy || !configured;
            list.append(floater);
          }
          start.append(el('h2', '', l("从一条笔记开始")), list);
        }
        content.append(start);
        if (floaterFailure) {
          message(errorText(floaterFailure), true);
          content.append(button(l("重试加载笔记"), 'ghost discovery-retry', loadFloaters));
        }
      }
      for (const warning of walkView?.warnings || []) message(discoveryText(warning));
      if (result?.exhausted) message(result.item ? l("能走的路都走完了。") : l("没有可漫步的笔记。"));
      if (result || ((busy || failure) && retry !== loadWalk)) {
        const actions = el('div', 'discovery-actions');
        actions.append(button(l("停止"), 'pbtn', stopWalk));
        if (result?.item && !result.exhausted) {
          const next = button(busy ? l("翻找中…") : l("继续漫步"), 'save', () => walkRequest('/walks/next', {
            name: result.item.note.name, path: [...result.path],
          }));
          next.disabled = busy || !configured; actions.append(next);
        }
        controls.append(actions);
      }
    }
    async function loadFloaters() {
      floaterController?.abort();
      const pending = new AbortController(); floaterController = pending; floaterFailure = null;
      try {
        const page = await request(`/notes?${listQuery('/', { tags: [] })}`, { controller: pending });
        if (!alive || floaterController !== pending) return;
        if (!Array.isArray(page?.notes) || page.notes.some(note => !validNote(note) || note.state !== 'NORMAL')) {
          throw Object.assign(new Error('Invalid notes'), { code: 'NOTE_LIST_INVALID' });
        }
        const tiers = [[], [], []];
        for (const note of page.notes.slice(0, 20)) {
          const body = walkSummaryBody(note.content, note.tags || []);
          if (body || note.attachments?.length) tiers[[...body].length >= 12 ? 0 : body ? 1 : 2].push({ note, body });
        }
        walkFloaters = floaters = tiers.flat().slice(0, 6);
      } catch (err) {
        if (!alive || floaterController !== pending || err.name === 'AbortError') return;
        if (err.status === 401) { expire(err); return; }
        floaterFailure = err;
      } finally {
        if (alive && floaterController === pending) {
          floaterController = null;
          if (!walkView?.result) render();
        }
      }
    }
    async function fetchWalk(path, body, pending) {
      const result = await sessionRequest(path, { method: 'POST', body, controller: pending });
      const validPath = Array.isArray(result?.path) && result.path.every(name => typeof name === 'string' && noteID(name)) &&
        new Set(result.path).size === result.path.length;
      const prior = path === '/walks/next' ? body.path : [];
      if (!validPath || typeof result.mode !== 'string' || typeof result.exhausted !== 'boolean' ||
          !Array.isArray(result.warnings) || result.warnings.some(value => typeof value !== 'string') ||
          prior.some((name, index) => result.path[index] !== name) ||
          (result.item ? !validNote(result.item.note) || result.item.note.state !== 'NORMAL' || result.path.at(-1) !== result.item.note.name ||
            result.path.length !== prior.length + 1 || (body.name && !prior.length && result.item.note.name !== body.name)
            : result.path.length !== prior.length)) {
        throw Object.assign(new Error('Invalid walk response'), { code: 'WALK_RESPONSE_INVALID' });
      }
      return result;
    }
    function prepareNext() {
      const source = walkView?.result;
      if (!alive || busy || !configured || !source?.item || source.exhausted || preparedNext?.source === source) return;
      discardNext();
      const body = { name: source.item.note.name, path: [...source.path] };
      const pending = { source, key: JSON.stringify(body), controller: new AbortController(), outcome: null, loadedAt: 0 };
      preparedNext = pending;
      pending.promise = fetchWalk('/walks/next', body, pending.controller).then(result => {
        pending.loadedAt = Date.now();
        return pending.outcome = { result };
      }, error => {
        if (alive && preparedNext === pending && error.status === 401) expire(error);
        return pending.outcome = { error };
      });
    }
    async function walkRequest(path, body) {
      if (busy || !configured) return;
      cancelVisual();
      let entering = false;
      const started = performance.now();
      let pending = path === '/walks/next' && preparedNext?.source === walkView?.result &&
        preparedNext.key === JSON.stringify(body) ? preparedNext : null;
      if (pending?.outcome?.error || (pending?.loadedAt && Date.now() - pending.loadedAt >= 60_000)) pending = null;
      if (pending) preparedNext = null;
      else discardNext();
      const ticket = ++sequence;
      controller = pending?.controller || new AbortController(); busy = true; failure = null;
      retry = () => walkRequest(path, body); walkView.warnings = [];
      if (!pending?.outcome?.result) render(true);
      try {
        const outcome = pending ? pending.outcome || await pending.promise : { result: await fetchWalk(path, body, controller) };
        if (!active(ticket)) return;
        if (outcome.error) throw outcome.error;
        const result = outcome.result;
        usage('walk', result.item || result.exhausted ? 'success' : 'over_limit', pending ? undefined : performance.now() - started);
        walkView.warnings = result.warnings;
        if (!result.item && !result.exhausted) {
          failure = { code: 'WALK_RESOURCE_LIMIT' };
        } else {
          if (result.item) {
            const current = content.querySelector('.walk-cards > .discovery-note');
            if (current) {
              const leaving = animateMotion(current, [{ opacity: 1 }, { transform: 'translateY(-24px)', opacity: 0 }],
                'tactile', { duration: 100, fill: 'forwards' });
              cancelVisual = () => leaving.cancel();
              if (!await leaving.finished.then(() => active(ticket), () => false)) return;
            }
            walkView.previous = walkView.result?.item?.note || null; walkView.result = result;
            entering = true;
          } else walkView.result = { ...result, item: walkView.result?.item };
        }
      } catch (err) {
        usage('walk', resultOf(err), performance.now() - started);
        if (!active(ticket) || err.name === 'AbortError') return;
        if (err.status === 401) { expire(err); return; }
        failure = err;
      } finally {
        if (active(ticket)) {
          controller = null; busy = false; render(!entering);
          if (entering) {
            const reading = content.querySelector('.walk-cards');
            const toolbar = document.querySelector('.mobile-header').getBoundingClientRect();
            window.scrollTo({ top: window.scrollY + reading.getBoundingClientRect().top - toolbar.bottom, behavior: 'instant' });
            const next = reading.querySelector('.discovery-note');
            const animation = animateMotion(next, [{ transform: 'translateY(24px)', opacity: 0 }, { opacity: 1 }],
              'tactile', { duration: 140, fill: 'backwards' });
            cancelVisual = () => animation.cancel();
          }
          if (!failure) prepareNext();
        }
      }
    }
    function startSeed() {
      if (!seedPending || !configured || busy) return;
      seedPending = false;
      if (!noteID(seedName)) { failure = { code: 'SOURCE_NOT_FOUND' }; render(); return; }
      return walkRequest('/walks/start', { name: seedName });
    }
    async function loadWalk() {
      const ticket = sequence, pending = new AbortController();
      let redraw = !walkConfiguration;
      modelController?.abort(); modelController = pending;
      if (!walkConfiguration) { failure = null; retry = loadWalk; }
      render();
      try {
        const models = await sessionRequest('/models', { controller: pending });
        if (!alive || modelController !== pending) return;
        if (typeof models?.embedding?.enabled !== 'boolean') {
          throw Object.assign(new Error('Invalid models response'), { code: 'WALK_RESPONSE_INVALID' });
        }
        redraw ||= configured !== models.embedding.enabled;
        walkConfiguration = { enabled: models.embedding.enabled };
        configured = models.embedding?.enabled === true;
        modelHint = configured ? '' : l("请在设置中启用 Embedding，再开始漫步。");
        if (!configured) discardNext();
        else { startSeed(); prepareNext(); }
      } catch (err) {
        if (!alive || modelController !== pending || err.name === 'AbortError') return;
        if (err.status === 401) { expire(err); return; }
        if (active(ticket)) { failure = err; retry = loadWalk; redraw = true; }
      } finally {
        if (alive && modelController === pending) {
          modelController = null;
          if (redraw || !walkView?.result) render();
          else updateBusy();
        }
      }
    }
    return {
      async mount(parent) {
        parent.append(root);
        if (kind === 'review') await loadReview();
        else {
          narrow.addEventListener('change', resizePreviews);
          walkView ||= { key: null, result: null, previous: null };
          if (seedPending) walkView = { key: pageKey, result: null, previous: null };
          if (walkConfiguration) {
            modelHint = configured ? '' : l("请在设置中启用 Embedding，再开始漫步。");
            startSeed(); prepareNext();
          }
          await Promise.all([loadWalk(), loadFloaters()]);
        }
      },
      refresh() {
        cancel(); floaterController?.abort(); floaters = [];
        if (kind === 'review') loadReview();
        else {
          walkView = { key: pageKey, result: null, previous: null };
          failure = retry = null; render();
          loadWalk(); loadFloaters();
        }
      },
      destroy() { alive = false; cancel(); narrow.removeEventListener('change', resizePreviews); modelController?.abort(); floaterController?.abort(); root.remove(); },
    };
  }
  return {
    createPage,
    clear() { reviewView = walkView = walkConfiguration = walkFloaters = null; },
    invalidate({ settings = false } = {}) {
      if (settings) walkView = walkFloaters = walkConfiguration = null;
      else reviewView = walkView = walkFloaters = null;
    },
  };
}
import { usage, resultOf } from './privacy.js';
