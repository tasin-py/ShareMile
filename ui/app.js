'use strict';
const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

async function rpc(method, args = {}) {
  const r = await fetch('rpc/' + method, {method: 'POST', headers: {'X-SM': '1', 'Content-Type': 'application/json'}, body: JSON.stringify(args)});
  const j = await r.json();
  if (j.error) throw new Error(j.error);
  return j.ok;
}

function fmtBytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 2 : n < 100 ? 1 : 0) : n) + ' ' + u[i];
}
const fmtSpeed = b => b > 1 ? fmtBytes(b) + '/s' : '0 B/s';

function toast(msg, err) {
  const t = document.createElement('div');
  t.className = 'toast' + (err ? ' err' : '');
  t.textContent = msg;
  $('#toasts').append(t);
  setTimeout(() => t.remove(), err ? 6000 : 3000);
}

async function copy(text) {
  try { await navigator.clipboard.writeText(text); }
  catch {
    const ta = document.createElement('textarea');
    ta.value = text; document.body.append(ta); ta.select();
    document.execCommand('copy'); ta.remove();
  }
  toast('Copied to clipboard');
}

// run an async action with a busy button (fixed size, spinner)
async function busyBtn(btn, fn) {
  if (btn.disabled) return;
  const w = btn.offsetWidth;
  btn.style.minWidth = w + 'px';
  btn.disabled = true;
  const sp = $('.spin', btn);
  if (sp) sp.hidden = false;
  try { return await fn(); }
  catch (e) { toast(e.message, true); }
  finally { btn.disabled = false; if (sp) sp.hidden = true; btn.style.minWidth = ''; }
}

let S = null; // last state

// ------------------------------------------------------------- navigation
$$('.nav').forEach(b => b.onclick = () => {
  $('main').scrollTop = 0;
  $$('.nav').forEach(x => x.classList.toggle('on', x === b));
  $$('.page').forEach(p => p.classList.toggle('on', p.id === 'page-' + b.dataset.page));
});
$$('.tab').forEach(b => b.onclick = () => {
  $$('.tab').forEach(x => x.classList.toggle('on', x === b));
  $$('.tabpane').forEach(p => p.classList.toggle('on', p.id === 'tab-' + b.dataset.tab));
});
document.addEventListener('click', e => {
  const a = e.target.closest('[data-url]');
  if (a) { e.preventDefault(); rpc('openURL', {url: a.dataset.url}).catch(er => toast(er.message, true)); }
});

// ------------------------------------------------------------- pickers
const picks = {qs: [], hn: []};

async function addPaths(target, paths) {
  for (const p of paths) if (p && !picks[target].includes(p)) picks[target].push(p);
  await rescanPick(target);
}

async function rescanPick(target) {
  const chips = $('#' + target + '-chips'), scan = $('#' + target + '-scan');
  const btn = $(target === 'qs' ? '#qs-start' : '#hn-create');
  chips.innerHTML = picks[target].map((p, i) => `<span class="chip" title="${esc(p)}"><span>${esc(p)}</span><button data-rm="${i}" aria-label="Remove">✕</button></span>`).join('');
  $$('[data-rm]', chips).forEach(b => b.onclick = () => { picks[target].splice(+b.dataset.rm, 1); rescanPick(target); });
  if (!picks[target].length) { scan.innerHTML = ''; btn.disabled = true; return; }
  scan.innerHTML = '<span class="spin"></span> Scanning files…';
  btn.disabled = true;
  try {
    const r = await rpc('scan', {paths: picks[target]});
    picks[target] = r.paths;
    const kinds = Object.entries(r.kinds).sort((a, b) => b[1] - a[1]).map(([k, v]) => `${v} ${k}${v > 1 ? 's' : ''}`).join(', ');
    scan.innerHTML = `Found <b>${r.count.toLocaleString()} file${r.count > 1 ? 's' : ''}</b> · <b>${fmtBytes(r.total)}</b> <span>(${esc(kinds)})</span>`;
    btn.disabled = false;
  } catch (e) {
    scan.textContent = e.message;
  }
}

