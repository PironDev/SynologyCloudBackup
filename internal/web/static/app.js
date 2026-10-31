(() => {
'use strict';
const $app = document.getElementById('app');
let S = null;               // состояние от /api/state
let view = 'overview';
let accounts = [], jobs = [];
let runFilter = '', selRun = null, logOffset = 0, logRunning = false;

const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const $ = (sel, root = document) => root.querySelector(sel);
const fmtDate = s => s ? new Date(s).toLocaleString('ru-RU', {dateStyle:'short', timeStyle:'short'}) : '—';
const fmtBytes = n => { if (!n) return '0 Б'; const u = ['Б','КБ','МБ','ГБ','ТБ']; let i = 0; while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; } return (i ? n.toFixed(1) : n) + ' ' + u[i]; };
const PRESETS = [
  {label:'Только вручную', cron:''},
  {label:'Каждую ночь в 03:00', cron:'0 3 * * *'},
  {label:'Каждый день в 12:00', cron:'0 12 * * *'},
  {label:'Каждые 6 часов', cron:'0 */6 * * *'},
  {label:'Раз в неделю (воскресенье, 03:00)', cron:'0 3 * * 0'},
  {label:'Раз в месяц (1-го числа, 03:00)', cron:'0 3 1 * *'},
];

// ---------- иконки (линейные, как в Ant Design) ----------
const svg = d => `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${d}</svg>`;
const IC = {
  info: svg('<circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/>'),
  success: svg('<circle cx="12" cy="12" r="10"/><path d="M8 12.5l3 3 5-6"/>'),
  error: svg('<circle cx="12" cy="12" r="10"/><path d="M15 9l-6 6M9 9l6 6"/>'),
  warning: svg('<path d="M10.3 3.9L1.8 18a2 2 0 001.7 3h17a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>'),
  plus: svg('<path d="M12 5v14M5 12h14"/>'),
  play: svg('<polygon points="7 4 20 12 7 20 7 4"/>'),
  stop: svg('<rect x="6" y="6" width="12" height="12" rx="2"/>'),
  edit: svg('<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 013 3L7 19l-4 1 1-4 12.5-12.5z"/>'),
  trash: svg('<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/>'),
  folder: svg('<path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2V7z"/>'),
  up: svg('<path d="M12 19V5M5 12l7-7 7 7"/>'),
  log: svg('<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>'),
  help: svg('<circle cx="12" cy="12" r="10"/><path d="M9.1 9a3 3 0 015.8 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/>'),
  inbox: svg('<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.5 5.1L2 12v6a2 2 0 002 2h16a2 2 0 002-2v-6l-3.5-6.9A2 2 0 0016.8 4H7.2a2 2 0 00-1.7 1.1z"/>'),
  cloud: svg('<path d="M18 10h-1.3A7 7 0 104 14.9"/><path d="M12 12v8M9 15l3-3 3 3"/>'),
  check: svg('<path d="M5 12.5l4.5 4.5L19 7.5"/>'),
  eye: svg('<path d="M1 12s4-7 11-7 11 7 11 7-4 7-11 7S1 12 1 12z"/><circle cx="12" cy="12" r="3"/>'),
  refresh: svg('<path d="M21 12a9 9 0 11-3-6.7L21 8"/><path d="M21 3v5h-5"/>'),
};

// ---------- компоненты ----------
const STATUS = {success:['Успешно','ok'], partial:['Частично','warn'], failed:['Ошибка','err'], cancelled:['Остановлено','muted'], running:['Выполняется','run']};
const tag = (st) => { const [t, c] = STATUS[st] || [st, 'muted']; return `<span class="tag ${c}"><i class="dot"></i>${esc(t)}</span>`; };
const tip = text => `<span class="tip" tabindex="0" data-tip="${esc(text)}">${IC.help}</span>`;
const alertBox = (type, text, desc = '') => `<div class="alert ${type}">${IC[type] || IC.info}<div><div class="am">${esc(text)}</div>${desc ? `<div class="ad">${desc}</div>` : ''}</div></div>`;
const empty = (text, action = '') => `<div class="empty">${IC.inbox}<div class="et">${esc(text)}</div>${action}</div>`;
const label = (text, tipText, id) => `<label${id ? ` for="${id}"` : ''}>${esc(text)}${tipText ? tip(tipText) : ''}</label>`;
const linkify = text => esc(text).replace(/https?:\/\/[^\s«»]+/g, u => {
  let tail = ''; while (/[.,;)]$/.test(u)) { tail = u.slice(-1) + tail; u = u.slice(0, -1); }
  return `<a href="${u}" target="_blank" rel="noopener noreferrer">${u}</a>${tail}`;
});
const timeline = steps => `<ol class="timeline">${steps.map((s, i) => `<li><span class="tl-dot">${i + 1}</span><div class="tl-body"><div class="tl-title">${esc(s.title)}</div><div class="tl-text">${linkify(s.text)}</div></div></li>`).join('')}</ol>`;
const switchBox = (id, on, text) => `<label class="switch"><input type="checkbox" id="${id}" ${on ? 'checked' : ''}><span class="track"></span><span class="st">${esc(text)}</span></label>`;
const segmented = (id, items, val, act) => `<div class="segmented" data-seg="${id}">${items.map(([v, t]) => `<button type="button" data-act="${act}" data-val="${v}" class="${v === val ? 'on' : ''}">${esc(t)}</button>`).join('')}</div>`;
const spinner = '<div class="center"><span class="spin"></span></div>';
const pageHead = (title, sub, extra = '') => `<div class="bar"><div class="bt"><h2>${esc(title)}</h2>${sub ? `<div class="sub">${esc(sub)}</div>` : ''}</div><div class="extra">${extra}</div></div>`;

// Message (всплывающие уведомления)
function toast(text, type = 'error') {
  let box = $('#toasts'); if (!box) { box = document.createElement('div'); box.id = 'toasts'; document.body.appendChild(box); }
  const t = document.createElement('div'); t.className = 'toast ' + type; t.innerHTML = (IC[type] || IC.info) + `<span>${esc(text)}</span>`;
  box.appendChild(t); setTimeout(() => { t.classList.add('out'); setTimeout(() => t.remove(), 300); }, type === 'error' ? 6000 : 3200);
}

// Modal.confirm
function askConfirm(title, text, okText = 'Удалить', danger = true) {
  return new Promise(resolve => {
    const o = modal(`<div class="confirm">${IC.warning}<div><h3>${esc(title)}</h3><p class="muted">${esc(text)}</p></div></div>
      <div class="foot"><button class="btn" data-act="cfno">Отмена</button><button class="btn primary ${danger ? 'danger' : ''}" data-act="cfyes">${esc(okText)}</button></div>`, 'narrow');
    o._confirm = resolve;
  });
}

// Tooltip: один элемент на странице, не обрезается контейнерами
(() => {
  let el = null, cur = null;
  const hide = () => { if (el) el.classList.remove('show'); cur = null; };
  const show = t => {
    if (!t.dataset.tip) return;
    if (!el) { el = document.createElement('div'); el.id = 'tooltip'; el.setAttribute('role', 'tooltip'); document.body.appendChild(el); }
    cur = t; el.textContent = t.dataset.tip; el.className = 'show';
    const r = t.getBoundingClientRect(), w = el.offsetWidth, h = el.offsetHeight;
    let x = Math.min(Math.max(8, r.left + r.width / 2 - w / 2), innerWidth - w - 8), y = r.top - h - 10, below = false;
    if (y < 8) { y = r.bottom + 10; below = true; }
    el.style.left = x + 'px'; el.style.top = y + 'px'; el.classList.toggle('below', below);
    el.style.setProperty('--ax', Math.min(Math.max(14, r.left + r.width / 2 - x), w - 14) + 'px');
  };
  document.addEventListener('mouseover', e => { const t = e.target.closest && e.target.closest('[data-tip]'); if (t) show(t); else if (cur) hide(); });
  document.addEventListener('focusin', e => { const t = e.target.closest && e.target.closest('[data-tip]'); if (t) show(t); });
  document.addEventListener('focusout', hide);
  document.addEventListener('scroll', hide, true);
  document.addEventListener('click', e => { const t = e.target.closest && e.target.closest('.tip'); if (t) { cur === t ? hide() : show(t); } }, true);
})();

// ---------- тема и «живые» эффекты ----------
let theme = 'auto';
try { theme = localStorage.getItem('cb-theme') || 'auto'; } catch (e) {}
function applyTheme() {
  if (theme === 'auto') document.documentElement.removeAttribute('data-theme');
  else document.documentElement.dataset.theme = theme;
}
applyTheme();

(() => { // параллакс фона, блик на стекле и «магнитные» кнопки
  if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
  const root = document.documentElement; let raf = 0, ev = null;
  addEventListener('pointermove', e => {
    ev = e; if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0; const e = ev;
      root.style.setProperty('--px', (e.clientX / innerWidth - .5).toFixed(3));
      root.style.setProperty('--py', (e.clientY / innerHeight - .5).toFixed(3));
      const card = e.target.closest && e.target.closest('.card, .modal');
      if (card) { const r = card.getBoundingClientRect(); card.style.setProperty('--mx', (e.clientX - r.left) + 'px'); card.style.setProperty('--my', (e.clientY - r.top) + 'px'); }
      document.querySelectorAll('.btn.primary').forEach(b => {
        const r = b.getBoundingClientRect(), dx = e.clientX - (r.left + r.width / 2), dy = e.clientY - (r.top + r.height / 2);
        const near = Math.hypot(dx, dy) < Math.max(r.width, 70) * .8;
        b.style.setProperty('--tx', near ? (dx * .14).toFixed(1) + 'px' : '0px');
        b.style.setProperty('--ty', near ? (dy * .2).toFixed(1) + 'px' : '0px');
      });
    });
  }, {passive: true});
})();

