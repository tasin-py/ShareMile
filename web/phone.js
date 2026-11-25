'use strict';
// Send files from a phone (or any browser) to the PC that showed the QR code.
// Each file goes up in chunks; after a dropped connection or a reload the PC
// says how much it already has and the upload carries on from there.
const $ = s => document.querySelector(s);
const base = location.pathname.replace(/\/?$/, '/');
let CHUNK = 16 << 20;
const PARALLEL = 3;
const items = [];
let wake = null;

const fmt = b => {
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return (i ? b.toFixed(b < 10 ? 1 : 0) : b) + ' ' + u[i];
};
const esc = s => String(s).replace(/[&<>"]/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;'}[c]));
const sleep = ms => new Promise(r => setTimeout(r, ms));

fetch(base + 'api').then(r => r.json()).then(d => {
  if (d.pc) $('#pc').textContent = d.pc;
  if (d.chunk) CHUNK = d.chunk;
  $('#ver').textContent = d.version ? 'v' + d.version : '';
}).catch(() => {});

// The same file picked again (or after a reload) gets the same id, so it resumes.
function fileId(f) {
  const s = f.name + '|' + f.size + '|' + f.lastModified;
  let h1 = 0x811c9dc5, h2 = 0x01000193;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    h1 = Math.imul(h1 ^ c, 16777619) >>> 0;
    h2 = Math.imul(h2 + c, 2246822519) >>> 0;
  }
  return 'f' + h1.toString(16).padStart(8, '0') + h2.toString(16).padStart(8, '0');
}

function add(files) {
  for (const f of files) {
    const id = fileId(f);
    if (items.some(x => x.id === id && x.state !== 'err')) continue; // already queued or sent
    const li = document.createElement('li');
    li.className = 'wait';
    li.innerHTML = `<div class="nm"><span>${esc(f.name)}</span><span class="st">Waiting</span></div><div class="bar"><i></i></div>`;
    $('#list').prepend(li);
    items.push({f, id, li, have: 0, state: 'wait'});
  }
  pump();
}

function paint(it, st) {
  it.li.className = it.state;
  it.li.querySelector('.st').textContent = st;
  it.li.querySelector('.bar i').style.width = (it.f.size ? it.have / it.f.size * 100 : 100) + '%';
}

let t0 = 0, sent0 = 0;
function summary() {
  const total = items.reduce((a, x) => a + x.f.size, 0);
  const have = items.reduce((a, x) => a + Math.min(x.have, x.f.size), 0);
  const done = items.filter(x => x.state === 'done').length;
  const errs = items.filter(x => x.state === 'err').length;
  const busy = items.some(x => x.state === 'up' || x.state === 'wait' || x.state === 'retry');
  $('#sum').hidden = !items.length;
  $('#sum-bar').style.width = (total ? have / total * 100 : 100) + '%';
  $('#sum-txt').textContent = busy
    ? `Sending ${done} of ${items.length} · ${fmt(have)} of ${fmt(total)}`
    : errs ? `${done} sent, ${errs} failed` : `Done · ${items.length} file${items.length > 1 ? 's' : ''} sent (${fmt(total)})`;
  const sec = (Date.now() - t0) / 1000;
  $('#sum-speed').textContent = busy && sec > 1 ? fmt((have - sent0) / sec) + '/s' : '';
  $('#hint').hidden = !busy;
  if (busy && !wake && navigator.wakeLock) navigator.wakeLock.request('screen').then(w => { wake = w; w.onrelease = () => { wake = null; }; }).catch(() => {});
  if (!busy && wake) { wake.release(); wake = null; }
}

function pump() {
  if (!items.some(x => x.state === 'up' || x.state === 'retry')) {
    t0 = Date.now();
    sent0 = items.reduce((a, x) => a + (x.state === 'done' ? x.f.size : 0), 0);
  }
  while (items.filter(x => x.state === 'up' || x.state === 'retry').length < PARALLEL) {
    const it = items.find(x => x.state === 'wait');
    if (!it) break;
    send(it);
  }
  summary();
}

function q(it, extra) {
  return base + 'up?u=' + it.id + '&s=' + it.f.size + (extra || '');
}

async function askHave(it) {
  const r = await fetch(q(it), {cache: 'no-store'});
  if (!r.ok) throw new Error('net');
  return (await r.json()).have || 0;
}

// One chunk with upload progress. Resolves to the server's answer.
function chunk(it, off) {
  return new Promise((res, rej) => {
    const end = Math.min(off + CHUNK, it.f.size);
    const x = new XMLHttpRequest();
    x.open('POST', q(it, '&o=' + off + '&n=' + encodeURIComponent(it.f.name) + '&m=' + (it.f.lastModified || 0)));
    x.setRequestHeader('Content-Type', 'application/octet-stream');
    x.upload.onprogress = e => { it.have = off + e.loaded; paint(it, pct(it)); summary(); };
    x.onload = () => {
      let d = {};
      try { d = JSON.parse(x.responseText); } catch (e) {}
      if (x.status === 200 || x.status === 409) res(d);
      else if (x.status >= 500 && x.status !== 507) rej(new Error('net'));
      else rej(Object.assign(new Error(d.error || 'The PC refused the file'), {fatal: true}));
    };
    x.onerror = x.ontimeout = () => rej(new Error('net'));
    x.send(it.f.slice(off, end));
  });
}

const pct = it => (it.f.size ? Math.floor(it.have / it.f.size * 100) : 100) + '%';

async function send(it) {
  it.state = 'up';
  paint(it, '0%');
  let tries = 0;
  for (;;) {
    try {
      let off = await askHave(it);
      if (off > it.f.size) off = 0;
      for (;;) {
        it.have = off;
        paint(it, pct(it));
        const d = await chunk(it, off);
        tries = 0;
        if (d.done) {
          it.have = it.f.size;
          it.state = 'done';
          paint(it, 'Done');
          if (d.name && d.name !== it.f.name) it.li.querySelector('.nm span').textContent = d.name;
          pump();
          return;
        }
        off = d.have || 0;
        it.state = 'up';
      }
    } catch (e) {
      if (e.fatal) {
        it.state = 'err';
        paint(it, 'Failed');
        const m = document.createElement('div');
        m.className = 'msg';
        m.textContent = e.message;
        const b = document.createElement('button');
        b.className = 'btn';
        b.textContent = 'Try again';
        b.onclick = () => { m.remove(); b.remove(); it.state = 'wait'; paint(it, 'Waiting'); pump(); };
        it.li.append(m, b);
        pump();
        return;
      }
      // Connection lost: wait and carry on from what the PC has.
      it.state = 'retry';
      const wait = Math.min(30, 2 ** Math.min(tries++, 5));
      paint(it, navigator.onLine === false ? 'Offline, waiting…' : `Reconnecting in ${wait}s…`);
      summary();
      await sleep(wait * 1000);
      it.state = 'up';
    }
  }
}

$('#pick').onchange = e => { add([...e.target.files]); e.target.value = ''; };
const drop = $('#drop');
['dragenter', 'dragover'].forEach(ev => drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.add('over'); }));
['dragleave', 'drop'].forEach(ev => drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.remove('over'); }));
drop.addEventListener('drop', e => add([...e.dataTransfer.files]));
window.addEventListener('beforeunload', e => {
  if (items.some(x => x.state === 'up' || x.state === 'retry' || x.state === 'wait')) { e.preventDefault(); e.returnValue = ''; }
});
document.addEventListener('visibilitychange', () => { if (!document.hidden) summary(); });