$$('[data-pick]').forEach(b => b.onclick = () => busyBtn(b, async () => {
  const paths = await rpc('pick', {folders: b.dataset.pick === 'folders'});
  if (paths && paths.length) await addPaths(b.dataset.target, paths);
}));
for (const t of ['qs', 'hn']) {
  $('#' + t + '-path').addEventListener('keydown', e => {
    if (e.key === 'Enter' && e.target.value.trim()) {
      addPaths(t, [e.target.value.trim().replace(/^"|"$/g, '')]);
      e.target.value = '';
    }
  });
}

$('#qs-start').onclick = e => busyBtn(e.currentTarget, async () => {
  await rpc('createShare', {paths: picks.qs, allowUpload: $('#qs-upload').checked});
  picks.qs = []; await rescanPick('qs');
  toast('Sharing started');
  await refresh();
});
$('#hn-create').onclick = e => busyBtn(e.currentTarget, async () => {
  await rpc('hostNet', {name: $('#hn-name').value, paths: picks.hn});
  picks.hn = []; $('#hn-name').value = ''; await rescanPick('hn');
  toast('Network created. Share the secret code');
  await refresh();
});

// ------------------------------------------------------------- quick shares
const shareSel = {}; // share id -> link kind
const qrCache = {};

async function qrFor(text) {
  if (!qrCache[text]) qrCache[text] = await rpc('qr', {text});
  return qrCache[text];
}

