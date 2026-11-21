'use strict';
const $ = (s, el = document) => el.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const base = location.pathname.replace(/\/?$/, '/');
const fmt = n => { if (!n) return '0 B'; const u = ['B','KB','MB','GB','TB']; let i = 0; while (n >= 1024 && i < 4) { n /= 1024; i++; } return (i ? n.toFixed(n < 10 ? 2 : n < 100 ? 1 : 0) : n) + ' ' + u[i]; };
const ICONS = {
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  image: '<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="10" r="2"/><path d="M21 17l-5-5-9 8"/>',
  video: '<rect x="3" y="5" width="14" height="14" rx="2"/><path d="M17 10l4-2v8l-4-2"/>',
  audio: '<path d="M9 18V6l11-2v12"/><circle cx="6" cy="18" r="3"/><circle cx="17" cy="16" r="3"/>',
  text: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>',
  pdf: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
  archive: '<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"/>',
  doc: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h4"/>',
  app: '<rect x="3" y="3" width="18" height="18" rx="4"/><path d="M8 12h8M12 8v8"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
};
const DL = '<svg viewBox="0 0 24 24"><path d="M12 3v12M7 10l5 5 5-5M5 21h14"/></svg>';
const ic = k => `<svg class="ic" viewBox="0 0 24 24">${ICONS[k] || ICONS.file}</svg>`;

let D = null, cwd = '', sel = new Set(), view = localStorage.smView || 'grid', items = [];

async function load() {
  try {
    const r = await fetch(base + 'api', {cache: 'no-store'});
    if (!r.ok) throw 0;
    D = await r.json();
  } catch {
    $('#title').textContent = 'This share is no longer available';
    $('#sub').textContent = 'The sharer stopped sharing or their PC is offline.';
    return;
  }
  document.title = D.name + ' · ShareMile';
  $('#title').textContent = D.name;
  $('#sub').textContent = `${D.files.length.toLocaleString()} file${D.files.length > 1 ? 's' : ''} · ${fmt(D.total)}`;
  $('#dl-all').disabled = false;
  $('#upload').hidden = !D.allowUpload;
  // single top-level folder: open it directly
  const tops = new Set(D.files.map(f => f.rel.split('/')[0]));
  cwd = location.hash ? decodeURIComponent(location.hash.slice(1)) : (tops.size === 1 && D.files[0].rel.includes('/') ? [...tops][0] : '');
  render();
}

function listing() {
  const q = $('#q').value.trim().toLowerCase();
  if (q) return D.files.filter(f => f.rel.toLowerCase().includes(q)).map(f => ({type: 'file', f}));
  const pre = cwd ? cwd + '/' : '';
  const dirs = new Map(), files = [];
  for (const f of D.files) {
    if (!f.rel.startsWith(pre)) continue;
    const rest = f.rel.slice(pre.length);
    const i = rest.indexOf('/');
    if (i < 0) files.push({type: 'file', f});
    else {
      const d = rest.slice(0, i);
      const e = dirs.get(d) || {type: 'dir', name: d, path: pre + d, size: 0, count: 0};
      e.size += f.size; e.count++;
      dirs.set(d, e);
    }
  }
  return [...[...dirs.values()].sort((a, b) => a.name.localeCompare(b.name)), ...files];
}

function render() {
  const grid = $('#grid');
  grid.className = 'grid' + (view === 'list' ? ' list' : '') + (sel.size ? ' selecting' : '');
  document.querySelectorAll('[data-view]').forEach(b => b.classList.toggle('on', b.dataset.view === view));
  // breadcrumbs
  const parts = cwd ? cwd.split('/') : [];
  $('#crumbs').innerHTML = `<a href="#" data-nav="">All files</a>` + parts.map((p, i) => `<span class="muted">/</span><a href="#" data-nav="${esc(parts.slice(0, i + 1).join('/'))}">${esc(p)}</a>`).join('');
  items = listing();
  if (!items.length) { grid.innerHTML = '<div class="empty">Nothing here</div>'; updateSel(); return; }
  grid.innerHTML = items.map((it, i) => {
    if (it.type === 'dir') return `<div class="item folder" data-i="${i}"><div class="thumb">${ic('folder')}</div>
      <button class="dlb" data-zip="${i}" title="Download folder as ZIP">${DL}</button>
      <div class="meta"><div class="nm">${esc(it.name)}</div><div class="sz">${it.count} file${it.count > 1 ? 's' : ''} · ${fmt(it.size)}</div></div></div>`;
    const f = it.f, name = f.rel.split('/').pop(), ext = (name.includes('.') ? name.split('.').pop() : '').slice(0, 5).toUpperCase();
    const thumb = f.kind === 'image' && view === 'grid' ? `<img loading="lazy" src="${base}t/${f.id}" alt="" onerror="this.replaceWith(document.createRange().createContextualFragment(this.dataset.fb))" data-fb="${esc(ic('image'))}">` : ic(f.kind);
    return `<div class="item ${sel.has(f.id) ? 'sel' : ''}" data-i="${i}"><div class="thumb">${thumb}${ext && view === 'grid' ? `<span class="ext">${esc(ext)}</span>` : ''}</div>
      <span class="pick" data-pick="${f.id}">${sel.has(f.id) ? '✓' : ''}</span>
      <a class="dlb" href="${base}f/${f.id}?dl=1" download title="Download">${DL}</a>
      <div class="meta"><div class="nm" title="${esc(f.rel)}">${esc(name)}</div><div class="sz">${fmt(f.size)}</div></div></div>`;
  }).join('');
  updateSel();
}

