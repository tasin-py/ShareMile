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

// ------------------------------------------------------------- sorting
const SORTS = [
  ['name-asc', 'Name (A → Z)'], ['name-desc', 'Name (Z → A)'],
  ['size-desc', 'Size (largest first)'], ['size-asc', 'Size (smallest first)'],
  ['date-desc', 'Date modified (newest)'], ['date-asc', 'Date modified (oldest)'],
];
const sortOptions = () => SORTS.map(([v, l]) => `<option value="${v}">${l}</option>`).join('');
let sortMode = localStorage.getItem('sm-sort') || 'name-asc';
function setSort(v) { sortMode = v; localStorage.setItem('sm-sort', v); $$('.sort-sel').forEach(s => s.value = v); }
function sortFiles(list, mode = sortMode) {
  const name = f => f.rel.toLowerCase();
  const cmp = {
    'name-asc': (a, b) => name(a) < name(b) ? -1 : name(a) > name(b) ? 1 : 0,
    'size-desc': (a, b) => b.size - a.size,
    'date-desc': (a, b) => (b.mtime || 0) - (a.mtime || 0),
  };
  const [k, dir] = mode.split('-');
  const base = cmp[k + '-' + (k === 'name' ? 'asc' : 'desc')];
  const flip = (k === 'name') === (dir === 'desc');
  return [...list].sort((a, b) => (flip ? -1 : 1) * base(a, b) || (name(a) < name(b) ? -1 : 1));
}
const fmtDate = ms => ms ? new Date(ms).toLocaleDateString(undefined, {year: 'numeric', month: 'short', day: 'numeric'}) : '';

// How members reach a hosted network.
const METHODS = [
  ['auto', 'Automatic (Relay + every direct path)'],
  ['relay', 'Relay (works anywhere)'],
  ['public', 'BDIX Transfer (needs a real IP)'],
  ['lan', 'Same Wi-Fi / LAN only'],
  ['ipv6', 'IPv6 direct'],
];
const methodOptions = () => METHODS.map(([v, l]) => `<option value="${v}">${l}</option>`).join('');

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
  await rpc('hostNet', {name: $('#hn-name').value, paths: picks.hn, method: $('#hn-method').value});
  picks.hn = []; $('#hn-name').value = ''; await rescanPick('hn');
  toast('Network created. Share the secret code');
  await refresh();
});