function renderShares(list) {
  const box = $('#qs-list');
  $('#qs-empty').hidden = list.length > 0;
  const ids = new Set(list.map(s => s.id));
  $$('.share-card', box).forEach(c => { if (!ids.has(c.dataset.id)) c.remove(); });
  list.forEach((s, idx) => {
    let c = $(`.share-card[data-id="${s.id}"]`, box);
    if (!c) {
      c = document.createElement('div');
      c.className = 'card share-card';
      c.dataset.id = s.id;
      c.innerHTML = `<div class="share">
        <div class="qr"><img alt="QR code"></div>
        <div class="share-body">
          <div class="share-title"><h3></h3><button class="btn small danger" data-act="stop">Stop sharing</button></div>
          <div class="seg"></div>
          <div class="link"><input readonly spellcheck="false"><button class="btn" data-act="copy">Copy link</button><button class="btn" data-act="open" title="Open in browser">Open</button></div>
          <div class="note"></div>
          <div class="stats">
            <span><b data-f="count"></b> files · <b data-f="total"></b></span>
            <span>Visitors <b data-f="visits"></b></span>
            <span>Sent <b data-f="sent"></b></span>
            <span>Speed <b class="stat-speed" data-f="speed"></b></span>
          </div>
          <div class="recv" hidden></div>
        </div></div>`;
      $('[data-act=stop]', c).onclick = e => busyBtn(e.currentTarget, async () => { await rpc('stopShare', {id: s.id}); await refresh(); });
      $('[data-act=copy]', c).onclick = () => copy($('.link input', c).value);
      $('[data-act=open]', c).onclick = () => rpc('openURL', {url: $('.link input', c).value});
      box.insertBefore(c, box.children[idx] || null);
    }
    $('h3', c).textContent = s.name;
    const links = s.links || [];
    // Only the user's own click is remembered; otherwise Relay (first) is shown,
    // or the link type picked in Settings → Preferred link.
    const mode = S.config.linkMode;
    let cur = links.find(l => l.kind === shareSel[s.id])
      || (mode !== 'auto' && links.find(l => l.kind === mode))
      || links[0];
    const seg = $('.seg', c);
    const n = S.net;
    // BDIX Transfer only hands out its link once a test from outside passed.
    const isBdix = cur && cur.kind === 'public';
    const bdixReady = n.bdix === 'ready';
    const segKey = links.map(l => l.kind + l.url + l.note).join('|') + (cur && cur.kind) + '|' + n.bdix + n.bdixMsg;
    if (seg.dataset.k !== segKey) {
      seg.dataset.k = segKey;
      seg.innerHTML = links.map((l, i) => `<button data-i="${i}" class="${cur && l.kind === cur.kind ? 'on' : ''}">${esc(l.label)}</button>`).join('');
      $$('button', seg).forEach(b => b.onclick = () => {
        const k = links[+b.dataset.i].kind;
        shareSel[s.id] = k;
        if (k === 'public' && !['ready', 'checking'].includes(S.net.bdix)) { rpc('bdixCheck').then(refresh); S.net.bdix = 'checking'; S.net.bdixMsg = 'Testing from the internet whether people can reach this PC directly…'; }
        seg.dataset.k = ''; renderShares(S.shares);
      });
      const inp = $('.link input', c), note = $('.note', c), img = $('.qr img', c);
      let url = cur ? cur.url : '', text = cur ? cur.note : '', status = '';
      if (isBdix && !bdixReady) {
        url = '';
        status = {checking: 'Checking BDIX Transfer…', noRealIP: '[Real IP Not enabled]', needForward: '[Port forwarding needed]', firewall: '[Blocked by Windows Firewall]', error: '[Check failed]'}[n.bdix] || 'Checking BDIX Transfer…';
        text = n.bdixMsg || '';
      } else if (isBdix) {
        text = 'Reachable from the internet ✓ ' + cur.note;
      } else if (cur && !url) {
        status = cur.kind === 'relay' ? 'Starting the relay…' : 'Not available';
      }
      inp.value = url || status || (cur ? '' : 'No network connection');
      inp.classList.toggle('status', !url);
      $('[data-act=copy]', c).disabled = !url;
      $('[data-act=open]', c).disabled = !url;
      note.innerHTML = esc(text) + (isBdix && !['ready', 'checking'].includes(n.bdix) ? ' <button class="btn small" data-act="bdix">Check again</button>' : '');
      const again = $('[data-act=bdix]', note);
      if (again) again.onclick = e => busyBtn(e.currentTarget, async () => { await rpc('bdixCheck'); await refresh(); });
      $('.qr', c).classList.toggle('off', !url);
      if (url) qrFor(url).then(src => { img.src = src; }); else img.removeAttribute('src');
    }
    const f = k => $(`[data-f="${k}"]`, c);
    f('count').textContent = s.count.toLocaleString();
    f('total').textContent = fmtBytes(s.total);
    f('visits').textContent = s.visits;
    f('sent').textContent = fmtBytes(s.sent);
    f('speed').textContent = fmtSpeed(s.speed);
    const recv = $('.recv', c);
    if (s.allowUpload) {
      recv.hidden = false;
      const k = String(s.received ? s.received.length : 0);
      if (recv.dataset.k !== k) {
        recv.dataset.k = k;
        recv.innerHTML = s.received && s.received.length
          ? '<b>Received</b>' + s.received.slice(-6).reverse().map(p => `<a href="#" data-path="${esc(p)}">${esc(p.split('\\').pop())}</a>`).join('')
          : '<span class="muted">Uploads are on. Files receivers send you appear here.</span>';
        $$('[data-path]', recv).forEach(a => a.onclick = e => { e.preventDefault(); rpc('openPath', {path: a.dataset.path}); });
      }
    }
  });
}