// Внутри DSM страница открыта через CGI-мост (index.cgi?r=<путь>): адрес API строится от <meta api-base>.
const BASE = (document.querySelector('meta[name="api-base"]') || {}).content || '';
let SYNO_TOKEN = new URLSearchParams(location.search).get('SynoToken') || '';
// DSM 7 требует токен CSRF для проверки сессии; берём его у самой DSM (тот же адрес, cookie сессии).
async function ensureToken() {
  if (SYNO_TOKEN || !BASE) return;
  try {
    const r = await fetch('/webman/login.cgi', {credentials: 'same-origin'});
    const d = await r.json();
    if (d && d.SynoToken) SYNO_TOKEN = d.SynoToken;
  } catch (e) {}
}
const U = p => (BASE ? BASE + encodeURIComponent(p) : p);

async function api(method, url, body) {
  const headers = {'X-Requested-With': 'cloudbackup', 'Content-Type': 'application/json'};
  if (SYNO_TOKEN) headers['X-SYNO-TOKEN'] = SYNO_TOKEN;
  const r = await fetch(U(url), {method, credentials: 'same-origin', headers,
    body: body === undefined ? undefined : JSON.stringify(body)});
  let data = null; try { data = await r.json(); } catch (e) {}
  if (r.status === 401 && S && S.loggedIn && url !== '/api/login') { S.loggedIn = false; closeModals(); render(); }
  if (!r.ok) throw new Error((data && data.error) || ('Ошибка ' + r.status));
  return data;
}

// ---------- каркас ----------
async function loadState() { await ensureToken(); S = await api('GET', '/api/state'); render(); }
// Все провайдеры пакета, поля и выбор описания по виду аккаунта.
const providerOf = kind => S.providers.find(p => p.kind === kind) || S.providers[0];
const allFields = () => S.providers.flatMap(p => p.fields);

const TABS = [['overview', 'Обзор'], ['explorer', 'Проводник'], ['jobs', 'Задания'], ['accounts', 'Аккаунты'], ['runs', 'Журнал'], ['gateway', 'File Station'], ['settings', 'Настройки']];

function render() {
  if (!S) { $app.innerHTML = spinner; return; }
  document.title = S.title;

  if (S.auth === 'dsm' && !S.loggedIn) return renderDenied();
  if (S.needSetup) return renderAuth(true);
  if (!S.loggedIn) return renderAuth(false);
  const who = S.auth === 'dsm'
    ? `<span class="who"><span class="avatar">${esc((S.user || '?').slice(0, 1).toUpperCase())}</span>${esc(S.user)}</span>`
    : '<button class="btn sm" data-act="logout">Выйти</button>';
  $app.innerHTML = `<div class="shell">
    <header><span class="logo"><img src="${U('/logo.svg')}" alt=""></span><h1>${esc(S.title)}</h1><span class="tag muted ver">v${esc(S.version)}</span>
      <span class="grow"></span><button class="btn sm" data-act="tour">Экскурсия</button><button class="btn sm icon" data-act="wizard" data-tip="Мастер знакомства" aria-label="Мастер знакомства">${IC.help}</button>${segmented('theme', [['auto', 'Авто'], ['light', 'День'], ['dark', 'Ночь']], theme, 'settheme')}${who}</header>
    <nav class="tabs" role="tablist">${TABS.map(([k, t]) => `<button role="tab" data-act="nav" data-v="${k}" class="${view === k ? 'active' : ''}">${t}</button>`).join('')}</nav>
    <main id="view">${spinner}</main><footer class="devfoot">Разработчик: <a href="https://pirondev.rbel.info/" target="_blank" rel="noopener noreferrer">pirondev.rbel.info</a></footer></div>`;
  showView(); maybeWizard();
}

function renderDenied() {
  $app.innerHTML = `<div class="result card"><div class="ricon warn">${IC.warning}</div><h1>Нет доступа</h1>
    <p class="muted">Приложение «${esc(S.title)}» доступно администраторам DSM.</p>
    ${alertBox('error', S.reason || 'Нет действительной сессии DSM.')}
    <p class="muted small">Войдите в DSM под учётной записью администратора и откройте приложение заново.</p>
    <p><button class="btn primary" data-act="reload">${IC.refresh} Повторить</button></p></div>`;
}

function renderAuth(setup) {
  $app.innerHTML = `<form class="card login" data-form="${setup ? 'setup' : 'login'}">
    <h1>${esc(S.title)}</h1>
    <p class="muted">${setup ? 'Первый запуск. Придумайте пароль для входа в этот интерфейс (не короче 8 символов).' : 'Введите пароль.'}</p>
    <div class="fi">${label('Пароль', '', 'pw')}<input type="password" id="pw" autofocus autocomplete="${setup ? 'new-password' : 'current-password'}"></div>
    <div id="authmsg"></div>
    <p><button class="btn primary" type="submit">${setup ? 'Задать пароль и войти' : 'Войти'}</button></p></form>`;
}

async function showView() {
  const el = $('#view'); if (!el) return;
  try {
    if (view === 'overview') await viewOverview(el);
    else if (view === 'explorer') await window.CBX.view(el);
    else if (view === 'jobs') await viewJobs(el);
    else if (view === 'accounts') await viewAccounts(el);
    else if (view === 'runs') await viewRuns(el);
    else if (view === 'gateway') await viewGateway(el);
    else await viewSettings(el);
  } catch (e) { el.innerHTML = alertBox('error', e.message); }
}

// ---------- модальные окна ----------
function modal(html, cls = '') {
  const o = document.createElement('div'); o.className = 'overlay';
  o.innerHTML = `<div class="modal ${cls}" role="dialog" aria-modal="true">${html}</div>`;
  document.body.appendChild(o); return o;
}
const settle = o => { if (o._confirm) { o._confirm(false); o._confirm = null; } };
const closeModals = () => document.querySelectorAll('.overlay').forEach(o => { settle(o); o.remove(); });
const closeTop = () => { const all = document.querySelectorAll('.overlay'); if (all.length) { const o = all[all.length - 1]; settle(o); o.remove(); } };
const msg = (el, text, cls = 'error') => { el.innerHTML = text ? alertBox(cls, text) : ''; };

// ---------- задания ----------
function schedText(j) {
  if (!j.schedule) return '<span class="muted">вручную</span>';
  const p = PRESETS.find(p => p.cron === j.schedule);
  return esc(p ? p.label : j.schedule) + (j.next ? `<div class="small muted">след.: ${fmtDate(j.next)}</div>` : '');
}
function lastText(j) {
  if (j.active) return tag('running') + `<div class="small muted">с ${fmtDate(j.active.started)}</div>`;
  const l = j.last; if (!l) return '<span class="muted">не запускалось</span>';
  const s = l.stats || {};
  return tag(l.status) + ` <span class="small muted">${fmtDate(l.started)}</span>` +
    `<div class="small muted">выгружено ${s.uploaded || 0} · пропущено ${s.skipped || 0} · ошибок ${s.errors || 0} · ${fmtBytes(s.bytes)}</div>` +
    (l.message ? `<div class="small err-text">${esc(l.message)}</div>` : '');
}
function jobsTable() {
  if (!jobs.length) return `<div class="card">${empty('Заданий пока нет', accounts.length ? `<button class="btn primary" data-act="newjob">${IC.plus} Создать задание</button>` : '<div class="muted small">Сначала добавьте аккаунт</div>')}</div>`;
  return `<div class="card flush"><div class="tw"><table><thead><tr><th>Задание</th><th>Режим</th><th>Расписание</th><th>Последний запуск</th><th></th></tr></thead><tbody>${jobs.map(j => `
    <tr><td><b>${esc(j.name)}</b>${j.enabled ? '' : ' <span class="tag muted">выключено</span>'}<div class="small muted">${esc(j.accountName)} → ${esc(j.remoteDir)}</div></td>
    <td><span class="tag ${j.mode === 'archive' ? 'info' : 'muted'}">${j.mode === 'archive' ? 'Архив' : 'Зеркало'}</span></td><td>${schedText(j)}</td><td>${lastText(j)}</td>
    <td class="act">${j.active ? `<button class="btn sm danger-o" data-act="cancel" data-id="${j.id}">${IC.stop} Остановить</button>` : `<button class="btn sm primary" data-act="run" data-id="${j.id}">${IC.play} Запустить</button>`}
      <button class="btn sm icon" data-tip="Журнал" aria-label="Журнал" data-act="joblog" data-id="${j.id}">${IC.log}</button>
      <button class="btn sm icon" data-tip="Изменить" aria-label="Изменить" data-act="editjob" data-id="${j.id}">${IC.edit}</button>
      <button class="btn sm icon danger-o" data-tip="Удалить" aria-label="Удалить" data-act="deljob" data-id="${j.id}">${IC.trash}</button></td></tr>`).join('')}</tbody></table></div></div>`;
}
async function refreshJobs() { jobs = await api('GET', '/api/jobs'); const box = $('#jobsbox'); if (box) box.innerHTML = jobsTable(); }
async function viewJobs(el) {
  [accounts, jobs] = await Promise.all([api('GET', '/api/accounts'), api('GET', '/api/jobs')]);
  el.innerHTML = pageHead('Задания', 'Что, куда и когда выгружать',
      `<button class="btn primary" data-act="newjob" ${accounts.length ? '' : 'disabled'}>${IC.plus} Новое задание</button>`) +
    (accounts.length ? '' : alertBox('warning', 'Нет подключённых аккаунтов', 'Откройте вкладку «Аккаунты» и добавьте первый.')) +
    `<div id="jobsbox">${jobsTable()}</div>`;
}