$('#hn-method').innerHTML = methodOptions();
$('#rc-sort').innerHTML = sortOptions();
$('#rc-sort').value = sortMode;
$('#rc-sort').onchange = e => { setSort(e.target.value); rcRender(); };

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
          <div class="share-title"><h3></h3><div class="row gap"><button class="btn small" data-act="renew" title="Make a new link. The current one stops working">New link</button><button class="btn small danger" data-act="stop">Stop sharing</button></div></div>
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
      $('[data-act=renew]', c).onclick = e => {
        if (!confirm('Make a new link for this share?\n\nThe current link (and QR code) stops working right away. Anyone who still needs the files will need the new link.')) return;
        busyBtn(e.currentTarget, async () => { await rpc('refreshShare', {id: s.id}); toast('New link ready. Send it again'); await refresh(); });
      };
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
        status = {checking: 'Checking BDIX Transfer…', noRealIP: '[Real IP Not enabled]', needForward: '[Port forwarding needed]', firewall: '[Blocked by Windows Firewall]', error: '[Check failed]'}[n.bdix] || '[Not checked yet]';
        text = n.bdixMsg || 'Press Check now to test whether people on the internet can reach this PC directly (uses portchecker.io).';
      } else if (isBdix) {
        text = 'Reachable from the internet ✓ ' + cur.note;
      } else if (cur && !url) {
        status = cur.kind === 'relay' ? 'Starting the relay…' : 'Not available';
      }
      inp.value = url || status || (cur ? '' : 'No network connection');
      inp.classList.toggle('status', !url);
      $('[data-act=copy]', c).disabled = !url;
      $('[data-act=open]', c).disabled = !url;
      note.innerHTML = esc(text) + (isBdix && !['ready', 'checking'].includes(n.bdix) ? ' <button class="btn small" data-act="bdix">' + (n.bdix ? 'Check again' : 'Check now') + '</button>' : '');
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
          <button class="btn small danger" data-act="renew" title="Replace the secret code. Everyone with the old code loses access">New code</button>
          <button class="btn small danger" data-act="del">Delete network</button>
        </div>
        <div class="stats" style="margin-top:12px">
          <span><b data-f="count"></b> files · <b data-f="total"></b></span>
          <span>Members online <b data-f="peers"></b></span>
          <span>Sent <b data-f="sent"></b></span>
          <span>Speed <b class="stat-speed" data-f="speed"></b></span>
        </div>
        <div class="warn" data-f="err" hidden></div>
        <div class="row gap wrap method-row">
          <label class="method">Members connect via <select class="method-sel" data-act="method">${methodOptions()}</select></label>
          <button class="btn small" data-act="edit">Edit files</button>
        </div>
        <div class="note method-note" data-f="mnote" hidden></div>
        <div class="hn-edit" hidden>
          <div class="row gap wrap"><div class="pathin grow"><input data-e="name" maxlength="60" placeholder="Network name"></div><button class="btn small" data-e="rename">Rename</button></div>
          <div class="he-label">Folders and files in this network</div>
          <div class="chips" data-e="roots"></div>
          <div class="row gap wrap">
            <button class="btn small" data-e="addfiles">Add files</button>
            <button class="btn small" data-e="addfolder">Add folder</button>
            <span class="muted he-hint">Members see changes within about 30 seconds.</span>
          </div>
          <div class="fb">
            <div class="fb-head">
              <input type="search" placeholder="Search files" data-e="q">
              <select class="sort-sel" data-e="sort" title="Sort files">${sortOptions()}</select>
              <button class="btn small" data-e="restoreall" hidden></button>
            </div>
            <div class="fb-list" data-e="files"><div class="empty">Loading…</div></div>
          </div>
        </div>`;
      $('[data-act=copy]', c).onclick = () => copy($('[data-f=code]', c).textContent);
      wireHostEditor(c);
      $('[data-act=pause]', c).onclick = e => busyBtn(e.currentTarget, async () => { await rpc('pauseHostNet', {id: h.id, on: !c._active}); await refresh(); });
      $('[data-act=rescan]', c).onclick = e => busyBtn(e.currentTarget, async () => { await rpc('rescanHostNet', {id: h.id}); toast('Files rescanned'); await refresh(); });
      $('[data-act=renew]', c).onclick = e => {
        if (!confirm('⚠ Make a new secret code?\n\nEveryone who has the current code, including members who already joined, will LOSE ACCESS to this network. Only people you send the new code to can connect again.\n\nContinue?')) return;
        busyBtn(e.currentTarget, async () => { await rpc('renewHostNet', {id: c.dataset.id}); toast('New code ready. Send it to the members you want to keep'); await refresh(); });
      };
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
    c._h = h;
    const msel = $('[data-act=method]', c);
    if (document.activeElement !== msel) msel.value = h.method || 'auto';
    f('mnote').hidden = !h.methodNote;
    f('mnote').textContent = h.methodNote || '';
    if (!$('.hn-edit', c).hidden) {
      const key = h.count + ':' + h.total + ':' + h.excluded + ':' + (h.roots || []).join('|');
      if (c._ek !== key) { c._ek = key; loadHostFiles(c); }
    }
  });
}

// Host-side editor: rename, add/remove folders and files, remove single files.
function wireHostEditor(c) {
  const e = k => $(`[data-e="${k}"]`, c);
  const id = () => c.dataset.id;
  const edit = async (args, msg) => { await rpc('editHostNet', {id: id(), ...args}); if (msg) toast(msg); c._ek = ''; await refresh(); };
  $('[data-act=method]', c).onchange = ev => {
    const v = ev.target.value;
    rpc('setHostMethod', {id: id(), method: v}).then(() => { toast('Members now connect via ' + METHODS.find(m => m[0] === v)[1].replace(/ \(.*\)$/, '') + '. Send the updated code to new members'); refresh(); }).catch(er => toast(er.message, true));
  };
  $('[data-act=edit]', c).onclick = () => {
    const ed = $('.hn-edit', c);
    ed.hidden = !ed.hidden;
    $('[data-act=edit]', c).textContent = ed.hidden ? 'Edit files' : 'Close editor';
    if (!ed.hidden) { e('name').value = c._h.name; e('sort').value = sortMode; c._ek = ''; refresh(); }
  };
  e('rename').onclick = ev => busyBtn(ev.currentTarget, () => edit({name: e('name').value}, 'Network renamed'));
  e('name').addEventListener('keydown', ev => { if (ev.key === 'Enter') e('rename').click(); });
  const addRoots = folders => busyBtn(e(folders ? 'addfolder' : 'addfiles'), async () => {
    const paths = await rpc('pick', {folders});
    if (!paths || !paths.length) return;
    await edit({setRoots: true, paths: [...(c._h.roots || []), ...paths]}, 'Added. Members will see the new files shortly');
  });
  e('addfiles').onclick = () => addRoots(false);
  e('addfolder').onclick = () => addRoots(true);
  e('q').oninput = () => drawHostFiles(c);
  e('sort').onchange = ev => { setSort(ev.target.value); drawHostFiles(c); };
  e('restoreall').onclick = ev => busyBtn(ev.currentTarget, () => edit({restoreAll: true}, 'Removed files are back in the network'));
}

async function loadHostFiles(c) {
  try { c._files = await rpc('hostFiles', {id: c.dataset.id}); } catch { c._files = []; }
  drawHostFiles(c);
}

function drawHostFiles(c) {
  const e = k => $(`[data-e="${k}"]`, c);
  const h = c._h, files = c._files || [];
  // folder/file chips
  const roots = h.roots || [];
  const rk = roots.join('|');
  if (e('roots').dataset.k !== rk) {
    e('roots').dataset.k = rk;
    e('roots').innerHTML = roots.map((p, i) => `<span class="chip" title="${esc(p)}"><span>${esc(p)}</span><button data-ri="${i}" aria-label="Remove from network" title="Remove from network">✕</button></span>`).join('');
    $$('[data-ri]', e('roots')).forEach(b => b.onclick = () => {
      if (roots.length === 1) { toast('A network needs at least one file or folder. Add another first, or delete the network', true); return; }
      const p = roots[+b.dataset.ri];
      if (!confirm(`Remove this from the network?\n\n${p}\n\nThe files stay on your PC. Members can no longer download them.`)) return;
      busyBtn(b, async () => { await rpc('editHostNet', {id: c.dataset.id, setRoots: true, paths: roots.filter(x => x !== p)}); toast('Removed from the network'); c._ek = ''; await refresh(); });
    });
  }
  const removed = files.filter(f => f.removed).length;
  e('restoreall').hidden = !removed;
  e('restoreall').textContent = `Restore removed (${removed})`;
  const q = e('q').value.trim().toLowerCase();
  const shown = sortFiles(files.filter(f => !q || f.rel.toLowerCase().includes(q)));
  const list = e('files');
  if (!files.length) { list.innerHTML = '<div class="empty">No files found in these folders</div>'; return; }
  if (!shown.length) { list.innerHTML = '<div class="empty">No matching files</div>'; return; }
  const top = shown.slice(0, 1500);
  list.innerHTML = top.map((x, i) => {
    const parts = x.rel.split('/'); const nm = parts.pop();
    return `<div class="fb-row he${x.removed ? ' removed' : ''}">
      <span class="nm" title="${esc(x.rel)}">${parts.length ? `<span class="dir">${esc(parts.join('/'))}/</span>` : ''}${esc(nm)}</span>
      <span class="dt">${x.removed ? 'Removed' : fmtDate(x.mtime)}</span><span class="sz">${fmtBytes(x.size)}</span>
      ${x.removed ? `<button class="btn small" data-fi="${i}" data-op="restore">Restore</button>` : `<button class="btn icon x" data-fi="${i}" data-op="remove" title="Remove from network" aria-label="Remove from network">✕</button>`}
    </div>`;
  }).join('') + (shown.length > top.length ? `<div class="empty">${shown.length - top.length} more. Use search to narrow down.</div>` : '');
  $$('[data-fi]', list).forEach(b => b.onclick = () => {
    const x = top[+b.dataset.fi], op = b.dataset.op;
    busyBtn(b, async () => {
      await rpc('editHostNet', {id: c.dataset.id, [op]: [x.rel]});
      x.removed = op === 'remove';
      toast(op === 'remove' ? 'Removed from the network. The file stays on your PC' : 'Back in the network');
      drawHostFiles(c);
      c._ek = ''; refresh();
    });
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
let RC = null;            // opened manifest
const rcSel = new Set();  // selected file ids
const KIND_ICON = {image: '🖼', video: '🎬', audio: '🎵', pdf: '📕', text: '📄', archive: '🗜', doc: '📝', app: '⚙', file: '📄'};

function rcRender() {
  if (!RC) { $('#rc-pick').hidden = true; return; }
  $('#rc-pick').hidden = false;
  $('#rc-help').hidden = true;
  $('#rc-name').textContent = RC.name;
  $('#rc-sum').textContent = RC.files.length.toLocaleString() + (RC.files.length === 1 ? ' file · ' : ' files · ') + fmtBytes(RC.total);
  const q = $('#rc-filter').value.trim().toLowerCase();
  const shown = sortFiles(RC.files.filter(f => !q || f.rel.toLowerCase().includes(q))).slice(0, 3000);
  $('#rc-files').innerHTML = shown.map(f => `<label class="rc-file"><input type="checkbox" data-id="${f.id}" ${rcSel.has(f.id) ? 'checked' : ''}><span class="ic">${KIND_ICON[f.kind] || '📄'}</span><span class="nm" title="${esc(f.rel)}">${esc(f.rel)}</span><span class="dt tnum">${fmtDate(f.mtime)}</span><span class="sz tnum">${fmtBytes(f.size)}</span></label>`).join('')
    + (shown.length === 3000 ? '<div class="muted">Showing the first 3,000. Use the filter to find others.</div>' : '');
  $$('#rc-files input').forEach(cb => cb.onchange = () => { cb.checked ? rcSel.add(+cb.dataset.id) : rcSel.delete(+cb.dataset.id); rcSum(); });
  rcSum();
}
function rcSum() {
  let n = 0, b = 0;
  for (const f of RC.files) if (rcSel.has(f.id)) { n++; b += f.size; }
  $('#rc-selsum').textContent = n ? `${n.toLocaleString()} selected · ${fmtBytes(b)}` : 'Nothing selected';
  $('#rc-all').checked = n === RC.files.length;
  ['#rc-dl', '#rc-idm', '#rc-browser'].forEach(s => $(s).disabled = !n);
}
$('#rc-all').onchange = e => { rcSel.clear(); if (e.target.checked) RC.files.forEach(f => rcSel.add(f.id)); rcRender(); };
$('#rc-filter').oninput = () => rcRender();
const rcIds = () => [...rcSel].map(String);
$('#rc-dl').onclick = e => busyBtn(e.currentTarget, async () => {
  await rpc('recvStart', {code: RC.token, fileIds: rcIds()});
  toast('Downloading into ' + S.config.downloadDir);
  RC = null; rcRender(); await refresh();
});
$('#rc-idm').onclick = e => busyBtn(e.currentTarget, async () => {
  const n = await rpc('recvIDM', {code: RC.token, fileIds: rcIds()});
  toast(`Sent ${n} file${n === 1 ? '' : 's'} to IDM`);
});
$('#rc-browser').onclick = e => busyBtn(e.currentTarget, async () => {
  await rpc('recvBrowser', {code: RC.token, fileIds: rcIds()});
  toast(rcSel.size === 1 ? 'Download started in your browser' : 'Opened in your browser. Pick files there, or use Download all');
});

function renderReceives(list) {
  const box = $('#rc-list');
  if (!RC) $('#rc-help').hidden = list.length > 0;
  const ids = new Set(list.map(t => t.id));
  $$('.rc-card', box).forEach(c => { if (!ids.has(c.dataset.id)) c.remove(); });
  list.forEach((t, idx) => {
    let c = $(`.rc-card[data-id="${t.id}"]`, box);
    if (!c) {
      c = document.createElement('div');
      c.className = 'card rc-card';
      c.dataset.id = t.id;
      c.innerHTML = `<div class="share-title"><h3></h3><span class="pill" data-f="state"></span></div>
        <div class="bar"><i></i></div>
        <div class="row between wrap gap">
          <span class="stats"><span><b data-f="got"></b> of <b data-f="total"></b></span><span>Files <b data-f="files"></b></span><span>Speed <b class="stat-speed" data-f="speed"></b></span><span data-f="eta"></span></span>
          <span class="row gap wrap">
            <button class="btn small" data-act="toggle"></button>
            <button class="btn small" data-act="dir">Open folder</button>
            <button class="btn small danger" data-act="del"></button>
          </span>
        </div>
        <div class="warn" data-f="err" hidden></div>`;
      $('[data-act=toggle]', c).onclick = e => busyBtn(e.currentTarget, async () => { await rpc(c._state === 'downloading' ? 'recvPause' : 'recvResume', {id: t.id}); await refresh(); });
      $('[data-act=dir]', c).onclick = () => rpc('recvOpenDir', {id: t.id});
      $('[data-act=del]', c).onclick = e => {
        const done = c._state === 'done';
        if (!done && !confirm('Cancel this download and delete the unfinished parts?')) return;
        busyBtn(e.currentTarget, async () => { await rpc('recvRemove', {id: t.id, on: !done}); await refresh(); });
      };
      box.insertBefore(c, box.children[idx] || null);
    }
    c._state = t.state;
    const f = k => $(`[data-f="${k}"]`, c);
    $('h3', c).textContent = t.name;
    const st = {downloading: ['Downloading', 'ok'], paused: ['Paused', ''], done: ['Done ✓', 'ok'], error: ['Stopped', 'bad']}[t.state] || [t.state, ''];
    f('state').textContent = st[0]; f('state').className = 'pill ' + st[1];
    $('.bar i', c).style.width = (t.total ? Math.min(100, t.got / t.total * 100) : 100).toFixed(1) + '%';
    f('got').textContent = fmtBytes(t.got); f('total').textContent = fmtBytes(t.total);
    f('files').textContent = `${t.doneCount}/${t.count}`;
    f('speed').textContent = fmtSpeed(t.state === 'downloading' ? t.speed : 0);
    const left = t.total - t.got;
    f('eta').textContent = t.state === 'downloading' && t.speed > 1024 && left > 0 ? 'About ' + fmtETA(left / t.speed) + ' left' : '';
    $('[data-act=toggle]', c).hidden = t.state === 'done';
    $('[data-act=toggle]', c).textContent = t.state === 'downloading' ? 'Pause' : 'Resume';
    $('[data-act=del]', c).textContent = t.state === 'done' ? 'Remove from list' : 'Cancel';
    f('err').hidden = !t.err; f('err').textContent = t.err || '';
  });
}
function fmtETA(s) {
  s = Math.round(s);
  if (s < 60) return s + ' s';
  if (s < 3600) return Math.round(s / 60) + ' min';
  return Math.floor(s / 3600) + ' h ' + Math.round(s % 3600 / 60) + ' min';
}

$('#rc-go').onclick = e => busyBtn(e.currentTarget, async () => {
  const v = $('#rc-in').value.trim();
  if (!v) { toast('Paste a link or a code first', true); return; }
  if (/^https?:\/\//i.test(v)) {
    const r = await rpc('recvOpen', {url: v});
    RC = r.manifest;
    rcSel.clear(); RC.files.forEach(f => rcSel.add(f.id));
    $('#rc-filter').value = '';
    $('#rc-idm').hidden = !r.idm;
    rcRender();
    await refresh();
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

// ------------------------------------------------------------- bookmarks
const BM_SVG = '<svg viewBox="0 0 24 24"><path d="M6 3h12v18l-6-4.5L6 21z"/></svg>';
async function setBookmark(code, on) {
  await rpc('bookmark', {code, on});
  toast(on ? 'Bookmarked. It stays here, ready to connect' : 'Bookmark removed');
  await refresh();
}
function inputBookmarked() {
  const v = $('#jn-code').value.trim();
  return !!v && (S.bookmarks || []).some(b => b.code === v);
}
function syncBmButton() {
  const on = S ? inputBookmarked() : false;
  $('#jn-bm').classList.toggle('on', on);
  $('#jn-bm').title = on ? 'Remove bookmark' : 'Bookmark this code';
}
$('#jn-code').addEventListener('input', syncBmButton);
$('#jn-bm').onclick = e => {
  const v = $('#jn-code').value.trim();
  if (!v) { toast('Paste a code first, then bookmark it', true); return; }
  busyBtn(e.currentTarget, async () => { await setBookmark(v, !inputBookmarked()); syncBmButton(); });
};
function renderBookmarks(list) {
  $('#bm-card').hidden = !list.length;
  const key = JSON.stringify(list);
  const box = $('#bm-list');
  if (box.dataset.k === key) return;
  box.dataset.k = key;
  box.innerHTML = list.map((b, i) => `<div class="bm-row">
      <button class="btn icon bm on" data-i="${i}" data-act="unbm" title="Remove bookmark" aria-label="Remove bookmark">${BM_SVG}</button>
      <div class="bm-name"><b>${esc(b.name)}</b><span class="muted">${esc(b.code.slice(0, 22))}…</span></div>
      ${b.joined ? '<span class="pill ok">Joined</span>' : `<button class="btn small primary" data-i="${i}" data-act="connect">Connect</button>`}
    </div>`).join('');
  $$('[data-act=unbm]', box).forEach(btn => btn.onclick = () => setBookmark(list[+btn.dataset.i].code, false));
  $$('[data-act=connect]', box).forEach(btn => btn.onclick = e => busyBtn(e.currentTarget, async () => {
    await rpc('join', {code: list[+btn.dataset.i].code});
    toast('Connecting to ' + list[+btn.dataset.i].name + '…');
    await refresh();
  }));
}

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
      c.innerHTML = `<div class="share-title"><div class="row gap"><button class="btn icon bm" data-act="bm" title="Bookmark" aria-label="Bookmark">${BM_SVG}</button><h3></h3></div><div class="row gap"><span class="pill" data-f="status"></span><button class="btn small" data-act="reconnect">Reconnect</button><button class="btn small danger" data-act="leave">Leave</button></div></div>
        <div class="stats" style="margin:8px 0 12px"><span>Other members <b data-f="peers"></b></span><span>You uploaded <b data-f="up"></b></span></div>
        <label class="check" style="margin-bottom:12px"><input type="checkbox" data-act="seed"><span></span>Share what I download with other members (faster for everyone)</label>
        <div class="warn" data-f="err" hidden></div>
        <div class="fb">
          <div class="fb-head">
            <input type="search" placeholder="Search files" data-act="search">
            <select class="sort-sel" data-act="sort" title="Sort files">${sortOptions()}</select>
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
      $('[data-act=bm]', c).onclick = e => busyBtn(e.currentTarget, async () => { await setBookmark(c._j.code, !c._j.bookmarked); });
      $('[data-act=seed]', c).onchange = e => rpc('setSeed', {id: j.id, on: e.target.checked});
      $('[data-act=search]', c).oninput = e => { jfilter[j.id] = e.target.value.toLowerCase(); c._fk = ''; renderJoined(S.joined); };
      $('[data-act=sort]', c).value = sortMode;
      $('[data-act=sort]', c).onchange = e => { setSort(e.target.value); $$('.jn-card').forEach(x => x._fk = ''); renderJoined(S.joined); };
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
    $('[data-act=bm]', c).classList.toggle('on', !!j.bookmarked);
    $('[data-act=bm]', c).title = j.bookmarked ? 'Remove bookmark' : 'Bookmark this network';
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
    const fk = sortMode + ':' + (j.files || []).length + ':' + files.map(x => x.id).join(',') + ':' + [...jsel[j.id]].join(',') + ':' + Object.keys(j.have || {}).join(',');
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
            <span class="tag">${j.have && j.have[x.id] ? 'SAVED' : ''}</span><span class="dt">${fmtDate(x.mtime)}</span><span class="sz">${fmtBytes(x.size)}</span></label>`;
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
          el.innerHTML = `<span class="nm"></span><span class="meta"></span><div class="bar"><i></i></div>
            <div class="row gap task-acts" style="grid-column:1/-1">
              <button class="btn small" data-act="toggle"></button>
              <button class="btn small" data-act="show">Show in folder</button>
              <button class="btn icon x" data-act="rm" title="Delete download" aria-label="Delete download">✕</button>
            </div>`;
          const fid = t.file.id;
          $('[data-act=toggle]', el).onclick = ev => busyBtn(ev.currentTarget, async () => {
            await rpc(el._running ? 'pauseDl' : 'download', {id: j.id, fileIds: [fid]});
            await refresh();
          });
          $('[data-act=show]', el).onclick = () => rpc('openPath', {path: el._path});
          $('[data-act=rm]', el).onclick = ev => {
            const done = el._state === 'done';
            if (!done && !confirm('Delete this download?\n\nThe unfinished file is deleted from your PC.')) return;
            busyBtn(ev.currentTarget, async () => { await rpc('removeDl', {id: j.id, fileIds: [fid]}); toast(done ? 'Removed from the list. The file stays in your folder' : 'Download deleted'); await refresh(); });
          };
          tb.append(el);
        }
        $('.nm', el).textContent = t.file.rel;
        $('.nm', el).title = t.file.rel;
        const pct = t.state === 'done' ? 100 : t.file.size ? Math.min(100, t.done / t.file.size * 100) : 0;
        const label = {queued: 'Queued', preparing: 'Preparing…', downloading: `${pct.toFixed(1)}% · ${fmtSpeed(t.speed)} · ${t.sources} source${t.sources > 1 ? 's' : ''}`, paused: `Paused · ${pct.toFixed(1)}%`, done: 'Done · ' + fmtBytes(t.file.size), error: 'Failed', cancelled: 'Cancelled'}[t.state] || t.state;
        $('.meta', el).textContent = label;
        $('.bar', el).classList.toggle('indet', t.state === 'preparing' || t.state === 'queued');
        $('.bar i', el).style.width = pct + '%';
        const running = t.state === 'downloading' || t.state === 'preparing' || t.state === 'queued';
        el._running = running; el._state = t.state; el._path = t.path;
        const tg = $('[data-act=toggle]', el);
        tg.hidden = t.state === 'done';
        const tl = running ? 'Pause' : t.state === 'paused' ? 'Resume' : 'Retry';
        if (tg.textContent !== tl && !tg.disabled) tg.textContent = tl;
        $('[data-act=show]', el).hidden = t.state !== 'done';
        $('[data-act=rm]', el).title = t.state === 'done' ? 'Remove from list (the file stays)' : 'Delete download';
        el.title = t.err || '';
      });
    }
  });
}

function visibleFiles(j) {
  const q = jfilter[j.id] || '';
  return sortFiles((j.files || []).filter(f => !q || f.rel.toLowerCase().includes(q)));
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
    renderReceives(S.receives || []);
    renderBookmarks(S.bookmarks || []);
    syncBmButton();
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

// Deep links into the UI, e.g. #page=network&tab=join or #page=receive&open=<link>
// (used for documentation screenshots).
(function () {
  const p = new URLSearchParams(location.hash.slice(1));
  if (p.get('page')) { const b = $(`[data-page="${p.get('page')}"]`); if (b) b.click(); }
  if (p.get('tab')) { const b = $(`[data-tab="${p.get('tab')}"]`); if (b) b.click(); }
  if (p.get('open')) { $('#rc-in').value = p.get('open'); setTimeout(() => $('#rc-go').click(), 300); }
})();