// ------------------------------------------------------------- hosted networks
function renderHosted(list) {
  const box = $('#hn-list');
  const ids = new Set(list.map(h => h.id));
  $$('.hn-card', box).forEach(c => { if (!ids.has(c.dataset.id)) c.remove(); });
  list.forEach((h, idx) => {
    let c = $(`.hn-card[data-id="${h.id}"]`, box);
    if (!c) {
      c = document.createElement('div');
      c.className = 'card hn-card';
      c.dataset.id = h.id;
      c.innerHTML = `<div class="share-title"><h3></h3><span class="pill" data-f="state"></span></div>
        <p class="note">Secret code. Anyone with this code and the ShareMile app can join and download.</p>
        <div class="code" data-f="code"></div>
        <div class="row gap wrap" style="margin-top:10px">
          <button class="btn primary small" data-act="copy">Copy code</button>
          <button class="btn small" data-act="pause"></button>
          <button class="btn small" data-act="rescan"><span class="spin" hidden></span>Rescan files</button>
          <button class="btn small danger" data-act="del">Delete network</button>
        </div>
        <div class="stats" style="margin-top:12px">
          <span><b data-f="count"></b> files · <b data-f="total"></b></span>
          <span>Members online <b data-f="peers"></b></span>
          <span>Sent <b data-f="sent"></b></span>
          <span>Speed <b class="stat-speed" data-f="speed"></b></span>
        </div>
        <div class="warn" data-f="err" hidden></div>`;
      $('[data-act=copy]', c).onclick = () => copy($('[data-f=code]', c).textContent);
      $('[data-act=pause]', c).onclick = e => busyBtn(e.currentTarget, async () => { await rpc('pauseHostNet', {id: h.id, on: !c._active}); await refresh(); });
      $('[data-act=rescan]', c).onclick = e => busyBtn(e.currentTarget, async () => { await rpc('rescanHostNet', {id: h.id}); toast('Files rescanned'); await refresh(); });
      $('[data-act=del]', c).onclick = e => { if (confirm('Delete this network? Members will lose access and the code stops working.')) busyBtn(e.currentTarget, async () => { await rpc('removeHostNet', {id: h.id}); await refresh(); }); };
      box.insertBefore(c, box.children[idx] || null);
    }
    c._active = h.active;
    const f = k => $(`[data-f="${k}"]`, c);
    $('h3', c).textContent = h.name;
    if (f('code').textContent !== h.code) f('code').textContent = h.code;
    f('state').textContent = h.active ? 'Online' : 'Paused';
    f('state').className = 'pill ' + (h.active ? 'ok' : '');
    $('[data-act=pause]', c).textContent = h.active ? 'Pause' : 'Resume';
    f('count').textContent = h.count.toLocaleString();
    f('total').textContent = fmtBytes(h.total);
    f('peers').textContent = h.peers;
    f('sent').textContent = fmtBytes(h.sent);
    f('speed').textContent = fmtSpeed(h.speed);
    f('err').hidden = !h.err;
    f('err').textContent = h.err || '';
  });
}

// ------------------------------------------------------------- joined networks
const jsel = {};    // net id -> Set(file ids)
const jfilter = {}; // net id -> search text

$('#jn-join').onclick = e => busyBtn(e.currentTarget, async () => {
  const code = $('#jn-code').value.trim();
  if (!code) { toast('Paste a code first', true); return; }
  await rpc('join', {code});
  $('#jn-code').value = '';
  toast('Joined. Connecting to the host…');
  await refresh();
});
$('#jn-code').addEventListener('keydown', e => { if (e.key === 'Enter') $('#jn-join').click(); });

// Receive page: one box for both links and network codes.
$('#rc-go').onclick = e => busyBtn(e.currentTarget, async () => {
  const v = $('#rc-in').value.trim();
  if (!v) { toast('Paste a link or a code first', true); return; }
  if (/^https?:\/\//i.test(v)) {
    await rpc('openURL', {url: v});
    toast('Opened in your browser');
  } else if (/^SM-/i.test(v)) {
    await rpc('join', {code: v});
    toast('Joined. Connecting to the host…');
    $('[data-page=network]').click();
    $('[data-tab=join]').click();
    await refresh();
  } else {
    toast('That doesn\'t look like a ShareMile link or code', true);
    return;
  }
  $('#rc-in').value = '';
});
$('#rc-in').addEventListener('keydown', e => { if (e.key === 'Enter') $('#rc-go').click(); });