const MODE_TIP = 'Зеркало — выгружаются только новые и изменённые файлы, структура папок сохраняется. Архив — всё упаковывается в tar.gz с датой в имени (можно резать на части и хранить N последних).';
function openJobForm(job) {
  const j = job ? JSON.parse(JSON.stringify(job)) : {name:'', accountId: accounts[0] && accounts[0].id, enabled:true, mode:'mirror', sources:[],
    excludes: (S.defaultExcludes || []).slice(), remoteDir:'/NAS-Backups', schedule:'0 3 * * *', concurrency:4, verify:'hash', archive:{partSizeMB:0, keep:7}};
  const o = modal(`<h3>${job ? 'Изменить задание' : 'Новое задание'}</h3>
    <form data-form="job" data-id="${job ? job.id : ''}">
    <div class="grid2"><div class="fi">${label('Название', '', 'f-name')}<input type="text" id="f-name" value="${esc(j.name)}"></div>
    <div class="fi">${label('Аккаунт', '', 'f-acc')}<select id="f-acc">${accounts.map(a => `<option value="${a.id}" ${a.id === j.accountId ? 'selected' : ''}>${esc(a.name)}</option>`).join('')}</select></div></div>
    <div class="fi">${label('Режим', MODE_TIP)}${segmented('f-mode', [['mirror', 'Зеркало'], ['archive', 'Архив']], j.mode, 'seg')}<input type="hidden" id="f-mode" value="${j.mode}"></div>
    <div class="fi">${label('Что выгружать', 'Папка или файл на NAS. Пользователю пакета нужно выдать право чтения на эти общие папки: Панель управления → Общие папки → Изменить → Разрешения → «Системный внутренний пользователь».')}<div class="srcs" id="srcs"></div>
    <div class="row"><input type="text" id="f-srcin" placeholder="/volume1/photo"><button class="btn" type="button" data-act="addsrc">${IC.plus} Добавить</button><button class="btn" type="button" data-act="browse">${IC.folder} Обзор…</button><button class="btn" type="button" data-act="tree">${IC.folder} Дерево папок…</button></div></div>
    <div class="fi">${label('Папка в облаке', 'Каждая выбранная папка будет создана внутри этой папки.', 'f-remote').replace('<span class="tip"', '<span id="f-remote-tip" class="tip"')}<input type="text" id="f-remote" value="${esc(j.remoteDir)}"></div>
    <div class="fi">${label('Исключения', 'Один шаблон на строку: имя файла или папки, поддерживаются * и ?. По умолчанию пропускаются служебные @eaDir, #recycle, .DS_Store, Thumbs.db.', 'f-excl')}<textarea id="f-excl">${esc((j.excludes || []).join('\n'))}</textarea></div>
    <div class="fi">${label('Расписание', 'Формат cron из 5 полей (минута час день месяц день_недели), время по часовому поясу NAS. Пустое значение — только запуск вручную.')}
      <div class="grid2"><select id="f-preset">${PRESETS.map(p => `<option value="${esc(p.cron)}" ${p.cron === j.schedule ? 'selected' : ''}>${esc(p.label)}</option>`).join('')}
      <option value="__once" ${(j.schedule || '').startsWith('@at ') ? 'selected' : ''}>Один раз в дату…</option>
      <option value="__custom" ${PRESETS.some(p => p.cron === j.schedule) || (j.schedule || '').startsWith('@at ') ? '' : 'selected'}>Своё выражение cron…</option></select>
      <input type="text" id="f-cron" value="${esc(j.schedule)}" placeholder="мин час день месяц день_недели"></div>
      <input type="datetime-local" id="f-once" style="display:${(j.schedule || '').startsWith('@at ') ? 'block' : 'none'}" value="${esc((j.schedule || '').slice(4).replace(' ', 'T'))}"></div>
    <div id="f-mirror" class="fi">${label('Потоков загрузки', 'Сколько файлов загружать одновременно (1–16). Больше потоков — быстрее для множества мелких файлов.', 'f-conc')}<input type="number" id="f-conc" min="1" max="16" value="${j.concurrency || 4}"></div>
    <div id="f-archive" class="grid2"><div class="fi">${label('Размер части, МБ', '0 — одним файлом. Для Яндекс.Диска без подписки Яндекс 360 лимит на файл 1 ГБ — поставьте, например, 900.', 'f-part')}<input type="number" id="f-part" min="0" value="${j.archive ? j.archive.partSizeMB : 0}"></div>
      <div class="fi">${label('Хранить архивов', 'Сколько последних архивов оставлять в облаке. 0 — не удалять старые.', 'f-keep')}<input type="number" id="f-keep" min="0" value="${j.archive ? j.archive.keep : 7}"></div>
      <div class="fi">${label('Способ ротации', '«Удалять самые старые» — сверх лимита уходят самые старые. «Умная ротация» — один снимок за день, неделю и месяц.', 'f-policy')}<select id="f-policy"><option value="oldest" ${j.archive && j.archive.policy === 'smart' ? '' : 'selected'}>Удалять самые старые</option><option value="smart" ${j.archive && j.archive.policy === 'smart' ? 'selected' : ''}>Умная ротация</option></select></div>
      <div class="fi"><label class="chk"><input type="checkbox" id="f-compress" ${j.archive && j.archive.noCompress ? '' : 'checked'}> Сжимать архив (gzip)</label></div></div>
    <div class="fi">${label('Сверка после загрузки', 'Размер файла сверяется всегда. «Хеш» дополнительно сравнивает MD5 с тем, что вернуло облако (у Яндекс.Диска он есть; если облако хеш не отдаёт, остаётся проверка размера). «Полная» для облаков без хеша (Облако Mail.ru, WebDAV) скачивает файл обратно и сверяет MD5: надёжнее, но удваивает трафик. При несовпадении файл загружается заново, затем задание сообщит об ошибке.', 'f-verify')}<select id="f-verify"><option value="size" ${j.verify === 'size' ? 'selected' : ''}>Только размер</option><option value="hash" ${!j.verify || j.verify === 'hash' ? 'selected' : ''}>Размер и хеш MD5 (если отдаёт облако)</option><option value="full" ${j.verify === 'full' ? 'selected' : ''}>Полная: скачать обратно и сравнить</option></select></div>
    <div class="fi">${switchBox('f-en', j.enabled, 'Задание включено (запускается по расписанию)')}</div>
    <div class="fi">${switchBox('f-notify', j.notify, 'Уведомлять в DSM о результате задания')}</div>
    <div id="f-msg"></div>
    <div class="foot"><button class="btn" type="button" data-act="closemodal">Отмена</button><button class="btn primary" type="submit">Сохранить</button></div></form>`);
  o._sources = j.sources.slice();
  drawSources(o); syncJobForm(o);
}
function drawSources(o) {
  $('#srcs', o).innerHTML = o._sources.length
    ? o._sources.map((s, i) => `<div class="src">${IC.folder}<span>${esc(s)}</span><button class="btn sm icon danger-o" type="button" data-tip="Убрать" aria-label="Убрать" data-act="rmsrc" data-i="${i}">${IC.trash}</button></div>`).join('')
    : '<div class="muted small nosrc">Ничего не выбрано</div>';
}
function syncJobForm(o) {
  const arch = $('#f-mode', o).value === 'archive';
  $('#f-archive', o).style.display = arch ? '' : 'none';
  $('#f-mirror', o).style.display = arch ? 'none' : '';
  const t = $('#f-remote-tip', o);
  if (t) t.dataset.tip = arch ? 'Архивы лягут в подпапку с названием задания.' : 'Каждая выбранная папка будет создана внутри этой папки.';
}
const jobFormModal = () => { const all = document.querySelectorAll('.overlay'); for (const o of all) if (o._sources) return o; return null; };

async function saveJob(form) {
  const o = jobFormModal(), id = form.dataset.id;
  const body = {name: $('#f-name', o).value, accountId: $('#f-acc', o).value, enabled: $('#f-en', o).checked, notify: $('#f-notify', o).checked, mode: $('#f-mode', o).value,
    sources: o._sources, excludes: $('#f-excl', o).value.split('\n'), remoteDir: $('#f-remote', o).value, schedule: $('#f-cron', o).value,
    concurrency: +$('#f-conc', o).value || 4, verify: $('#f-verify', o).value, archive: {partSizeMB: +$('#f-part', o).value || 0, keep: +$('#f-keep', o).value || 0, policy: $('#f-policy', o).value, noCompress: !$('#f-compress', o).checked}};
  try { await api(id ? 'PUT' : 'POST', '/api/jobs' + (id ? '/' + id : ''), body); o.remove(); await refreshJobs(); toast('Задание сохранено', 'success'); }
  catch (e) { msg($('#f-msg', o), e.message); }
}

