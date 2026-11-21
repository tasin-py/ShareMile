'use strict';
// ShareMile chat drawer, shared by the app and the browser share page.
// SMChat.open({key, title, sub, get(since), post(body), name(), setName(n), hostLabel})
(function () {
  const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
  const seenKey = k => 'smc-seen:' + k;
  const SMChat = {
    seen(key) { return +(localStorage.getItem(seenKey(key)) || 0); },
    unread(key, seq) { return Math.max(0, (seq || 0) - SMChat.seen(key)); },
    markSeen(key, seq) { if (seq > SMChat.seen(key)) localStorage.setItem(seenKey(key), seq); },
    current: null,
  };
  window.SMChat = SMChat;

  let el = null, o = null, V = null, rev = -1, timer = null, replyTo = 0, picked = {}, ac = null, busy = false;

  function build() {
    el = document.createElement('aside');
    el.className = 'smc';
    el.hidden = true;
    el.innerHTML = `<header class="smc-h">
        <div class="smc-t"><b></b><span class="smc-sub"></span></div>
        <button class="smc-x" data-c="close" title="Close chat" aria-label="Close chat">✕</button>
      </header>
      <div class="smc-people"></div>
      <div class="smc-list" tabindex="-1"></div>
      <button class="smc-new" hidden>New messages ↓</button>
      <div class="smc-reply" hidden><div><span>Replying to <b></b></span><q></q></div><button data-c="unreply" aria-label="Cancel reply">✕</button></div>
      <div class="smc-ac" hidden></div>
      <form class="smc-in">
        <textarea rows="1" maxlength="2000" placeholder="Message… (type @ to mention)"></textarea>
        <button class="smc-send" title="Send" aria-label="Send"><svg viewBox="0 0 24 24"><path d="M4 12l16-8-6 16-2-7z"/></svg></button>
      </form>
      <form class="smc-name" hidden>
        <p><b>Your name in chat</b><br><span>Others see this next to your messages.</span></p>
        <input maxlength="32" placeholder="e.g. Rafi" autocomplete="off">
        <button class="smc-btn">Start chatting</button>
      </form>
      <div class="smc-pick" hidden></div>`;
    document.body.append(el);
    const q = s => el.querySelector(s);
    q('[data-c=close]').onclick = () => SMChat.close();
    q('[data-c=unreply]').onclick = () => setReply(0);
    const ta = q('.smc-in textarea');
    ta.addEventListener('input', () => { grow(ta); autocomplete(); });
    ta.addEventListener('keydown', e => {
      if (ac && !q('.smc-ac').hidden) {
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); ac.i = (ac.i + (e.key === 'ArrowDown' ? 1 : -1) + ac.list.length) % ac.list.length; drawAC(); return; }
        if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); chooseAC(ac.list[ac.i]); return; }
        if (e.key === 'Escape') { e.preventDefault(); hideAC(); return; }
      }
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
      if (e.key === 'Escape' && replyTo) setReply(0);
    });
    ta.addEventListener('blur', () => setTimeout(hideAC, 150));
    q('.smc-in').onsubmit = e => { e.preventDefault(); send(); };
    q('.smc-name').onsubmit = async e => {
      e.preventDefault();
      const v = q('.smc-name input').value.trim();
      if (!v) return;
      try { await o.setName(v); showName(false); ta.focus(); rev = -1; poll(); } catch (er) { flash(er.message); }
    };
    q('.smc-new').onclick = () => { toBottom(); };
    const list = q('.smc-list');
    list.addEventListener('scroll', () => { if (nearBottom()) q('.smc-new').hidden = true; });
    list.addEventListener('click', e => {
      const b = e.target.closest('[data-a]');
      if (!b) return;
      const id = +b.closest('[data-id]')?.dataset.id;
      const a = b.dataset.a;
      if (a === 'reply') { setReply(id); ta.focus(); }
      else if (a === 'react') openPicker(b, id);
      else if (a === 'emo') react(id, b.dataset.e);
      else if (a === 'jump') jump(+b.dataset.to);
    });
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && !el.hidden && !q('.smc-pick').hidden) q('.smc-pick').hidden = true; });
    document.addEventListener('click', e => { if (!e.target.closest('.smc-pick') && !e.target.closest('[data-a=react]')) q('.smc-pick').hidden = true; });
  }
  const q = s => el.querySelector(s);
  const grow = ta => { ta.style.height = 'auto'; ta.style.height = Math.min(140, ta.scrollHeight) + 'px'; };
  const nearBottom = () => { const l = q('.smc-list'); return l.scrollHeight - l.scrollTop - l.clientHeight < 60; };
  const toBottom = () => { const l = q('.smc-list'); l.scrollTop = l.scrollHeight; q('.smc-new').hidden = true; };
  function flash(msg) {
    let f = q('.smc-err');
    if (!f) { f = document.createElement('div'); f.className = 'smc-err'; q('.smc-in').before(f); }
    f.textContent = msg; f.hidden = false;
    clearTimeout(f._t); f._t = setTimeout(() => { f.hidden = true; }, 5000);
  }
  function showName(on) {
    q('.smc-name').hidden = !on; q('.smc-in').hidden = on;
    if (on) { q('.smc-name input').value = o.name() || ''; setTimeout(() => q('.smc-name input').focus(), 50); }
  }

  SMChat.open = function (opts) {
    if (!el) build();
    if (o && o.key === opts.key && !el.hidden) { q('.smc-in textarea').focus(); return; }
    o = opts; SMChat.current = opts.key; V = null; rev = -1; replyTo = 0; picked = {};
    q('.smc-t b').textContent = opts.title;
    q('.smc-sub').textContent = opts.sub || '';
    q('.smc-list').innerHTML = '<div class="smc-empty">Loading chat…</div>';
    q('.smc-people').innerHTML = '';
    q('.smc-reply').hidden = true;
    q('.smc-in textarea').value = '';
    el.hidden = false;
    document.body.classList.add('smc-open');
    showName(!opts.name());
    if (opts.name()) setTimeout(() => q('.smc-in textarea').focus(), 50);
    poll();
  };
  SMChat.close = function () {
    if (!el) return;
    el.hidden = true; document.body.classList.remove('smc-open');
    clearTimeout(timer); o = null; SMChat.current = null;
  };

  async function poll() {
    clearTimeout(timer);
    if (!o) return;
    const mine = o;
    try {
      const v = await mine.get(rev);
      if (o !== mine) return;
      if (!v.same) apply(v);
    } catch (e) {
      if (o !== mine) return;
      if (!V) q('.smc-list').innerHTML = `<div class="smc-empty">${esc(e.message)}</div>`;
    }
    if (o === mine) timer = setTimeout(poll, document.hidden ? 8000 : 2000);
  }

  function apply(v) {
    const atEnd = !V || nearBottom();
    const before = V ? V.seq : 0;
    V = v; rev = v.rev;
    V.byId = {}; for (const m of v.msgs || []) V.byId[m.id] = m;
    V.names = {}; for (const p of v.people || []) V.names[p.id] = p.name;
    drawPeople(); drawList();
    if (atEnd) toBottom(); else if (v.seq > before) q('.smc-new').hidden = false;
    SMChat.markSeen(o.key, v.seq);
    if (o.onSeen) o.onSeen(v.seq);
  }

  function drawPeople() {
    const ps = (V.people || []).slice().sort((a, b) => (b.online - a.online) || (b.host - a.host) || a.name.localeCompare(b.name));
    const on = ps.filter(p => p.online).length;
    q('.smc-sub').textContent = (o.sub ? o.sub + ' · ' : '') + `${on} online`;
    q('.smc-people').innerHTML = ps.map(p => `<span class="smc-p ${p.online ? 'on' : ''}" title="${p.online ? 'Online' : 'Away'}"><i></i>${esc(p.name)}${p.host ? ` <em>${esc(o.hostLabel || 'Host')}</em>` : ''}${p.id === V.me ? ' (you)' : ''}</span>`).join('');
  }

  const time = t => new Date(t).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
  const day = t => new Date(t).toLocaleDateString([], {weekday: 'short', month: 'short', day: 'numeric'});

  function richText(m) {
    let h = esc(m.text);
    const names = (m.mentions || []).map(id => [id, V.names[id]]).filter(x => x[1]).sort((a, b) => b[1].length - a[1].length);
    for (const [id, n] of names) {
      const at = esc('@' + n);
      h = h.split(at).join(`<span class="smc-men${id === V.me ? ' me' : ''}">${at}</span>`);
    }
    return h.replace(/\n/g, '<br>');
  }

  function drawList() {
    const list = q('.smc-list');
    const msgs = V.msgs || [];
    if (!msgs.length) { list.innerHTML = `<div class="smc-empty">No messages yet. Say hi 👋</div>`; return; }
    let out = '', lastDay = '', prev = null;
    for (const m of msgs) {
      const d = day(m.t);
      if (d !== lastDay) { out += `<div class="smc-day">${esc(d)}</div>`; lastDay = d; prev = null; }
      const mine = m.fromId === V.me;
      const cont = prev && prev.fromId === m.fromId && m.t - prev.t < 5 * 60e3 && !m.reply;
      const mentionsMe = (m.mentions || []).includes(V.me) && !mine;
      const r = m.reply && V.byId[m.reply];
      const reacts = Object.entries(m.reacts || {}).map(([e, who]) => `<button class="smc-r ${who.includes(V.me) ? 'on' : ''}" data-a="emo" data-e="${esc(e)}" title="${esc(who.map(id => V.names[id] || 'Someone').join(', '))}">${e} <b>${who.length}</b></button>`).join('');
      out += `<div class="smc-m ${mine ? 'mine' : ''} ${cont ? 'cont' : ''} ${mentionsMe ? 'ping' : ''}" data-id="${m.id}">
        ${cont ? '' : `<div class="smc-who"><b>${esc(mine ? 'You' : m.from)}</b>${m.host ? `<em>${esc(o.hostLabel || 'Host')}</em>` : ''}<time>${time(m.t)}</time></div>`}
        <div class="smc-b">
          ${m.reply ? `<button class="smc-q" data-a="jump" data-to="${m.reply}">${r ? `<b>${esc(r.fromId === V.me ? 'You' : r.from)}</b> ${esc(r.text.slice(0, 120))}` : '<i>Earlier message</i>'}</button>` : ''}
          <div class="smc-txt">${richText(m)}</div>
          <div class="smc-acts"><button data-a="react" title="React" aria-label="React">☺</button><button data-a="reply" title="Reply" aria-label="Reply">↩</button></div>
        </div>
        ${reacts ? `<div class="smc-rs">${reacts}</div>` : ''}
      </div>`;
      prev = m;
    }
    list.innerHTML = out;
  }

  function setReply(id) {
    replyTo = id;
    const m = id && V && V.byId[id];
    q('.smc-reply').hidden = !m;
    if (m) { q('.smc-reply b').textContent = m.fromId === V.me ? 'yourself' : m.from; q('.smc-reply q').textContent = m.text.slice(0, 140); }
  }
  function jump(id) {
    const n = q(`.smc-m[data-id="${id}"]`);
    if (!n) return;
    n.scrollIntoView({block: 'center', behavior: 'smooth'});
    n.classList.remove('hl'); void n.offsetWidth; n.classList.add('hl');
  }
  function openPicker(btn, id) {
    const p = q('.smc-pick');
    p.innerHTML = (V.emojis || []).map(e => `<button data-e="${esc(e)}">${e}</button>`).join('');
    p.hidden = false;
    const r = btn.getBoundingClientRect(), er = el.getBoundingClientRect();
    p.style.top = Math.max(8, r.top - er.top - p.offsetHeight - 6) + 'px';
    p.style.left = Math.min(er.width - p.offsetWidth - 8, Math.max(8, r.left - er.left - p.offsetWidth / 2)) + 'px';
    p.querySelectorAll('button').forEach(b => b.onclick = () => { p.hidden = true; react(id, b.dataset.e); });
  }
  async function react(id, emoji) {
    try { apply(await o.post({op: 'react', msg: id, emoji})); } catch (e) { flash(e.message); }
  }
  async function send() {
    const ta = q('.smc-in textarea');
    const text = ta.value.trim();
    if (!text || busy) return;
    if (!o.name()) { showName(true); return; }
    const mentions = Object.entries(picked).filter(([n]) => text.includes('@' + n)).map(([, id]) => id);
    busy = true;
    try {
      const v = await o.post({op: 'send', text, reply: replyTo, mentions});
      ta.value = ''; grow(ta); picked = {}; setReply(0);
      apply(v); toBottom();
    } catch (e) { flash(e.message); }
    finally { busy = false; }
  }

  // @mention autocomplete
  function autocomplete() {
    const ta = q('.smc-in textarea');
    const upto = ta.value.slice(0, ta.selectionStart);
    const m = upto.match(/(^|\s)@([^\s@]{0,32})$/);
    if (!m || !V) return hideAC();
    const s = m[2].toLowerCase();
    const list = (V.people || []).filter(p => p.id !== V.me && p.name.toLowerCase().includes(s)).slice(0, 8);
    if (!list.length) return hideAC();
    ac = {list, i: 0, start: upto.length - m[2].length - 1};
    drawAC();
  }
  function drawAC() {
    const box = q('.smc-ac');
    box.innerHTML = ac.list.map((p, i) => `<button type="button" class="${i === ac.i ? 'on' : ''}" data-i="${i}"><i class="${p.online ? 'on' : ''}"></i>${esc(p.name)}${p.host ? ` <em>${esc(o.hostLabel || 'Host')}</em>` : ''}</button>`).join('');
    box.hidden = false;
    box.querySelectorAll('button').forEach(b => b.onmousedown = e => { e.preventDefault(); chooseAC(ac.list[+b.dataset.i]); });
  }
  function hideAC() { if (el) q('.smc-ac').hidden = true; ac = null; }
  function chooseAC(p) {
    const ta = q('.smc-in textarea');
    const end = ta.selectionStart;
    const ins = '@' + p.name + ' ';
    ta.value = ta.value.slice(0, ac.start) + ins + ta.value.slice(end);
    const pos = ac.start + ins.length;
    ta.setSelectionRange(pos, pos);
    picked[p.name] = p.id;
    hideAC(); grow(ta); ta.focus();
  }
})();