function renderJoined(list) {
  const box = $('#jn-list');
  const ids = new Set(list.map(j => j.id));
  $$('.jn-card', box).forEach(c => { if (!ids.has(c.dataset.id)) c.remove(); });
  list.forEach((j, idx) => {
    let c = $(`.jn-card[data-id="${j.id}"]`, box);
    if (!jsel[j.id]) jsel[j.id] = new Set();
    if (!c) {
      c = document.createElement('div');
      c.className = 'card jn-card';
      c.dataset.id = j.id;
      c.innerHTML = `<div class="share-title"><h3></h3><div class="row gap"><span class="pill" data-f="status"></span><button class="btn small" data-act="reconnect">Reconnect</button><button class="btn small danger" data-act="leave">Leave</button></div></div>
        <div class="stats" style="margin:8px 0 12px"><span>Other members <b data-f="peers"></b></span><span>You uploaded <b data-f="up"></b></span></div>
        <label class="check" style="margin-bottom:12px"><input type="checkbox" data-act="seed"><span></span>Share what I download with other members (faster for everyone)</label>
        <div class="warn" data-f="err" hidden></div>
        <div class="fb">
          <div class="fb-head">
            <input type="search" placeholder="Search files" data-act="search">
            <button class="btn small" data-act="all">Select all</button>
            <button class="btn small" data-act="none">Clear</button>
            <button class="btn small primary" data-act="dl">Download selected</button>
          </div>
          <div class="fb-list"></div>
        </div>
        <div class="row between" style="margin:14px 0 8px"><b>Downloads</b><div class="row gap"><button class="btn small" data-act="openfolder">Open folder</button><button class="btn small" data-act="clear">Clear finished</button></div></div>
        <div class="tasks"></div>`;
      $('[data-act=leave]', c).onclick = e => { if (confirm('Leave this network?')) busyBtn(e.currentTarget, async () => { await rpc('leave', {id: j.id}); await refresh(); }); };
      $('[data-act=reconnect]', c).onclick = e => busyBtn(e.currentTarget, async () => { await rpc('reconnect', {id: j.id}); toast('Reconnecting…'); });
      $('[data-act=seed]', c).onchange = e => rpc('setSeed', {id: j.id, on: e.target.checked});
      $('[data-act=search]', c).oninput = e => { jfilter[j.id] = e.target.value.toLowerCase(); c._fk = ''; renderJoined(S.joined); };
      $('[data-act=all]', c).onclick = () => { visibleFiles(c._j).forEach(f => jsel[j.id].add(f.id)); c._fk = ''; renderJoined(S.joined); };
      $('[data-act=none]', c).onclick = () => { jsel[j.id].clear(); c._fk = ''; renderJoined(S.joined); };
      $('[data-act=dl]', c).onclick = e => busyBtn(e.currentTarget, async () => {
        const ids = [...jsel[j.id]];
        if (!ids.length) { toast('Select some files first', true); return; }
        const n = await rpc('download', {id: j.id, fileIds: ids});
        jsel[j.id].clear(); c._fk = '';
        toast(n ? `Downloading ${n} file${n > 1 ? 's' : ''}` : 'Those files are already downloading');
        await refresh();
      });
      $('[data-act=openfolder]', c).onclick = () => rpc('openPath', {});
      $('[data-act=clear]', c).onclick = async () => { await rpc('clearDone', {id: j.id}); await refresh(); };
      box.insertBefore(c, box.children[idx] || null);
    }
    c._j = j;
    const f = k => $(`[data-f="${k}"]`, c);
    $('h3', c).textContent = j.name;
    const st = {online: ['Connected', 'ok'], connecting: ['Connecting…', ''], offline: ['Host offline', 'bad']}[j.status] || [j.status, ''];
    f('status').textContent = st[0];
    f('status').className = 'pill ' + st[1];
    f('peers').textContent = j.peers;
    f('up').textContent = fmtBytes(j.uploaded);
    $('[data-act=seed]', c).checked = j.seed;
    f('err').hidden = !(j.err && j.status !== 'online');
    f('err').textContent = j.err || '';

    // file list (rebuilt only when it changes)
    const files = visibleFiles(j);
    const fk = (j.files || []).length + ':' + files.map(x => x.id).join(',').length + ':' + [...jsel[j.id]].join(',') + ':' + Object.keys(j.have || {}).join(',');
    if (c._fk !== fk) {
      c._fk = fk;
      const list = $('.fb-list', c);
      if (!j.files) list.innerHTML = `<div class="empty">${j.status === 'online' ? 'No files' : 'Waiting for the host…'}</div>`;
      else if (!files.length) list.innerHTML = '<div class="empty">No matching files</div>';
      else {
        const shown = files.slice(0, 1500);
        list.innerHTML = shown.map(x => {
          const parts = x.rel.split('/'); const nm = parts.pop();
          return `<label class="fb-row"><input type="checkbox" data-id="${x.id}" ${jsel[j.id].has(x.id) ? 'checked' : ''}>
            <span class="nm" title="${esc(x.rel)}">${parts.length ? `<span class="dir">${esc(parts.join('/'))}/</span>` : ''}${esc(nm)}</span>
            <span class="tag">${j.have && j.have[x.id] ? 'SAVED' : ''}</span><span class="sz">${fmtBytes(x.size)}</span></label>`;
        }).join('') + (files.length > shown.length ? `<div class="empty">${files.length - shown.length} more. Use search to narrow down.</div>` : '');
        $$('input[data-id]', list).forEach(cb => cb.onchange = () => { cb.checked ? jsel[j.id].add(cb.dataset.id) : jsel[j.id].delete(cb.dataset.id); c._fk = ''; });
      }
    }
    // tasks
    const tb = $('.tasks', c);
    const tasks = j.tasks || [];
    if (!tasks.length) { if (!tb._empty) { tb.innerHTML = '<div class="muted" style="font-size:13px">Nothing downloading. Select files above.</div>'; tb._empty = true; } }
    else {
      if (tb._empty) { tb.innerHTML = ''; tb._empty = false; }
      const tids = new Set(tasks.map(t => t.file.id));
      $$('.task', tb).forEach(el => { if (!tids.has(el.dataset.id)) el.remove(); });
      tasks.forEach(t => {
        let el = $(`.task[data-id="${t.file.id}"]`, tb);
        if (!el) {
          el = document.createElement('div');
          el.className = 'task'; el.dataset.id = t.file.id;
          el.innerHTML = `<span class="nm"></span><span class="meta"></span><div class="bar"><i></i></div><div class="row gap" style="grid-column:1/-1"><button class="btn small" data-act="x">Cancel</button></div>`;
          tb.append(el);
        }
        $('.nm', el).textContent = t.file.rel;
        $('.nm', el).title = t.file.rel;
        const pct = t.state === 'done' ? 100 : t.file.size ? Math.min(100, t.done / t.file.size * 100) : 0;
        const label = {queued: 'Queued', preparing: 'Preparing…', downloading: `${pct.toFixed(1)}% · ${fmtSpeed(t.speed)} · ${t.sources} source${t.sources > 1 ? 's' : ''}`, done: 'Done · ' + fmtBytes(t.file.size), error: 'Failed', cancelled: 'Cancelled'}[t.state] || t.state;
        $('.meta', el).textContent = label;
        $('.bar', el).classList.toggle('indet', t.state === 'preparing' || t.state === 'queued');
        $('.bar i', el).style.width = pct + '%';
        const act = $('[data-act=x]', el);
        const running = t.state === 'downloading' || t.state === 'preparing' || t.state === 'queued';
        const wantMode = running ? 'cancel' : t.state === 'done' ? 'show' : 'retry';
        if (act.dataset.mode !== wantMode) {
          act.dataset.mode = wantMode;
          act.textContent = {cancel: 'Cancel', show: 'Show in folder', retry: 'Retry'}[wantMode];
        }
        act.onclick = () => {
          if (wantMode === 'cancel') rpc('cancel', {id: j.id, fileIds: [t.file.id]});
          else if (wantMode === 'show') rpc('openPath', {path: t.path});
          else rpc('download', {id: j.id, fileIds: [t.file.id]}).then(refresh);
        };
        el.title = t.err || '';
      });
    }
  });
}