async function openPicker(start) {
  const o = modal(`<h3>Выбор папки на NAS</h3><div id="pk-path" class="muted small pkpath"></div><div class="dirs" id="pk-list">${spinner}</div><div id="pk-msg"></div>
    <div class="foot"><button class="btn" data-act="closetop">Отмена</button><button class="btn primary" data-act="pickhere">Выбрать эту папку</button></div>`, 'narrow');
  o._pick = true; await pickerLoad(o, start || '');
}
async function pickerLoad(o, p) {
  try {
    const d = await api('GET', '/api/fs?path=' + encodeURIComponent(p));
    o._cur = d.path; $('#pk-path', o).textContent = d.path + ' · файлов: ' + d.files;
    $('#pk-list', o).innerHTML = (d.parent ? `<div data-act="pkgo" data-p="${esc(d.parent)}">${IC.up} ..</div>` : '') +
      (d.dirs.map(x => `<div data-act="pkgo" data-p="${esc(x.path)}">${IC.folder} ${esc(x.name)}</div>`).join('') || '<div class="muted nodirs">Подпапок нет</div>');
    msg($('#pk-msg', o), '');
  } catch (e) { $('#pk-list', o).innerHTML = ''; msg($('#pk-msg', o), e.message); }
}


// Дерево папок: отметки хранятся в o._tree.sel; родитель отмечен — дети входят в него целиком.
async function openTree(o) {
  const m = modal(`<h3>Папки для резервного копирования</h3><div class="muted small">Отметьте папку — войдёт вся её структура. Раскройте папку, чтобы выбрать вложенные.</div>
    <div class="dirs tree" id="tr-list">${spinner}</div><div id="tr-msg"></div>
    <div class="foot"><button class="btn" data-act="closetop">Отмена</button><button class="btn primary" data-act="treeok">Готово</button></div>`, 'narrow');
  m._tree = {sel: new Set(o._sources), o};
  await treeLoad(m, '/', $('#tr-list', m), 0);
}
async function treeLoad(m, p, box, depth) {
  try {
    const d = await api('GET', '/api/fs?path=' + encodeURIComponent(p));
    const sel = m._tree.sel;
    box.innerHTML = d.dirs.length ? d.dirs.map(x => `<div class="trow" style="padding-left:${12 + depth * 22}px"><button class="btn sm icon" type="button" data-act="treeexp" data-p="${esc(x.path)}" data-d="${depth}" aria-label="Раскрыть">▸</button><label class="chk"><input type="checkbox" data-tree="${esc(x.path)}" ${sel.has(x.path) ? 'checked' : ''}> ${esc(x.name)}</label></div><div class="tkids" data-kids="${esc(x.path)}"></div>`).join('')
      : '<div class="muted nodirs">Подпапок нет</div>';
    msg($('#tr-msg', m), '');
  } catch (e) { box.innerHTML = ''; msg($('#tr-msg', m), e.message); }
}
function treeToggle(m, path, on) {
  const sel = m._tree.sel;
  if (on) sel.add(path); else sel.delete(path);
}
// Убирает из списка папки, которые уже входят в отмеченного родителя.
function treeTop(paths) {
  const sorted = [...paths].sort((a, b) => a.length - b.length);
  return sorted.filter((p, i) => !sorted.slice(0, i).some(q => p.startsWith(q + '/')));
}

// ---------- обзор: место и аналитика ----------
let forceSum = false;
const meter = (used, total) => { const p = total ? Math.min(100, used / total * 100) : 0; return `<div class="meter ${p > 90 ? 'hot' : p > 75 ? 'warm' : ''}"><i style="width:${p.toFixed(1)}%"></i></div>`; };
const stat = (title, value, sub = '', cls = '') => `<div class="stat ${cls}"><div class="st-t">${esc(title)}</div><div class="st-v">${value}</div><div class="st-s">${sub}</div></div>`;
const shortDate = d => { const [, m, dd] = d.split('-'); return dd + '.' + m; };
async function viewOverview(el, again = false) {
  if (!again) el.innerHTML = pageHead('Обзор', 'Место в облаке и выгрузки', `<button class="btn" data-act="refreshsum">${IC.refresh} Обновить</button>`) + spinner;
  else el.classList.add('still'); // повторный запрос без повторной анимации появления
  const d = await api('GET', '/api/summary' + (forceSum ? '?refresh=1' : '')); forceSum = false;
  if (d.accounts.some(a => a.pending) && view === 'overview') setTimeout(() => { if (view === 'overview' && $('#view') && !document.querySelector('.overlay')) viewOverview($('#view'), true).catch(() => {}); }, 1500);
  const up = await api('GET', '/api/update').catch(() => ({available: false}));
  const banner = up.available ? `<a class="upd-banner" href="${esc(up.url)}" target="_blank" rel="noopener">${alertBox('info', 'Доступна версия ' + up.version, 'Скачать пакет можно на странице релиза. Установите его через Центр пакетов.')}</a>` : '';
  const t = d.totals, usedPct = t.total ? Math.round(t.used / t.total * 100) : 0;
  const tiles = `<div class="stats">${stat('Всего места', t.known ? fmtBytes(t.total) : '—', t.known ? `по ${t.known} из ${t.accounts} аккаунтов` : (d.accounts.some(a => a.pending) ? 'запрашиваю у облака…' : 'облако не сообщает объём'))}
    ${stat('Занято', t.known ? fmtBytes(t.used) : '—', t.known ? usedPct + '% от объёма' : '')}
    ${stat('Свободно', t.known ? fmtBytes(t.free) : '—', t.known ? (100 - usedPct) + '% свободно' : '', t.known && usedPct > 90 ? 'bad' : 'good')}
    ${stat('Выгружено за 14 дней', fmtBytes(d.jobs.bytes), `запусков: ${d.jobs.runsOk} успешно` + (d.jobs.runsFail ? `, <span class="err-text">${d.jobs.runsFail} с ошибками</span>` : ''))}</div>`;
  const accs = d.accounts.length ? d.accounts.map(a => a.pending ? `<div class="acc-row"><div class="ar-h"><b>${esc(a.name)}</b> <span class="small muted">${esc(providerOf(a.kind).title)}</span><span class="tag run"><i class="dot"></i>запрашиваю…</span></div></div>` : `<div class="acc-row"><div class="ar-h"><b>${esc(a.name)}</b> <span class="small muted">${esc(providerOf(a.kind).title)}</span>
      ${!a.ok ? `<span class="tag err"><i class="dot"></i>ошибка</span>` : a.total ? `<span class="small muted">${fmtBytes(a.used)} из ${fmtBytes(a.total)}</span>` : '<span class="tag muted">объём не сообщается</span>'}</div>
      ${!a.ok ? `<div class="small err-text">${esc(a.error)}</div>` : a.total ? meter(a.used, a.total) + `<div class="small muted">свободно ${fmtBytes(a.free)} · занято ${Math.round(a.used / a.total * 100)}%</div>` : `<div class="small muted">${esc(a.title || 'подключено')}</div>`}</div>`).join('')
    : empty('Аккаунтов пока нет', `<button class="btn primary" data-act="newacc">${IC.plus} Добавить аккаунт</button>`);
  const perProv = S.providers.length > 1 ? `<div class="stats">${S.providers.map(p => { const xs = d.accounts.filter(a => a.kind === p.kind), tot = xs.reduce((s, a) => s + (a.total || 0), 0), used = xs.reduce((s, a) => s + (a.used || 0), 0); return stat(p.title, 'аккаунтов: ' + xs.length, tot ? `занято ${fmtBytes(used)} из ${fmtBytes(tot)}` : 'объём не сообщается'); }).join('')}</div>` : '';
  const donut = t.known ? `<div class="donut" style="--p:${usedPct}"><div class="dn"><b>${usedPct}%</b><span>занято</span></div></div>` : '';
  const max = Math.max(1, ...d.days.map(x => x.bytes));
  const chart = `<div class="bars">${d.days.map(x => `<div class="bar-c" data-tip="${shortDate(x.date)}: ${fmtBytes(x.bytes)}, файлов ${x.files}${x.fail ? ', ошибок: ' + x.fail : ''}">
      <div class="bar-v"><i class="${x.fail ? 'f' : ''}" style="height:${Math.max(x.bytes / max * 100, x.bytes ? 4 : 0).toFixed(1)}%"></i></div><span>${shortDate(x.date)}</span></div>`).join('')}</div>`;
  const vols = d.volumes.length ? d.volumes.map(v => `<div class="acc-row"><div class="ar-h"><b>${esc(v.path)}</b><span class="small muted">свободно ${fmtBytes(v.free)} из ${fmtBytes(v.total)}</span></div>${meter(v.total - v.free, v.total)}</div>`).join('')
    : `<div class="small muted">Тома NAS не определены. Укажите папки в «Настройки → Папки NAS».</div>`;
  el.innerHTML = pageHead('Обзор', 'Место в облаке и выгрузки', `<button class="btn" data-act="refreshsum">${IC.refresh} Обновить</button>`) + banner + tiles + perProv +
    `<div class="grid-ov"><div class="card"><h3 class="ct">Аккаунты</h3><div class="ov-acc">${donut}<div class="grow">${accs}</div></div></div>
      <div class="card"><h3 class="ct">Выгрузки по дням</h3>${chart}<div class="small muted legend"><i></i> успешно <i class="f"></i> с ошибками · заданий: ${d.jobs.count}, включено: ${d.jobs.enabled}</div></div>
      <div class="card"><h3 class="ct">Тома NAS</h3>${vols}</div></div>`;
}