function updateSel() {
  $('#selbar').hidden = !sel.size;
  document.body.classList.toggle('has-sel', sel.size > 0);
  $('#sel-n').textContent = sel.size;
  let s = 0; for (const f of D.files) if (sel.has(f.id)) s += f.size;
  $('#sel-size').textContent = fmt(s);
}

function zip(params) {
  const form = document.createElement('form');
  form.method = 'POST'; form.action = base + 'zip';
  for (const [k, v] of Object.entries(params)) { const i = document.createElement('input'); i.type = 'hidden'; i.name = k; i.value = v; form.append(i); }
  document.body.append(form); form.submit(); form.remove();
}

document.addEventListener('click', e => {
  const nav = e.target.closest('[data-nav]');
  if (nav) { e.preventDefault(); cwd = nav.dataset.nav; $('#q').value = ''; history.replaceState(null, '', cwd ? '#' + encodeURIComponent(cwd) : location.pathname); render(); scrollTo(0, 0); return; }
  if (e.target.closest('a.dlb')) return; // native download
  const z = e.target.closest('[data-zip]');
  if (z) { const it = items[+z.dataset.zip]; zip({prefix: it.path + '/', name: it.name}); return; }
  const pk = e.target.closest('[data-pick]');
  if (pk) { const id = +pk.dataset.pick; sel.has(id) ? sel.delete(id) : sel.add(id); render(); return; }
  const card = e.target.closest('.item');
  if (card) {
    const it = items[+card.dataset.i];
    if (it.type === 'dir') { cwd = it.path; history.replaceState(null, '', '#' + encodeURIComponent(cwd)); render(); scrollTo(0, 0); }
    else if (sel.size) { sel.has(it.f.id) ? sel.delete(it.f.id) : sel.add(it.f.id); render(); }
    else openPreview(+card.dataset.i);
  }
});
document.querySelectorAll('[data-view]').forEach(b => b.onclick = () => { view = b.dataset.view; localStorage.smView = view; render(); });
$('#q').oninput = render;
$('#dl-all').onclick = () => { if (D.files.length === 1) location.href = base + 'f/0?dl=1'; else zip({}); };
$('#sel-clear').onclick = () => { sel.clear(); render(); };
$('#sel-dl').onclick = () => {
  const ids = [...sel];
  if (ids.length === 1) location.href = base + 'f/' + ids[0] + '?dl=1';
  else zip({ids: ids.join(','), name: D.name + ' (selection)'});
};

// ---- preview
let cur = -1;
function openPreview(i) {
  const it = items[i];
  if (!it || it.type !== 'file') return;
  cur = i;
  const f = it.f, url = base + 'f/' + f.id, name = f.rel.split('/').pop();
  $('#m-name').textContent = name;
  $('#m-dl').href = url + '?dl=1';
  $('#m-dl').setAttribute('download', name);
  const b = $('#m-body');
  const ext = name.split('.').pop().toLowerCase();
  if (f.kind === 'image' && ext !== 'heic') b.innerHTML = `<img src="${url}" alt="">`;
  else if (f.kind === 'video') b.innerHTML = `<video src="${url}" controls autoplay playsinline preload="metadata"></video>`;
  else if (f.kind === 'audio') b.innerHTML = `<audio src="${url}" controls autoplay></audio>`;
  else if (f.kind === 'pdf') b.innerHTML = `<iframe src="${url}" title="PDF preview"></iframe>`;
  else if (f.kind === 'text') {
    b.innerHTML = '<div class="spin"></div>';
    fetch(url, {headers: {Range: 'bytes=0-262143'}}).then(r => r.text()).then(t => {
      if (cur !== i) return;
      b.innerHTML = '<pre></pre>';
      $('pre', b).textContent = t + (f.size > 262144 ? '\n\n… (preview limited to the first 256 KB. Download to see the rest)' : '');
    });
  } else b.innerHTML = `<div class="nopv">${ic(f.kind)}<p>No preview for this file type.<br>${fmt(f.size)}</p></div>`;
  $('#modal').hidden = false;
  document.body.style.overflow = 'hidden';
}
function closePreview() { $('#modal').hidden = true; $('#m-body').innerHTML = ''; document.body.style.overflow = ''; cur = -1; }
function step(d) {
  for (let i = cur + d; i >= 0 && i < items.length; i += d) if (items[i].type === 'file') return openPreview(i);
}
$('#m-close').onclick = closePreview;
$('#modal').onclick = e => { if (e.target.id === 'modal') closePreview(); };
$('#m-prev').onclick = () => step(-1);
$('#m-next').onclick = () => step(1);
document.addEventListener('keydown', e => {
  if ($('#modal').hidden) return;
  if (e.key === 'Escape') closePreview();
  if (e.key === 'ArrowLeft') step(-1);
  if (e.key === 'ArrowRight') step(1);
});