function visibleFiles(j) {
  const q = jfilter[j.id] || '';
  return (j.files || []).filter(f => !q || f.rel.toLowerCase().includes(q));
}

// ------------------------------------------------------------- settings
const THEMES = [
  ['pitch-black', 'Pitch Black', '#000', '#2ee6a6', '#f4f4f5'],
  ['midnight-blue', 'Midnight Blue', '#070d1f', '#6ea8ff', '#eef2ff'],
  ['graphite', 'Graphite', '#16171a', '#e8e8ea', '#9aa0ad'],
  ['noir-gold', 'Noir Gold', '#0a0907', '#e8b84a', '#f7f1e3'],
  ['aurora', 'Aurora', '#03110f', '#42f5c8', '#b38cff'],
  ['light', 'Light', '#f4f5f7', '#0b7a5c', '#0f1115'],
  ['paper', 'Paper', '#f6f1e7', '#9a4b12', '#1d1a14'],
];
$('#themes').innerHTML = THEMES.map(([id, n, bg, a, b]) => `<button class="theme" data-theme-id="${id}"><span class="sw" style="background:${bg}"><i style="background:${a}"></i><i style="background:${b}"></i></span><span class="tn">${n}</span></button>`).join('');
$$('[data-theme-id]').forEach(b => b.onclick = async () => { applyTheme(b.dataset.themeId); await rpc('setConfig', {theme: b.dataset.themeId}); });
function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  $$('[data-theme-id]').forEach(b => b.classList.toggle('on', b.dataset.themeId === t));
  if (window.smSetDarkTitle) window.smSetDarkTitle(!(t === 'light' || t === 'paper'));
}