// ---------- аккаунты ----------
async function viewAccounts(el) {
  accounts = await api('GET', '/api/accounts');
  el.innerHTML = pageHead('Аккаунты', S.providers.map(p => p.title).join(' · '), `<button class="btn primary" data-act="newacc">${IC.plus} Добавить аккаунт</button>`) +
    (accounts.length ? `<div class="card flush"><div class="tw"><table><thead><tr><th>Название</th><th>Адрес в шлюзе</th><th>Подключение</th><th></th></tr></thead><tbody>${accounts.map(a => `<tr><td><b>${esc(a.name)}</b>
      <div class="small muted">${esc(providerOf(a.kind).title)} · ${providerOf(a.kind).fields.filter(f => !f.secret && !f.global && !['user_agent', 'split_mb'].includes(f.key) && a.fields[f.key]).map(f => esc(f.options ? ((f.options.find(o => o.value === a.fields[f.key]) || {}).label || a.fields[f.key]) : a.fields[f.key])).join(' · ')}${a.set && a.set.enc_password ? ' · 🔒 шифрование' : ''}${a.fields.split_mb && a.fields.split_mb !== '0' ? ' · части по ' + esc(a.fields.split_mb) + ' МБ' : ''}</div></td><td><code>/${esc(a.slug)}/</code></td>
      <td><span id="t-${a.id}" class="small muted">не проверялось</span></td>
      <td class="act"><button class="btn sm" data-act="testsaved" data-id="${a.id}">${IC.check} Проверить</button>
      <button class="btn sm icon" data-tip="Изменить" aria-label="Изменить" data-act="editacc" data-id="${a.id}">${IC.edit}</button>
      <button class="btn sm icon danger-o" data-tip="Удалить" aria-label="Удалить" data-act="delacc" data-id="${a.id}">${IC.trash}</button></td></tr>`).join('')}</tbody></table></div></div>`
      : `<div class="card">${empty('Аккаунтов пока нет', `<button class="btn primary" data-act="newacc">${IC.plus} Добавить аккаунт</button>`)}</div>`);
  if (accounts.length) api('GET', '/api/summary').then(d => d.accounts.forEach(a => {
    const c = document.getElementById('t-' + a.id); if (!c) return;
    if (a.pending) { c.textContent = 'запрашиваю объём…'; setTimeout(() => { if (view === 'accounts') showView(); }, 2000); return; }
    if (!a.ok) { c.className = 'small err-text'; c.textContent = 'Ошибка: ' + a.error; return; }
    c.className = 'small'; c.innerHTML = a.total ? `${meter(a.used, a.total)}<div class="muted">занято ${fmtBytes(a.used)} из ${fmtBytes(a.total)} · свободно ${fmtBytes(a.free)}</div>` : '<span class="ok-text">подключено</span> <span class="muted">· объём не сообщается</span>';
  })).catch(() => {});
}
function howTo(p) { return (p.steps && p.steps.length) ? timeline(p.steps) : `<p class="small">${linkify(p.help)}</p>`; }
const accFields = (p, a) => p.fields.map(f => f.options ? `<div class="fi">${label(f.label, f.help, 'a-' + f.key)}<select id="a-${f.key}">${f.options.map(o => `<option value="${esc(o.value)}" ${(a.fields[f.key] || f.default) === o.value ? 'selected' : ''}>${esc(o.label)}</option>`).join('')}</select></div>` : `<div class="fi">${label(f.label, f.help, 'a-' + f.key)}<input type="${f.secret ? 'password' : 'text'}" id="a-${f.key}" autocomplete="off"
      value="${f.secret ? '' : esc(f.global ? ((S.globals || {})[f.key] || '') : (a.fields[f.key] || ''))}" placeholder="${f.secret && a.set[f.key] ? 'сохранено — оставьте пустым, чтобы не менять' : esc(f.placeholder || '')}"></div>`).join('') + (p.authUrl ? `<div class="fi"><button class="btn" type="button" data-act="gettoken">${IC.link || ''} Получить токен</button></div>` : '');
function openAccForm(acc) {
  const a = acc || {name:'', slug:'', kind: S.providers[0].kind, fields:{}, set:{}};
  const p = providerOf(a.kind);
  const pick = !acc && S.providers.length > 1 ? `<div class="fi">${label('Облако', 'Вид аккаунта выбирается один раз и потом не меняется.', 'a-kind')}<select id="a-kind">${S.providers.map(x => `<option value="${x.kind}">${esc(x.title)}</option>`).join('')}</select></div>` : '';
  modal(`<h3>${acc ? 'Изменить аккаунт' : 'Новый аккаунт'}</h3>
    <details class="collapse" ${acc ? '' : 'open'}><summary>Как получить данные для подключения</summary><div id="a-how">${howTo(p)}</div></details>
    <form data-form="acc" data-id="${acc ? acc.id : ''}" data-kind="${a.kind}">
    ${pick}
    <div class="fi">${label('Название (для вас)', '', 'a-name')}<input type="text" id="a-name" value="${esc(a.name)}" placeholder="Например: личный"></div>
    <div id="a-fields">${accFields(p, a)}</div>
    <div class="fi">${label('Адрес в WebDAV-шлюзе', 'Латиницей. Короткое имя аккаунта в адресе для File Station. Создаётся автоматически, если оставить пустым.', 'a-slug')}<input type="text" id="a-slug" value="${esc(a.slug)}" placeholder="создаётся автоматически"></div>
    <div id="a-msg"></div>
    <div class="foot"><button class="btn" type="button" data-act="closemodal">Отмена</button><button class="btn" type="button" data-act="testform">Проверить подключение</button><button class="btn primary" type="submit">Сохранить</button></div></form>`);
}
const accBody = () => { const p = providerOf($('form[data-form=acc]').dataset.kind); return {name: $('#a-name').value, slug: $('#a-slug').value, kind: p.kind, fields: Object.fromEntries(p.fields.filter(f => !f.global).map(f => [f.key, $('#a-' + f.key).value]))}; };
// Общие поля (ID приложения Яндекса) хранятся один раз в настройках и подставляются во все аккаунты.
async function saveGlobals() {
  const g = {}; let changed = false;
  allFields().filter(f => f.global).forEach(f => { const v = ($('#a-' + f.key) || {value: (S.globals || {})[f.key] || ''}).value.trim(); g[f.key] = v; if (v !== ((S.globals || {})[f.key] || '')) changed = true; });
  if (!changed) return;
  const r = await api('PUT', '/api/settings', {globals: g}); S.globals = r.globals || {};
}
function infoText(i) { return 'Подключено: ' + (i.title || 'ок') + (i.total ? ` · занято ${fmtBytes(i.used)} из ${fmtBytes(i.total)}` : ''); }

// ---------- журнал ----------
async function viewRuns(el) {
  jobs = await api('GET', '/api/jobs');
  el.innerHTML = pageHead('Журнал запусков', 'Последние 50 запусков каждого задания',
      `<select id="runjob"><option value="">Все задания</option>${jobs.map(j => `<option value="${j.id}" ${j.id === runFilter ? 'selected' : ''}>${esc(j.name)}</option>`).join('')}</select>`) +
    `<div id="runsbox"></div><div class="card" id="logcard" style="display:none"><div class="bar tight"><div class="bt"><h2 id="logtitle"></h2></div></div><pre class="log" id="logpre"></pre></div>`;
  await refreshRuns();
}
async function refreshRuns() {
  const runs = await api('GET', '/api/runs?limit=60' + (runFilter ? '&job=' + runFilter : ''));
  const box = $('#runsbox'); if (!box) return;
  box.innerHTML = runs.length ? `<div class="card flush"><div class="tw"><table class="hover"><thead><tr><th>Начало</th><th>Задание</th><th>Статус</th><th>Итог</th></tr></thead><tbody>${runs.map(r => `
    <tr data-act="openrun" data-id="${r.id}" class="clickable ${r.id === selRun ? 'sel' : ''}"><td>${fmtDate(r.started)}</td><td>${esc(r.jobName)} <span class="small muted">${r.trigger === 'schedule' ? 'по расписанию' : 'вручную'}</span></td>
    <td>${tag(r.status)}</td><td class="small">выгружено ${r.stats.uploaded} · пропущено ${r.stats.skipped} · ошибок ${r.stats.errors} · ${fmtBytes(r.stats.bytes)}${r.message ? `<div class="err-text">${esc(r.message)}</div>` : ''}</td></tr>`).join('')}</tbody></table></div></div>` : `<div class="card">${empty('Запусков пока не было')}</div>`;
}
async function openRun(id, name) {
  selRun = id; logOffset = 0; logRunning = false;
  const card = $('#logcard'); if (!card) return;
  card.style.display = ''; $('#logtitle').textContent = 'Журнал: ' + (name || id); $('#logpre').textContent = '';
  await pollLog(); await refreshRuns(); card.scrollIntoView({behavior: 'smooth'});
}
async function pollLog() {
  if (!selRun || !$('#logpre')) return;
  try {
    const d = await api('GET', `/api/runs/${selRun}/log?offset=${logOffset}`);
    const pre = $('#logpre'); const atEnd = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 30;
    if (d.text) { pre.textContent += d.text; if (atEnd) pre.scrollTop = pre.scrollHeight; }
    logOffset = d.next; logRunning = d.running;
  } catch (e) { logRunning = false; }
}