// ---- upload back to sharer
function upload(files) {
  if (!files.length) return;
  const fd = new FormData();
  let total = 0;
  for (const f of files) { fd.append('file', f, f.name); total += f.size; }
  const x = new XMLHttpRequest();
  const prog = $('#up-prog'), bar = $('#up-prog .bar i'), txt = $('#up-txt');
  prog.hidden = false;
  const t0 = Date.now();
  x.upload.onprogress = e => {
    const p = e.total ? e.loaded / e.total * 100 : 0;
    bar.style.width = p + '%';
    const sp = e.loaded / Math.max(.001, (Date.now() - t0) / 1000);
    txt.textContent = `${p.toFixed(0)}% · ${fmt(sp)}/s`;
  };
  x.onload = () => { txt.textContent = x.status === 200 ? `Sent ${files.length} file${files.length > 1 ? 's' : ''} ✓` : 'Upload failed'; bar.style.width = x.status === 200 ? '100%' : '0'; };
  x.onerror = () => { txt.textContent = 'Upload failed. Connection lost'; };
  x.open('POST', base + 'up');
  x.send(fd);
}
$('#up-input').onchange = e => upload([...e.target.files]);
const drop = $('#drop');
['dragenter', 'dragover'].forEach(ev => drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.add('over'); }));
['dragleave', 'drop'].forEach(ev => drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.remove('over'); }));
drop.addEventListener('drop', e => upload([...e.dataTransfer.files]));

// ---- chat with the sharer
const chatKey = (() => {
  let k = localStorage.smChatKey;
  if (!k || k.length < 16) {
    const a = new Uint8Array(18); crypto.getRandomValues(a);
    k = btoa(String.fromCharCode(...a)).replace(/[+/=]/g, c => ({'+': '-', '/': '_', '=': ''}[c]));
    localStorage.smChatKey = k;
  }
  return k;
})();
const chatId = 'share:' + base.split('/')[2];
let chatRev = -1;
async function chatReq(method, q, body) {
  const r = await fetch(base + 'chat' + q, {method, cache: 'no-store', headers: {'X-SM-Chat': chatKey, 'Content-Type': 'application/json'}, body: body && JSON.stringify(body)});
  if (r.status === 404) throw new Error('Chat isn\'t available for this share.');
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || 'Chat error');
  return j;
}
const chatOpts = {
  key: chatId, title: 'Chat with the sharer', hostLabel: 'Sharer',
  get: since => chatReq('GET', `?since=${since}&name=${encodeURIComponent(localStorage.smChatName || '')}`),
  post: body => chatReq('POST', '', {...body, name: localStorage.smChatName || ''}),
  name: () => localStorage.smChatName || '',
  setName: n => { localStorage.smChatName = n.trim().slice(0, 32); },
  onSeen: () => { $('#chat-badge').textContent = ''; },
};
$('#chat-fab').onclick = () => SMChat.open(chatOpts);
async function chatBadge() {
  if (SMChat.current !== chatId) {
    try {
      const v = await chatReq('GET', `?since=${chatRev}&name=${encodeURIComponent(localStorage.smChatName || '')}`);
      $('#chat-fab').hidden = false;
      if (!v.same) {
        chatRev = v.rev;
        const n = SMChat.unread(chatId, v.seq);
        $('#chat-badge').textContent = n ? (n > 99 ? '99+' : n) : '';
      }
    } catch { /* old sender or offline */ }
  } else chatRev = -1;
  setTimeout(chatBadge, document.hidden ? 60000 : 15000);
}
setTimeout(chatBadge, 800);

load();