$('#set-linkmode').onchange = e => rpc('setConfig', {linkMode: e.target.value}).then(refresh);
$('#set-upnp').onchange = e => rpc('setConfig', {useUpnp: e.target.checked}).then(() => toast(e.target.checked ? 'UPnP on. Opening the port…' : 'UPnP off'));
$('#set-port-save').onclick = e => busyBtn(e.currentTarget, async () => { await rpc('setConfig', {port: +$('#set-port').value}); toast('Port changed. Old links stop working'); await refresh(); });
$('#set-port').addEventListener('wheel', e => { if (document.activeElement !== e.target) return; e.preventDefault(); e.target.value = Math.min(65535, Math.max(1024, (+e.target.value || 8750) + (e.deltaY < 0 ? 1 : -1))); }, {passive: false});
$('#set-dldir-btn').onclick = e => busyBtn(e.currentTarget, async () => { const p = await rpc('pickDownloadDir'); if (p) toast('Download folder changed'); await refresh(); });
$('#set-dldir-open').onclick = () => rpc('openPath', {});
$('#net-refresh').onclick = e => busyBtn(e.currentTarget, async () => { await rpc('refreshNetwork'); await new Promise(r => setTimeout(r, 3500)); await refresh(); });
const fwClick = e => busyBtn(e.currentTarget, async () => { await rpc('firewall'); toast('Firewall rule requested'); setTimeout(refresh, 2500); });
$('#fw-btn').onclick = fwClick;
$$('[data-fw]').forEach(b => b.onclick = fwClick);
$('#set-autorelay').onchange = e => rpc('setConfig', {autoRelay: e.target.checked}).then(() => toast(e.target.checked ? 'Auto relay on' : 'Auto relay off'));
$('#relay-btn').onclick = e => busyBtn(e.currentTarget, async () => { const on = !(S && ['on', 'starting', 'downloading'].includes(S.net.relayState)); await rpc('relay', {on}); await refresh(); });