// ---------- File Station / настройки ----------
async function viewGateway(el) {
  const g = await api('GET', '/api/gateway');
  const steps = [
    {title: 'Откройте мастер удалённого подключения', text: 'В File Station нажмите «Инструменты» → «Удалённое подключение» → «Создать». Откроется «Мастер установки удалённого подключения».'},
    {title: 'Выберите протокол', text: 'В списке сервисов выберите «WebDAV / WebDAV HTTPS» (серый значок с глобусом) и нажмите «Далее».'},
    {title: 'Заполните параметры протокола', text: 'Значения для каждого поля — в таблице ниже. Главное: хост 127.0.0.1, порт ' + g.port + ', в поле «Путь» — адрес аккаунта вида /имя/, галочку «Использовать HTTPS-подключение WebDAV» НЕ ставьте.'},
    {title: 'Логин и пароль', text: 'Имя учётной записи — любое (например nas), пароль — «Пароль шлюза» внизу этой страницы. Кодовую страницу оставьте Unicode (UTF-8).'},
    {title: 'Нажмите «Выполнено»', text: 'Подключение появится в левом дереве File Station под заголовком «WebDAV». Если оно не раскрывается, проверьте, что пакет запущен, а пароль введён верно.'},
    {title: 'Несколько аккаунтов', text: 'Для каждого аккаунта создайте отдельное подключение: у каждого свой «Путь» и своё «Имя профиля».'},
  ];
  const fields = g.accounts.length ? `<div class="card flush"><div class="tw"><table><thead><tr><th>Аккаунт</th><th>Имя хоста</th><th>Порт</th><th>Путь</th><th>Имя учётной записи</th><th>Пароль</th><th>Имя профиля</th></tr></thead><tbody>${g.accounts.map(a => `<tr><td>${esc(a.name)}</td><td><code>127.0.0.1</code></td><td><code>${esc(g.port)}</code></td><td><code>/${esc(a.slug)}/</code></td><td><code>nas</code></td><td class="small muted">пароль шлюза</td><td><code>${esc(a.name)}</code></td></tr>`).join('')}</tbody></table></div></div>` : `<div class="card">${empty('Сначала добавьте аккаунт')}</div>`;
  el.innerHTML = pageHead('Диск в File Station', 'Локальный WebDAV-сервер на самой NAS, порт ' + g.port) +
    `<div class="card"><h3 class="ct">Как подключить</h3>${timeline(steps)}
      ${alertBox('info', 'Ограничения File Station', 'Для облачных и удалённых источников File Station не поддерживает сжатие, распаковку и предпросмотр файлов.')}</div>` +
    `<div class="bar tight"><div class="bt"><h2>Значения для полей мастера</h2><div class="sub">Скопируйте в соответствующие поля</div></div></div>` + fields +
    `<div class="card"><div class="fi">${label('Пароль шлюза', 'При смене пароля нужно обновить подключения в File Station.', 'gwpw')}<div class="row"><input type="password" id="gwpw" value="${esc(g.password)}" readonly>
      <button class="btn" data-act="gwshow">${IC.eye} Показать</button><button class="btn" data-act="gwregen">${IC.refresh} Сменить</button></div></div></div>`;
}
async function viewSettings(el) {
  const st = await api('GET', '/api/settings');
  const globals = allFields().filter(f => f.global);
  el.innerHTML = pageHead('Настройки') +
    `<form class="card" data-form="settings">
      ${globals.map(f => `<div class="fi">${label(f.label, f.help, 's-g-' + f.key)}<input type="text" id="s-g-${f.key}" data-gkey="${f.key}" value="${esc((st.globals || {})[f.key] || '')}" placeholder="${esc(f.placeholder || '')}"></div>`).join('')}
      <div class="fi">${label('Папки NAS для проводника', 'По одной в строке, например /volume1/photo. Пусто — все общие папки томов /volume*/. Пользователю пакета нужны права на эти папки: Панель управления → Общая папка → Изменить → Разрешения → «Системный внутренний пользователь».', 's-roots')}
        <textarea id="s-roots" placeholder="пусто — все общие папки">${esc((st.localRoots || []).join('\n'))}</textarea></div>
      <div class="fi">${label('Временная папка для частей архивов', 'Здесь одновременно лежит одна часть архива (или весь архив, если не разбивать). Нужно свободное место и права записи у пользователя пакета.', 's-tmp')}
        <input type="text" id="s-tmp" value="${esc(st.tmpDir)}" placeholder="по умолчанию — внутри папки данных пакета"></div>
      <div class="fi"><label class="chk"><input type="checkbox" id="s-upd" ${st.updateCheck ? 'checked' : ''}> Проверять новые версии на GitHub раз в сутки ${tip('Запрос уходит на api.github.com: GitHub увидит IP NAS и версию пакета. Установку вы делаете сами через Центр пакетов. Выключено по умолчанию.')}</label></div>
      <div class="fi"><label class="chk"><input type="checkbox" id="s-tele" ${st.telemetry ? 'checked' : ''}> Отправлять разработчику обезличенные отчёты об ошибках ${tip('Отправляется только текст ошибки без путей, имён файлов, адресов и токенов, а также версия пакета и архитектура. Отключено по умолчанию.')}</label></div>
      <div id="s-msg"></div><p><button class="btn primary" type="submit">Сохранить</button></p></form>` +
    (S.auth === 'dsm' ? `<div class="card">${alertBox('info', 'Вход выполняется через вашу учётную запись DSM', 'Отдельный пароль интерфейса не используется.')}</div>` :
    `<form class="card" data-form="password"><h3 class="ct">Сменить пароль интерфейса</h3>
      <div class="fi">${label('Текущий пароль', '', 'p-old')}<input type="password" id="p-old" autocomplete="current-password"></div><div class="fi">${label('Новый пароль', '', 'p-new')}<input type="password" id="p-new" autocomplete="new-password"></div>
      <div id="p-msg"></div><p><button class="btn primary" type="submit">Сменить</button></p></form>`);
}