function renderSettings(s) {
  const n = s.net, c = s.config;
  const upnp = {off: 'Off', idle: 'Closed. Opens only while you share', searching: 'Opening…', mapped: 'Port open while sharing ✓', failed: 'Refused by router', none: 'Not supported by router'}[n.upnp] || n.upnp;
  $('#net-info').innerHTML = [
    ['Local IP', (n.lanIPs || []).join(', ') || '-'],
    ['Public IP', n.publicIP || (n.checked ? 'Not detected' : 'Checking…')],
    ['IPv6', n.ipv6 || 'None'],
    ['Router WAN IP', n.routerWAN || '-'],
    ['Real IP', {yes: 'Yes ✓ (BDIX Transfer possible)', no: 'No Real IP Found', unknown: 'Not known yet'}[n.realIP] || 'Not known yet'],
    ['UPnP', upnp],
    ['Listening on', 'TCP ' + s.listenPort],
  ].map(([k, v]) => `<div>${k}</div><div>${esc(v)}</div>`).join('');
  const warn = [];
  if (s.publicErr) warn.push(esc(s.publicErr));
  if (n.realIP === 'no') warn.push('<b>No Real IP Found</b><br>' + esc('Your links use the Relay. To enable BDIX Transfer, ask your ISP for a real IP.'));
  else if (n.bdix === 'needForward') warn.push('<b>Port forwarding needed</b><br>' + esc(n.bdixMsg));
  $('#net-warn').hidden = !warn.length;
  $('#net-warn').innerHTML = warn.join('<br>');
  $('#fw-state').textContent = s.firewall === 'allowed' ? 'Firewall rule present ✓' : s.firewall === 'missing' ? 'No firewall rule yet' : '';
  if (document.activeElement !== $('#set-port')) $('#set-port').value = c.port;
  $('#set-linkmode').value = c.linkMode;
  $('#set-upnp').checked = c.useUpnp;
  $('#set-autorelay').checked = c.autoRelay;
  $$('.fwwarn').forEach(w => w.hidden = s.firewall !== 'missing');
  $('#set-dldir').value = c.downloadDir;
  const rs = {off: 'Off', downloading: 'Downloading helper…', starting: 'Starting…', on: 'On: ' + n.relayURL, error: n.relayErr || 'Error'}[n.relayState] || n.relayState;
  $('#relay-state').textContent = rs;
  $('#relay-btn').textContent = ['on', 'starting', 'downloading'].includes(n.relayState) ? 'Turn relay off' : 'Turn relay on';
  if (document.documentElement.dataset.theme !== c.theme) applyTheme(c.theme);

  // sidebar pill
  const pill = $('#netpill');
  let txt = 'Local network only', cls = 'mid';
  if (!n.checked) { txt = 'Checking network…'; cls = ''; }
  else if (n.upnp === 'idle') { txt = 'Ready. Nothing exposed'; cls = 'ok'; }
  else if (n.publicOK) { txt = 'Internet links ready'; cls = 'ok'; }
  else if (n.relayState === 'on') { txt = 'Relay active'; cls = 'ok'; }
  else if (n.ipv6) { txt = 'LAN + IPv6 ready'; cls = 'ok'; }
  pill.className = 'netpill ' + cls;
  $('span', pill).textContent = txt;
}

// ------------------------------------------------------------- about / update
let updInfo = null;
$('#upd-btn').onclick = e => busyBtn(e.currentTarget, async () => {
  const msg = $('#upd-msg');
  const lbl = $('.lbl', e.currentTarget);
  if (updInfo && updInfo.available) {
    lbl.textContent = 'Updating…';
    msg.textContent = 'Downloading ' + updInfo.tag + '. ShareMile restarts by itself when it\'s done.';
    try { await rpc('applyUpdate'); }
    catch (er) { msg.textContent = er.message; lbl.textContent = 'Update now'; throw er; }
    return;
  }
  lbl.textContent = 'Checking…';
  try {
    updInfo = await rpc('checkUpdate');
    if (updInfo.available) { msg.textContent = 'Version ' + updInfo.tag + ' is available.'; lbl.textContent = 'Update now'; }
    else { msg.textContent = 'You\'re on the latest version (' + S.version + ').'; lbl.textContent = 'Check for Updates'; }
  } catch (er) { msg.textContent = er.message; lbl.textContent = 'Check for Updates'; }
});

// ------------------------------------------------------------- polling
let timer = null;
async function refresh() {
  try {
    S = await rpc('state');
    $('#ab-ver').textContent = S.version;
    renderShares(S.shares || []);
    renderHosted(S.hosted || []);
    renderJoined(S.joined || []);
    renderSettings(S);
  } catch (e) { /* app closing */ }
}
function loop() {
  clearTimeout(timer);
  if (document.hidden) return;
  refresh().finally(() => { timer = setTimeout(loop, 1000); });
}
document.addEventListener('visibilitychange', loop);
loop();