// ---------- мастер первого входа ----------
const WIZ_KEY = 'cb-wizard-1';
let wizStep = 0, wizShown = false;
function maybeWizard() {
  if (wizShown) return; wizShown = true;
  let done = false; try { done = !!localStorage.getItem(WIZ_KEY); } catch (e) {}
  if (!done) openWizard(0);
}
function wizSteps() {
  const user = S.runUser || 'sc-имя_пакета';
  return [
    {t: 'Добро пожаловать', html: `<p>«${esc(S.title)}» выгружает файлы и архивы с вашей NAS в облако и позволяет работать с облаком как с обычной папкой.</p>
      <ul class="wiz-list"><li><b>Обзор</b> — сколько места занято и свободно в каждом облаке, статистика выгрузок.</li><li><b>Проводник</b> — две панели: NAS и облако, перетаскивание файлов, копирование и перенос.</li>
      <li><b>Задания</b> — автоматические выгрузки по расписанию (зеркало или архив).</li><li><b>Аккаунты</b> — подключённые облака, можно несколько.</li><li><b>File Station</b> — облако в проводнике DSM.</li></ul>
      <p class="muted small">Мастер займёт минуту. Открыть его снова можно кнопкой «?» в шапке.</p>`},
    {t: 'Подключите аккаунт', html: `<p>Сначала подключите облако: ${S.providers.map(p => esc(p.title)).join(', ')}.</p>${S.providers.map(p => `<h4>${esc(p.title)}</h4>${howTo(p)}`).join('')}
      <p class="muted small">Аккаунтов может быть несколько — для каждого своя запись.</p>`, action: ['wizadd', 'Добавить аккаунт сейчас']},
    {t: 'Права на папки NAS', html: `<p>Пакет работает от отдельного пользователя DSM: <code>${esc(user)}</code>. Пока вы не дадите ему права, он не увидит ваши файлы и не сможет ничего записать.</p>
      ${timeline([
        {title: 'Откройте общие папки', text: 'DSM → Панель управления → Общая папка.'},
        {title: 'Выберите папку и нажмите «Изменить»', text: 'Например «docker» или «photo». В окне перейдите на вкладку «Разрешения».'},
        {title: 'Выберите системных пользователей', text: 'В выпадающем списке вместо «Локальные пользователи» выберите «Системный внутренний пользователь».'},
        {title: 'Найдите пользователя пакета', text: 'В списке найдите ' + user + ' (имя начинается с sc- и содержит название пакета).'},
        {title: 'Выдайте права', text: 'Отметьте «Чтение/запись» и сохраните. Только «Чтение» подойдёт, если облако нужно лишь копировать с NAS; для сохранения файлов из облака на NAS нужна запись.'},
        {title: 'Личные папки (homes)', text: 'Пути вроде /volume1/homes/ваш_логин в список не попадают сами: выдайте право на «homes» и добавьте путь в «Настройки → Папки NAS».'},
      ])}`},
    {t: 'Как работает проводник', html: `<ul class="wiz-list"><li>Слева и справа — любые источники: NAS или облако. Перетащите файлы из одной панели в другую и выберите «Копировать» или «Переместить».</li>
      <li>Файлы и целые папки можно перетащить в панель прямо с компьютера — они загрузятся.</li><li>Правый клик — меню; двойной клик — открыть папку или файл (картинки, видео и аудио показываются сразу).</li>
      <li>Горячие клавиши: <kbd>F2</kbd> переименовать, <kbd>F5</kbd> копировать, <kbd>F6</kbd> переместить, <kbd>F7</kbd> новая папка, <kbd>Del</kbd> удалить, <kbd>Tab</kbd> другая панель, <kbd>Backspace</kbd> вверх.</li>
      <li>Внизу — очередь операций с прогрессом; любую можно отменить. Удаление необратимо: корзина не используется.</li></ul>`},
    {t: 'Отчёты об ошибках', html: `<p>Если что-то сломается, разработчик сможет это исправить, получив отчёт об ошибке.</p>
      <ul class="wiz-list"><li>Отправляется только текст ошибки, версия пакета и архитектура процессора.</li><li>Пути, имена файлов и заданий, адреса, e-mail и токены вырезаются до отправки.</li><li>Отправка включается только вашим согласием; изменить решение можно в «Настройках».</li></ul>
      <div class="seg-wrap">${S.telemetry === true ? '<span class="tag success">Отправка включена</span>' : S.telemetry === false ? '<span class="tag muted">Отправка выключена</span>' : '<span class="tag muted">Решение ещё не принято</span>'}</div>`, action: ['teleon', 'Разрешить отправку']},
    {t: 'Автоматические выгрузки', html: `<ul class="wiz-list"><li><b>Зеркало</b> — выгружаются только новые и изменённые файлы.</li><li><b>Архив</b> — потоковый tar.gz, при необходимости режется на части; можно хранить N последних копий.</li>
      <li>Расписание — пресеты (каждую ночь, раз в неделю…) или своё выражение cron. Запускать можно и вручную, ход виден в «Журнале».</li><li><b>File Station</b>: вкладка с пошаговой инструкцией покажет, как подключить облако как удалённую папку DSM.</li></ul>
      <p>Готово! Начните с аккаунта и прав на папки — остальное подтянется.</p>`, action: ['wiztour', 'Экскурсия по интерфейсу'], last: true},
  ];
}
function paintWiz(o) {
  const st = wizSteps(), s = st[wizStep];
  $('.wiz-b', o).innerHTML = `<div class="wiz-n">Шаг ${wizStep + 1} из ${st.length}</div><h3>${esc(s.t)}</h3>${s.html}`;
  $('.wiz-dots', o).innerHTML = st.map((_, i) => `<i class="${i === wizStep ? 'on' : i < wizStep ? 'done' : ''}"></i>`).join('');
  $('.wiz-f', o).innerHTML = `<button class="btn" data-act="wizskip">${wizStep === st.length - 1 ? 'Закрыть' : 'Пропустить'}</button><span class="grow"></span>
    ${wizStep ? '<button class="btn" data-act="wizprev">Назад</button>' : ''}${s.action ? `<button class="btn" data-act="${s.action[0]}">${esc(s.action[1])}</button>` : ''}
    <button class="btn primary" data-act="${s.last ? 'wizskip' : 'wiznext'}">${s.last ? 'Готово' : 'Далее'}</button>`;
  $('.wiz-b', o).scrollTop = 0;
}
function openWizard(step = 0) {
  closeModals(); wizStep = step;
  const o = modal('<div class="wiz-b"></div><div class="wiz-dots"></div><div class="foot wiz-f"></div>', 'wiz'); o.dataset.wiz = '1'; paintWiz(o);
}
function wizFinish() { if (S && S.telemetry == null) { api('PUT', '/api/settings', {telemetry: false}).then(() => { S.telemetry = false; }).catch(() => {}); } try { localStorage.setItem(WIZ_KEY, '1'); } catch (e) {} closeModals(); }


// ---------- экскурсия по интерфейсу ----------
const TOUR = [
  ['[data-v=overview]', 'overview', 'Обзор', 'Сводка по всем аккаунтам: сколько места занято и свободно, график выгрузок за 14 дней и состояние заданий.'],
  ['[data-v=accounts]', 'accounts', 'Аккаунты', 'Здесь подключаются облака. Кнопка «Добавить» откроет форму с подсказками; значок «?» рядом с полем объясняет, где взять нужное значение. Кнопка «Проверить» убеждается, что доступ работает.'],
  ['[data-v=explorer]', 'explorer', 'Проводник', 'Две панели: слева и справа выбираете NAS или облако. Перетаскивайте файлы между панелями или с компьютера, правый клик открывает меню, F5/F6 копируют и переносят.'],
  ['[data-v=jobs]', 'jobs', 'Задания', 'Автоматические выгрузки: что копировать, куда и когда. «Зеркало» отправляет только изменения, «Архив» собирает tar.gz. Кнопка запуска выполнит задание сразу.'],
  ['[data-v=runs]', 'runs', 'Журнал', 'История запусков: сколько файлов выгружено, сколько ошибок, и подробный лог по каждому запуску.'],
  ['[data-v=gateway]', 'gateway', 'File Station', 'Пошаговая инструкция и готовые значения, чтобы подключить облако как удалённую папку в File Station через локальный WebDAV-шлюз.'],
  ['[data-v=settings]', 'settings', 'Настройки', 'Папки NAS для проводника (в том числе homes), временная папка и общий ID приложения Яндекса, который не нужно вводить для каждого аккаунта.'],
  ['[data-act=wizard]', null, 'Мастер знакомства', 'Кнопка «?» снова откроет мастер с правами на папки и подключением аккаунта. Экскурсию можно запустить кнопкой слева.'],
];
let tourI = -1;
function tourEnd() { tourI = -1; const t = document.getElementById('tour'); if (t) t.remove(); }
function tourShow(i) {
  if (i < 0 || i >= TOUR.length) return tourEnd();
  tourI = i; const [sel, v, title, text] = TOUR[i];
  if (v && view !== v) { view = v; render(); }
  let t = document.getElementById('tour');
  if (!t) { t = document.createElement('div'); t.id = 'tour'; document.body.appendChild(t); }
  const el = document.querySelector(sel);
  t.innerHTML = `<div class="tour-hole"></div><div class="tour-pop card" role="dialog"><div class="wiz-n">Экскурсия ${i + 1} из ${TOUR.length}</div><h3>${esc(title)}</h3><p>${esc(text)}</p>
    <div class="foot"><button class="btn sm" data-act="tourend">Закрыть</button><span class="grow"></span>${i ? '<button class="btn sm" data-act="tourprev">Назад</button>' : ''}<button class="btn sm primary" data-act="${i === TOUR.length - 1 ? 'tourend' : 'tournext'}">${i === TOUR.length - 1 ? 'Готово' : 'Далее'}</button></div></div>`;
  const hole = $('.tour-hole', t), pop = $('.tour-pop', t);
  if (el) {
    el.scrollIntoView({block: 'nearest', inline: 'nearest'});
    const r = el.getBoundingClientRect();
    Object.assign(hole.style, {left: r.left - 6 + 'px', top: r.top - 6 + 'px', width: r.width + 12 + 'px', height: r.height + 12 + 'px'});
    const w = Math.min(340, innerWidth - 24);
    pop.style.width = w + 'px';
    pop.style.left = Math.max(12, Math.min(r.left, innerWidth - w - 12)) + 'px';
    pop.style.top = Math.min(r.bottom + 16, Math.max(12, innerHeight - 260)) + 'px';
  }
}
addEventListener('keydown', e => { if (tourI >= 0 && e.key === 'Escape') tourEnd(); });
addEventListener('resize', () => { if (tourI >= 0) tourShow(tourI); });

// ---------- обработчики ----------
const H = {
  async nav(t) { view = t.dataset.v; render(); },
  reload() { loadState(); },
  settheme(t) {
    theme = t.dataset.val;
    try { localStorage.setItem('cb-theme', theme); } catch (e) {}
    applyTheme(); render();
  },
  seg(t) {
    const o = t.closest('.overlay'); $('#f-mode', o).value = t.dataset.val;
    t.parentNode.querySelectorAll('button').forEach(b => b.classList.toggle('on', b === t)); syncJobForm(o);
  },
  async logout() { await api('POST', '/api/logout', {}); S.loggedIn = false; render(); },
  closemodal() { closeModals(); }, closetop() { closeTop(); },
  tree() { openTree(jobFormModal()); },
  async treeexp(t) {
    const m = t.closest('.overlay'), box = t.closest('.trow').nextElementSibling;
    if (box.dataset.open) { box.innerHTML = ''; delete box.dataset.open; t.textContent = '▸'; return; }
    box.dataset.open = '1'; t.textContent = '▾';
    box.innerHTML = spinner;
    await treeLoad(m, t.dataset.p, box, +t.dataset.d + 1);
  },
  treeok(t) { const m = t.closest('.overlay'), o = m._tree.o; o._sources = treeTop([...m._tree.sel]); drawSources(o); m.remove(); },
  cfyes(t) { const o = t.closest('.overlay'), r = o._confirm; o._confirm = null; o.remove(); r && r(true); },
  cfno(t) { const o = t.closest('.overlay'), r = o._confirm; o._confirm = null; o.remove(); r && r(false); },
  newjob() { openJobForm(null); },
  editjob(t) { openJobForm(jobs.find(j => j.id === t.dataset.id)); },
  async deljob(t) { const j = jobs.find(j => j.id === t.dataset.id);
    if (await askConfirm(`Удалить задание «${j.name}»?`, 'Файлы, уже выгруженные в облако, не удаляются.')) { await api('DELETE', '/api/jobs/' + j.id); await refreshJobs(); toast('Задание удалено', 'success'); } },
  async run(t) { await api('POST', `/api/jobs/${t.dataset.id}/run`, {}); await refreshJobs(); toast('Запуск начат', 'success'); },
  async cancel(t) { await api('POST', `/api/jobs/${t.dataset.id}/cancel`, {}); await refreshJobs(); },
  joblog(t) { runFilter = t.dataset.id; view = 'runs'; render(); },
  addsrc() { const o = jobFormModal(), i = $('#f-srcin', o), v = i.value.trim(); if (v) { o._sources.push(v); i.value = ''; drawSources(o); } },
  rmsrc(t) { const o = jobFormModal(); o._sources.splice(+t.dataset.i, 1); drawSources(o); },
  browse() { const o = jobFormModal(); openPicker($('#f-srcin', o).value.trim() || (o._sources[0] || '')); },
  pkgo(t) { pickerLoad(t.closest('.overlay'), t.dataset.p); },
  pickhere(t) { const p = t.closest('.overlay'), o = jobFormModal(); if (p._cur) { o._sources.push(p._cur); drawSources(o); } p.remove(); },
  newacc() { openAccForm(null); },
  async gettoken() {
    const m = $('#a-msg'), url = providerOf($('form[data-form=acc]').dataset.kind).authUrl.replace(/\{(\w+)\}/g, (_, k) => encodeURIComponent((($('#a-' + k) || {}).value || '').trim()));
    if (/=(&|$)/.test(url)) return msg(m, 'Сначала впишите ID приложения', 'error');
    window.open(url, '_blank', 'noopener');
    msg(m, 'Откроется страница Яндекса. Разрешите доступ, скопируйте токен и вставьте в поле «OAuth-токен». ID приложения сохранён для всех аккаунтов.', 'info');
    await saveGlobals();
  },
  editacc(t) { openAccForm(accounts.find(a => a.id === t.dataset.id)); },
  async delacc(t) { const a = accounts.find(a => a.id === t.dataset.id);
    if (await askConfirm(`Удалить аккаунт «${a.name}»?`, 'Токен будет удалён с NAS. Файлы в облаке не затрагиваются.')) { await api('DELETE', '/api/accounts/' + a.id); showView(); toast('Аккаунт удалён', 'success'); } },
  async testsaved(t) { const el = $('#t-' + t.dataset.id); el.className = 'small muted'; el.textContent = 'Проверяю…'; const a = accounts.find(a => a.id === t.dataset.id);
    try { const r = await api('POST', '/api/accounts/test', {id: a.id, fields: {}});
      el.className = r.ok ? 'small ok-text' : 'small err-text'; el.textContent = r.ok ? infoText(r.info) : 'Ошибка: ' + r.error; } catch (e) { el.className = 'small err-text'; el.textContent = e.message; } },
  wizard() { tourEnd(); openWizard(0); },
  tour() { closeModals(); tourShow(0); },
  tournext() { tourShow(tourI + 1); },
  tourprev() { tourShow(tourI - 1); },
  tourend() { tourEnd(); },
  wiznext() { wizStep++; paintWiz($('.overlay[data-wiz]')); },
  wizprev() { wizStep--; paintWiz($('.overlay[data-wiz]')); },
  wizskip() { wizFinish(); },
  async teleon() { try { await api('PUT', '/api/settings', {telemetry: true}); S.telemetry = true; toast('Спасибо! Отправка включена', 'success'); paintWiz($('.overlay[data-wiz]')); } catch (e) { toast(e.message); } },
  wiztour() { wizFinish(); tourShow(0); },
  wizadd() { wizFinish(); view = 'accounts'; render(); setTimeout(() => openAccForm(null), 250); },
  async refreshsum() { forceSum = true; showView(); },
  async testform() { const m = $('#a-msg'); msg(m, 'Проверяю…', 'info'); const id = $('form[data-form=acc]').dataset.id;
    try { const b = accBody(); const r = await api('POST', '/api/accounts/test', {id, kind: b.kind, fields: b.fields}); r.ok ? msg(m, infoText(r.info), 'success') : msg(m, r.error); } catch (e) { msg(m, e.message); } },
  async openrun(t) { const r = t.querySelectorAll('td')[1].textContent; openRun(t.dataset.id, r); },
  gwshow(t) { const i = $('#gwpw'); i.type = i.type === 'password' ? 'text' : 'password'; t.innerHTML = i.type === 'password' ? IC.eye + ' Показать' : IC.eye + ' Скрыть'; },
  async gwregen() { if (await askConfirm('Сменить пароль шлюза?', 'Подключения в File Station придётся обновить.', 'Сменить', false)) { await api('POST', '/api/gateway/regenerate', {}); showView(); toast('Пароль шлюза изменён', 'success'); } },
};
document.addEventListener('click', async e => {
  const t = e.target.closest('[data-act]'); if (!t || !H[t.dataset.act]) return;
  if (t.tagName === 'A') e.preventDefault();
  try { await H[t.dataset.act](t, e); } catch (err) { toast(err.message); }
});
document.addEventListener('change', e => {
  if (e.target.id === 'runjob') { runFilter = e.target.value; refreshRuns(); return; }
  if (e.target.id === 'a-kind') { const p = providerOf(e.target.value); $('form[data-form=acc]').dataset.kind = p.kind; $('#a-fields').innerHTML = accFields(p, {fields: {}, set: {}}); $('#a-how').innerHTML = howTo(p); return; }
  const o = e.target.closest('.overlay'); if (!o) return;
  if (e.target.dataset.tree !== undefined) { treeToggle(o, e.target.dataset.tree, e.target.checked); return; }
  if (e.target.id === 'f-preset') {
    const once = e.target.value === '__once';
    $('#f-once', o).style.display = once ? 'block' : 'none';
    if (e.target.value !== '__custom' && !once) $('#f-cron', o).value = e.target.value;
  }
  if (e.target.id === 'f-once' && e.target.value) $('#f-cron', o).value = '@at ' + e.target.value.replace('T', ' ');
});
document.addEventListener('input', e => {
  if (e.target.id !== 'f-cron') return; const o = e.target.closest('.overlay'), p = $('#f-preset', o);
  p.value = PRESETS.some(x => x.cron === e.target.value) ? e.target.value : '__custom';
  $('#f-once', o).style.display = 'none';
});
document.addEventListener('submit', async e => {
  e.preventDefault(); const f = e.target, kind = f.dataset.form;
  try {
    if (kind === 'setup' || kind === 'login') {
      try { await api('POST', '/api/' + kind, {password: $('#pw').value}); await loadState(); } catch (err) { msg($('#authmsg'), err.message); }
    } else if (kind === 'job') await saveJob(f);
    else if (kind === 'acc') {
      const id = f.dataset.id; try { await saveGlobals(); await api(id ? 'PUT' : 'POST', '/api/accounts' + (id ? '/' + id : ''), accBody()); closeModals(); showView(); toast('Аккаунт сохранён', 'success'); } catch (err) { msg($('#a-msg'), err.message); }
    } else if (kind === 'settings') {
      try { const g = {}; f.querySelectorAll('[data-gkey]').forEach(i => g[i.dataset.gkey] = i.value); const r = await api('PUT', '/api/settings', {tmpDir: $('#s-tmp').value, globals: g, localRoots: $('#s-roots').value.split('\n'), telemetry: $('#s-tele').checked, updateCheck: $('#s-upd').checked}); S.telemetry = $('#s-tele').checked; S.globals = r.globals || {}; msg($('#s-msg'), 'Сохранено', 'success'); } catch (err) { msg($('#s-msg'), err.message); }
    } else if (kind === 'password') {
      try { await api('POST', '/api/password', {old: $('#p-old').value, new: $('#p-new').value}); msg($('#p-msg'), 'Пароль изменён', 'success'); f.reset(); } catch (err) { msg($('#p-msg'), err.message); }
    }
  } catch (err) { toast(err.message); }
});
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeTop(); });

setInterval(async () => {
  if (!S || !S.loggedIn || document.hidden) return;
  try {
    if (view === 'jobs' && !document.querySelector('.overlay')) await refreshJobs();
    if (view === 'runs') { await refreshRuns(); if (logRunning) await pollLog(); }
  } catch (e) {}
}, 3000);

window.CB = {esc, $, fmtBytes, fmtDate, IC, svg, api, U, toast, modal, closeModals, askConfirm, tag, label, empty, alertBox, segmented, spinner, pageHead, ensureToken,
  get S() { return S; }, get token() { return SYNO_TOKEN; }, get base() { return BASE; }, get view() { return view; }, msg};
loadState().catch(e => { $app.innerHTML = `<p class="center">Не удалось связаться с сервисом: ${esc(e.message)}</p>`; });
})();

// ---------- отчёты об ошибках интерфейса (уходят в Sentry через бэкенд, только по согласию) ----------
(function () {
  let n = 0;
  function report(msg) {
    if (!S || S.telemetry !== true || ++n > 5) return;
    fetch(U('/api/telemetry/client'), {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json', 'X-Requested-With': 'cloudbackup'}, body: JSON.stringify({msg: String(msg), view: typeof view === 'string' ? view : ''})}).catch(() => {});
  }
  addEventListener('error', e => report((e.message || 'error') + ' @' + (e.filename || '').split('/').pop() + ':' + e.lineno));
  addEventListener('unhandledrejection', e => report('unhandledrejection: ' + ((e.reason && e.reason.message) || e.reason)));
})();
