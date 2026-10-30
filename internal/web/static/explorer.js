// Проводник: две панели (NAS и облака), перетаскивание, горячие клавиши, очередь операций.
(() => {
'use strict';
const C = window.CB;
const {esc, $, fmtBytes, fmtDate, IC, api, toast, modal, askConfirm, alertBox, svg} = C;
const enc = encodeURIComponent;

// ---------- иконки интерфейса ----------
const I = {
  back: svg('<path d="M15 18l-6-6 6-6"/>'), fwd: svg('<path d="M9 18l6-6-6-6"/>'), up: IC.up, refresh: IC.refresh,
  search: svg('<circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/>'),
  download: svg('<path d="M12 4v11M7 11l5 5 5-5M5 20h14"/>'), upload: svg('<path d="M12 16V5M7 9l5-5 5 5M5 20h14"/>'),
  copy: svg('<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V6a2 2 0 012-2h9"/>'),
  move: svg('<path d="M4 12h15M13 6l6 6-6 6"/>'), rename: IC.edit, trash: IC.trash,
  newfolder: svg('<path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2V7z"/><path d="M12 11v6M9 14h6"/>'),
  info: IC.info, close: svg('<path d="M18 6L6 18M6 6l12 12"/>'), swap: svg('<path d="M7 4L3 8l4 4M3 8h15M17 20l4-4-4-4M21 16H6"/>'),
  vDetails: svg('<path d="M4 6h16M4 12h16M4 18h16"/>'), vList: svg('<path d="M4 6h6M4 12h6M4 18h6M14 6h6M14 12h6M14 18h6"/>'),
  vTiles: svg('<rect x="3" y="4" width="8" height="7" rx="1.5"/><rect x="13" y="4" width="8" height="7" rx="1.5"/><rect x="3" y="14" width="8" height="7" rx="1.5"/><rect x="13" y="14" width="8" height="7" rx="1.5"/>'),
  vIcons: svg('<rect x="3" y="3" width="5" height="5" rx="1"/><rect x="10" y="3" width="5" height="5" rx="1"/><rect x="17" y="3" width="4" height="5" rx="1"/><rect x="3" y="10" width="5" height="5" rx="1"/><rect x="10" y="10" width="5" height="5" rx="1"/><rect x="17" y="10" width="4" height="5" rx="1"/><rect x="3" y="17" width="5" height="4" rx="1"/><rect x="10" y="17" width="5" height="4" rx="1"/>'),
  l1: svg('<rect x="3" y="4" width="18" height="16" rx="2"/>'), lh: svg('<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M12 4v16"/>'),
  lv: svg('<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 12h18"/>'),
  nas: svg('<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>'),
  cloud: svg('<path d="M18 18a4 4 0 000-8 6 6 0 00-11.6-1.5A4.5 4.5 0 007 18h11z"/>'),
  open: svg('<path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 01-1 1H5a1 1 0 01-1-1V7a1 1 0 011-1h5"/>'),
};

// ---------- значки файлов ----------
const KINDS = {
  img: ['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp', 'svg', 'heic', 'avif', 'tif', 'tiff', 'raw', 'cr2', 'nef', 'ico'],
  vid: ['mp4', 'mkv', 'avi', 'mov', 'wmv', 'webm', 'flv', 'm4v', 'mpg', 'mpeg', 'ts', 'm2ts'],
  aud: ['mp3', 'flac', 'wav', 'ogg', 'm4a', 'aac', 'wma', 'opus'],
  arc: ['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'tgz', 'iso', 'dmg', 'img'],
  doc: ['doc', 'docx', 'odt', 'rtf', 'txt', 'md', 'log', 'pages'],
  xls: ['xls', 'xlsx', 'ods', 'csv', 'numbers'], ppt: ['ppt', 'pptx', 'odp', 'key'], pdf: ['pdf', 'epub', 'djvu', 'fb2'],
  code: ['js', 'ts', 'py', 'go', 'rb', 'java', 'c', 'cpp', 'h', 'cs', 'php', 'sh', 'json', 'xml', 'yml', 'yaml', 'html', 'css', 'sql', 'rs', 'toml', 'ini', 'conf'],
};
const COLORS = {img: '#22b573', vid: '#8b5cf6', aud: '#f59e0b', arc: '#d97706', doc: '#3b82f6', xls: '#16a34a', ppt: '#ea580c', pdf: '#ef4444', code: '#06b6d4', gen: '#94a3b8'};
const TYPE_NAMES = {img: 'Изображение', vid: 'Видео', aud: 'Аудио', arc: 'Архив', doc: 'Документ', xls: 'Таблица', ppt: 'Презентация', pdf: 'PDF / книга', code: 'Код / данные', gen: 'Файл'};
const extOf = n => { const i = n.lastIndexOf('.'); return i > 0 ? n.slice(i + 1).toLowerCase() : ''; };
const kindOf = n => { const x = extOf(n); for (const k in KINDS) if (KINDS[k].includes(x)) return k; return 'gen'; };
const typeName = e => e.isDir ? 'Папка' : (extOf(e.name) ? extOf(e.name).toUpperCase() + ' · ' : '') + TYPE_NAMES[kindOf(e.name)];
function fileIcon(e) {
  if (e.isDir) return '<svg class="fic" viewBox="0 0 48 48" aria-hidden="true"><path d="M4 11a3 3 0 013-3h10l4 4h20a3 3 0 013 3v4H4z" fill="#d89212"/><path d="M4 18a3 3 0 013-3h34a3 3 0 013 3v19a3 3 0 01-3 3H7a3 3 0 01-3-3z" fill="#ffc83d"/><path d="M4 18a3 3 0 013-3h34a3 3 0 013 3v2H4z" fill="#ffe08a" opacity=".7"/></svg>';
  const k = kindOf(e.name), col = COLORS[k], x = extOf(e.name).slice(0, 4).toUpperCase();
  return `<svg class="fic" viewBox="0 0 48 48" aria-hidden="true"><path d="M11 4h18l10 10v28a2 2 0 01-2 2H11a2 2 0 01-2-2V6a2 2 0 012-2z" fill="var(--fi-paper)" stroke="${col}" stroke-width="2"/><path d="M29 4v8a2 2 0 002 2h8" fill="${col}" opacity=".35"/><rect x="9" y="27" width="26" height="12" rx="3" fill="${col}"/>${x ? `<text x="22" y="36" text-anchor="middle" font-size="${x.length > 3 ? 7.5 : 9}" font-weight="700" fill="#fff" font-family="system-ui,sans-serif">${esc(x)}</text>` : ''}</svg>`;
}

// ---------- состояние ----------
const mkPane = (src = 'local', path = '/') => ({src, path, hist: [], hi: -1, entries: [], sel: new Set(), anchor: null, cur: null, view: 'details',
  sort: 'name', dir: 1, filter: '', loading: false, err: '', free: 0, limit: 300, seq: 0});
const X = {sources: [], autoRoots: false, layout: 'h', panes: [mkPane(), mkPane()], active: 0, tasks: [], taskState: null, uploads: [], conflict: 'rename',
  quota: {}, speeds: {}, handled: null, inited: false, dockOpen: true};
let root = null, bound = false, upSeq = 0, tick = 0, typeBuf = '', typeAt = 0;

const save = () => { try { localStorage.setItem('cbx', JSON.stringify({layout: X.layout, conflict: X.conflict, views: X.panes.map(p => [p.view, p.sort, p.dir]), paths: X.panes.map(p => [p.src, p.path])})); } catch (e) {} };
const restore = () => { try { return JSON.parse(localStorage.getItem('cbx') || 'null'); } catch (e) { return null; } };
const srcName = id => (X.sources.find(s => s.id === id) || {name: '?'}).name;
const srcIcon = id => id === 'local' ? I.nas : I.cloud;
const pane = i => X.panes[i];
const paneEl = i => root && $(`.xp-pane[data-i="${i}"]`, root);
const join = (a, b) => (a === '/' ? '' : a) + '/' + b;
const parent = p => p === '/' ? '/' : (p.replace(/\/[^/]+$/, '') || '/');
const baseName = p => p.split('/').filter(Boolean).pop() || '/';
const other = i => (X.layout === '1' ? i : 1 - i);

function urlFor(src, path, dl) {
  const q = `/api/x/file?src=${enc(src)}&path=${enc(path)}${dl ? '&dl=1' : ''}`;
  return C.base ? C.U(q) + (C.token ? '&SynoToken=' + enc(C.token) : '') : q;
}

// ---------- представление ----------
async function view(el) {
  el.innerHTML = C.spinner;
  const d = await api('GET', '/api/x/sources');
  X.sources = d.sources; X.autoRoots = d.autoRoots;
  if (!X.inited) {
    X.inited = true;
    const r = restore();
    if (r) {
      X.layout = r.layout || 'h'; X.conflict = r.conflict || 'rename';
      (r.views || []).forEach((v, i) => { if (X.panes[i]) { X.panes[i].view = v[0] || 'details'; X.panes[i].sort = v[1] || 'name'; X.panes[i].dir = v[2] || 1; } });
      (r.paths || []).forEach((p, i) => { if (X.panes[i] && X.sources.some(s => s.id === p[0])) { X.panes[i].src = p[0]; X.panes[i].path = p[1] || '/'; } });
    } else if (X.sources.length > 1) X.panes[1].src = X.sources[1].id;
  }
  X.panes.forEach(p => { if (!X.sources.some(s => s.id === p.src)) { p.src = 'local'; p.path = '/'; } });
  el.innerHTML = `<div class="xp">
    <div class="xp-bar" id="xp-bar"></div>
    <div class="xp-panes l-${X.layout}">${[0, 1].map(i => `<section class="xp-pane" data-i="${i}">
      <div class="xp-head"><div class="xp-nav"></div><select class="xp-src" aria-label="Источник"></select><div class="xp-addr"></div>
        <label class="xp-filter">${I.search}<input type="search" data-filter placeholder="Поиск в папке" aria-label="Поиск в папке"></label><div class="xp-views"></div></div>
      <div class="xp-body" tabindex="0"></div><div class="xp-foot"></div></section>`).join('')}</div>
    <div class="xp-dock" id="xp-dock"></div></div>`;
  root = $('.xp', el);
  const shell = el.closest('.shell'); if (shell) shell.classList.add('wide');
  bindRoot(); bindGlobal();
  paintBar(); X.panes.forEach((p, i) => { paintHead(i); $(`[data-filter]`, paneEl(i)).value = p.filter; });
  paintDock(); markActive();
  X.panes.forEach((p, i) => { if (X.layout !== '1' || i === 0) load(i, p.path, {push: true, first: true}); });
  api('GET', '/api/summary').then(s => { s.accounts.forEach(a => { X.quota[a.id] = a; }); X.panes.forEach((_, i) => paintFoot(i)); }).catch(() => {});
  pollTasks();
}

// ---------- панель инструментов ----------
function paintBar() {
  const b = (a, ic, t, tip, cls = '') => `<button class="btn sm ${cls}" data-xa="${a}" data-tip="${esc(tip)}">${ic}<span class="bl">${t}</span></button>`;
  $('#xp-bar', root).innerHTML = `<div class="xp-tools">
    ${b('mkdir', I.newfolder, 'Папка', 'Новая папка (F7)')}${b('upload', I.upload, 'Загрузить', 'Загрузить файлы с компьютера в текущую папку')}
    ${b('download', I.download, 'Скачать', 'Скачать выбранные файлы')}${(X.sources.find(x => x.id === pane(X.active).src) || {}).canTrash ? b('emptytrash', I.trash, 'Корзина', 'Очистить корзину облака') : ''}<i class="sep"></i>
    ${b('copy', I.copy, 'Копировать', 'Копировать в соседнюю панель (F5)')}${b('move', I.move, 'Переместить', 'Переместить в соседнюю панель (F6)')}<i class="sep"></i>
    ${b('rename', I.rename, 'Имя', 'Переименовать (F2)')}${b('info', I.info, 'Свойства', 'Свойства (Alt+Enter)')}${b('delete', I.trash, 'Удалить', 'Удалить (Delete)', 'danger-o')}</div>
    <div class="xp-opts"><label class="xp-conf" data-tip="Что делать, если файл с таким именем уже есть в папке назначения">При совпадении имён
      <select id="xp-conflict"><option value="rename">переименовать</option><option value="overwrite">заменить</option><option value="skip">пропустить</option></select></label>
      <div class="segmented">${[['h', I.lh, 'Две панели рядом'], ['v', I.lv, 'Две панели друг под другом'], ['1', I.l1, 'Одна панель']].map(([v, ic, t]) => `<button type="button" data-xa="layout" data-v="${v}" data-tip="${t}" class="${X.layout === v ? 'on' : ''}">${ic}</button>`).join('')}</div>
      <button class="btn sm icon" data-xa="swap" data-tip="Поменять панели местами">${I.swap}</button></div>`;
  $('#xp-conflict', root).value = X.conflict;
}

// ---------- шапка панели ----------
function paintHead(i) {
  const p = pane(i), el = paneEl(i); if (!el) return;
  $('.xp-nav', el).innerHTML = `<button class="btn sm icon" data-xa="back" ${p.hi > 0 ? '' : 'disabled'} data-tip="Назад (Alt+←)">${I.back}</button>
    <button class="btn sm icon" data-xa="fwd" ${p.hi < p.hist.length - 1 ? '' : 'disabled'} data-tip="Вперёд (Alt+→)">${I.fwd}</button>
    <button class="btn sm icon" data-xa="upDir" ${p.path !== '/' ? '' : 'disabled'} data-tip="Вверх (Backspace)">${I.up}</button>
    <button class="btn sm icon" data-xa="refresh" data-tip="Обновить">${I.refresh}</button>`;
  const sel = $('.xp-src', el);
  sel.innerHTML = X.sources.map(s => `<option value="${esc(s.id)}" ${s.id === p.src ? 'selected' : ''}>${s.id === 'local' ? 'NAS' : '☁ ' + esc(s.name)}</option>`).join('');
  const parts = p.path.split('/').filter(Boolean); let acc = '';
  $('.xp-addr', el).innerHTML = `<div class="crumbs" data-xa="addredit">${`<button class="cr" data-xa="crumb" data-path="/">${srcIcon(p.src)}<span>${esc(srcName(p.src))}</span></button>` +
    parts.map(x => { acc += '/' + x; return `<i>›</i><button class="cr" data-xa="crumb" data-path="${esc(acc)}"><span>${esc(x)}</span></button>`; }).join('')}</div>`;
  const cr = $('.crumbs', el); cr.scrollLeft = cr.scrollWidth;
  $('.xp-views', el).innerHTML = [['details', I.vDetails, 'Таблица'], ['list', I.vList, 'Список'], ['tiles', I.vTiles, 'Плитки'], ['icons', I.vIcons, 'Крупные значки']]
    .map(([v, ic, t]) => `<button class="btn sm icon ${p.view === v ? 'on' : ''}" data-xa="view" data-v="${v}" data-tip="${t}">${ic}</button>`).join('');
}

// ---------- загрузка папки ----------
async function load(i, path, {push = false, keepSel = false, first = false, fresh = false} = {}) {
  const p = pane(i), seq = ++p.seq; p.loading = true; p.err = '';
  paintBody(i);
  try {
    const d = await api('GET', `/api/x/list?src=${enc(p.src)}&path=${enc(path)}${fresh ? '&fresh=1' : ''}`);
    if (seq !== p.seq) return;
    p.ok = true; const old = p.sel; p.path = d.path; p.entries = d.entries; p.free = d.free || 0; p.limit = 300;
    p.sel = keepSel ? new Set([...old].filter(x => d.entries.some(e => e.path === x))) : new Set();
    if (!keepSel) { p.anchor = null; p.cur = null; }
    if (push) { p.hist = p.hist.slice(0, p.hi + 1); if (p.hist[p.hist.length - 1] !== d.path || p.hi < 0) { p.hist.push(d.path); p.hi = p.hist.length - 1; } }
  } catch (e) {
    if (seq !== p.seq) return;
    if (p.ok && !first) { // как в проводнике Windows: остаёмся в прежней папке и сообщаем об отказе
      p.loading = false; paintHead(i); paintBody(i); toast(`${path === '/' ? 'Корень' : baseName(path)}: ${e.message}`, 'error'); return;
    }
    p.err = e.message; p.entries = []; p.ok = false;
    if (first && p.path !== '/') { p.path = '/'; p.loading = false; return load(i, '/', {push: true}); }
  }
  p.loading = false;
  paintHead(i); paintBody(i); paintFoot(i); save();
}
const go = (i, path) => { X.active = i; markActive(); return load(i, path, {push: true}); };
const refresh = (i, keep = true) => load(i, pane(i).path, {keepSel: keep, fresh: true});

// ---------- сортировка и отбор ----------
function visible(i) {
  const p = pane(i), f = p.filter.trim().toLowerCase();
  const k = p.sort, d = p.dir, col = new Intl.Collator('ru', {numeric: true, sensitivity: 'base'});
  const cmp = {name: (a, b) => col.compare(a.name, b.name), size: (a, b) => a.size - b.size || col.compare(a.name, b.name),
    mtime: (a, b) => a.mtime - b.mtime || col.compare(a.name, b.name), type: (a, b) => col.compare(extOf(a.name), extOf(b.name)) || col.compare(a.name, b.name)}[k];
  return p.entries.filter(e => !f || e.name.toLowerCase().includes(f))
    .sort((a, b) => (b.isDir - a.isDir) || cmp(a, b) * d);
}
const selected = i => pane(i).entries.filter(e => pane(i).sel.has(e.path));

// ---------- список файлов ----------
function rowHTML(e, v) {
  const thumb = v === 'icons' && kindOf(e.name) === 'img' && !e.isDir && e.size > 0 && e.size < 6e6 && /\.(jpe?g|png|gif|webp|bmp|avif)$/i.test(e.name);
  const ic = thumb ? `<img class="thumb" loading="lazy" alt="" src="${esc(urlFor(CUR_SRC, e.path))}">` : fileIcon(e);
  const sub = e.isDir ? 'Папка' : fmtBytes(e.size);
  const name = `<span class="nm" title="${esc(e.name)}">${esc(e.name)}</span>`;
  if (v === 'details') return `<div class="xr" role="row" draggable="true" data-p="${esc(e.path)}" data-d="${e.isDir ? 1 : 0}"><span class="c-n">${ic}${name}</span><span class="c-m">${e.mtime ? fmtDate(e.mtime * 1000) : ''}</span><span class="c-t">${esc(typeName(e))}</span><span class="c-s">${e.isDir ? '' : fmtBytes(e.size)}</span></div>`;
  if (v === 'list') return `<div class="xr" role="row" draggable="true" data-p="${esc(e.path)}" data-d="${e.isDir ? 1 : 0}">${ic}${name}</div>`;
  if (v === 'tiles') return `<div class="xr" role="row" draggable="true" data-p="${esc(e.path)}" data-d="${e.isDir ? 1 : 0}">${ic}<span class="tx">${name}<small>${esc(typeName(e))}</small><small>${esc(sub)}${e.mtime ? ' · ' + fmtDate(e.mtime * 1000) : ''}</small></span></div>`;
  return `<div class="xr" role="row" draggable="true" data-p="${esc(e.path)}" data-d="${e.isDir ? 1 : 0}">${ic}${name}</div>`;
}
let CUR_SRC = 'local';
function paintBody(i, keepScroll = false) {
  const p = pane(i), el = paneEl(i); if (!el) return;
  const b = $('.xp-body', el), top = keepScroll ? b.scrollTop : 0;
  b.className = 'xp-body v-' + p.view + (p.loading ? ' loading' : '');
  CUR_SRC = p.src;
  let html = '';
  if (p.err) {
    const hint = p.src === 'local' ? `<div class="small muted">Проверьте «Настройки → Папки NAS» и права пользователя пакета на общие папки в DSM.</div>` : '';
    html = `<div class="xp-msg">${alertBox('error', p.err)}${hint}<p><button class="btn sm" data-xa="refresh">${I.refresh} Повторить</button></p></div>`;
  } else {
    const list = visible(i);
    if (!list.length && !p.loading) html = `<div class="xp-msg">${empty(p.filter ? 'Ничего не найдено' : 'Папка пуста')}${!p.filter ? '<div class="small muted center-t">Перетащите сюда файлы с компьютера или из соседней панели</div>' : ''}</div>`;
    else {
      const shown = list.slice(0, p.limit);
      const arrow = k => p.sort === k ? `<i class="ar">${p.dir > 0 ? '▲' : '▼'}</i>` : '';
      if (p.view === 'details') html += `<div class="xh"><span class="c-n" data-xa="sort" data-k="name">Имя${arrow('name')}</span><span class="c-m" data-xa="sort" data-k="mtime">Изменён${arrow('mtime')}</span><span class="c-t" data-xa="sort" data-k="type">Тип${arrow('type')}</span><span class="c-s" data-xa="sort" data-k="size">Размер${arrow('size')}</span></div>`;
      html += shown.map(e => rowHTML(e, p.view)).join('');
      if (list.length > shown.length) html += `<div class="xp-more">Показано ${shown.length} из ${list.length} · прокрутите вниз, чтобы загрузить ещё</div>`;
    }
  }
  b.innerHTML = html + (p.loading ? '<div class="xp-load"><span class="spin"></span></div>' : '');
  b.scrollTop = top;
  paintSel(i); paintFoot(i);
}
function paintSel(i) {
  const p = pane(i), el = paneEl(i); if (!el) return;
  el.querySelectorAll('.xr').forEach(r => { const on = p.sel.has(r.dataset.p); r.classList.toggle('sel', on); r.setAttribute('aria-selected', on); r.classList.toggle('cur', r.dataset.p === p.cur && X.active === i); });
}
function paintFoot(i) {
  const p = pane(i), el = paneEl(i); if (!el) return;
  const dirs = p.entries.filter(e => e.isDir).length, files = p.entries.length - dirs, sel = selected(i);
  const sz = sel.filter(e => !e.isDir).reduce((a, e) => a + e.size, 0);
  const q = X.quota[p.src];
  const space = p.src === 'local' ? (p.free ? `Свободно ${fmtBytes(p.free)}` : '') : (q && q.ok && q.total ? `Свободно ${fmtBytes(q.free)} из ${fmtBytes(q.total)}` : '');
  $('.xp-foot', el).innerHTML = `<span>${sel.length ? `Выбрано: ${sel.length}${sz ? ' · ' + fmtBytes(sz) : ''}` : `Папок: ${dirs} · файлов: ${files}`}</span><span class="grow"></span><span>${esc(space)}</span>`;
}
function markActive() {
  root && root.querySelectorAll('.xp-pane').forEach(el => el.classList.toggle('active', +el.dataset.i === X.active));
  X.panes.forEach((_, i) => paintSel(i));
}

// ---------- выделение ----------
function selectOnly(i, path) { const p = pane(i); p.sel = path ? new Set([path]) : new Set(); p.anchor = path; p.cur = path; }
function clickRow(i, path, ev) {
  const p = pane(i), list = visible(i).map(e => e.path);
  if (ev.shiftKey && p.anchor && list.includes(p.anchor)) {
    const a = list.indexOf(p.anchor), b = list.indexOf(path);
    p.sel = new Set(list.slice(Math.min(a, b), Math.max(a, b) + 1)); p.cur = path;
  } else if (ev.ctrlKey || ev.metaKey) { p.sel.has(path) ? p.sel.delete(path) : p.sel.add(path); p.anchor = path; p.cur = path; }
  else selectOnly(i, path);
  X.active = i; markActive(); paintFoot(i);
}
function moveCursor(i, delta, extend) {
  const p = pane(i), list = visible(i); if (!list.length) return;
  let idx = list.findIndex(e => e.path === p.cur); if (idx < 0) idx = delta > 0 ? -1 : list.length;
  const n = Math.max(0, Math.min(list.length - 1, idx + delta)), path = list[n].path;
  if (extend && p.anchor) { const a = list.findIndex(e => e.path === p.anchor); p.sel = new Set(list.slice(Math.min(a, n), Math.max(a, n) + 1).map(e => e.path)); p.cur = path; }
  else selectOnly(i, path);
  if (n >= p.limit - 5) { p.limit += 300; paintBody(i, true); }
  paintSel(i); paintFoot(i);
  const r = paneEl(i).querySelector(`.xr[data-p="${CSS.escape(path)}"]`); if (r) r.scrollIntoView({block: 'nearest'});
}
function gridCols(i) {
  const p = pane(i), b = $('.xp-body', paneEl(i)), r = b.querySelector('.xr');
  if (!r || p.view === 'details' || p.view === 'list') return 1;
  return Math.max(1, Math.floor(b.clientWidth / r.offsetWidth));
}

// ---------- действия над файлами ----------
function openEntry(i, e) {
  if (e.isDir) return go(i, e.path);
  const p = pane(i);
  if (/\.(jpe?g|png|gif|webp|bmp|avif|mp4|webm|mov|ogv|mp3|m4a|ogg|wav|flac)$/i.test(e.name)) return preview(p.src, e);
  download(p.src, [e]);
}
function download(src, entries) {
  const files = entries.filter(e => !e.isDir);
  if (!files.length) return toast('Скачивать можно только файлы. Папки копируйте в соседнюю панель', 'warning');
  files.forEach((e, n) => setTimeout(() => { const a = document.createElement('a'); a.href = urlFor(src, e.path, true); a.download = e.name; document.body.appendChild(a); a.click(); a.remove(); }, n * 350));
}
function preview(src, e) {
  const u = urlFor(src, e.path), v = /\.(mp4|webm|mov|ogv)$/i.test(e.name), a = /\.(mp3|m4a|ogg|wav|flac)$/i.test(e.name);
  const o = modal(`<div class="pv-h"><b title="${esc(e.name)}">${esc(e.name)}</b><span class="grow"></span><button class="btn sm" data-xm="dl">${I.download} Скачать</button><button class="btn sm icon" data-xm="close" aria-label="Закрыть">${I.close}</button></div>
    <div class="pv-b">${v ? `<video src="${esc(u)}" controls autoplay></video>` : a ? `<audio src="${esc(u)}" controls autoplay></audio>` : `<img src="${esc(u)}" alt="">`}</div>`, 'preview');
  o.addEventListener('click', ev => { const t = ev.target.closest('[data-xm]'); if (ev.target === o || (t && t.dataset.xm === 'close')) o.remove(); else if (t) download(src, [e]); });
}
function promptName(title, value, okText = 'Готово') {
  return new Promise(res => {
    const o = modal(`<h3>${esc(title)}</h3><form><div class="fi"><input type="text" id="xn" value="${esc(value)}" autocomplete="off" spellcheck="false"></div><div id="xn-msg"></div>
      <div class="foot"><button type="button" class="btn" data-xm="no">Отмена</button><button class="btn primary" type="submit">${esc(okText)}</button></div></form>`, 'narrow');
    const inp = $('#xn', o); inp.focus(); const dot = value.lastIndexOf('.'); inp.setSelectionRange(0, dot > 0 ? dot : value.length);
    o._confirm = res;
    o.addEventListener('click', ev => { if (ev.target === o || ev.target.closest('[data-xm=no]')) { o._confirm = null; o.remove(); res(null); } });
    $('form', o).addEventListener('submit', ev => { ev.preventDefault(); const v = inp.value.trim(); if (!v || /[\/\\]/.test(v)) { $('#xn-msg', o).innerHTML = alertBox('error', 'Имя пустое или содержит / \\'); return; } o._confirm = null; o.remove(); res(v); });
  });
}
async function mkdir(i) {
  const p = pane(i), name = await promptName('Новая папка', 'Новая папка', 'Создать'); if (!name) return;
  try { await api('POST', '/api/x/mkdir', {src: p.src, path: join(p.path, name)}); await refresh(i); selectOnly(i, join(p.path, name)); paintSel(i); toast('Папка создана', 'success'); } catch (e) { toast(e.message); }
}
async function rename(i) {
  const s = selected(i); if (s.length !== 1) return toast('Выберите один объект', 'warning');
  const p = pane(i), name = await promptName('Переименовать', s[0].name); if (!name || name === s[0].name) return;
  try { const r = await api('POST', '/api/x/rename', {src: p.src, from: s[0].path, name}); await refresh(i, false); selectOnly(i, r.path); paintSel(i); } catch (e) { toast(e.message); }
}
async function emptyTrash(i) {
  const p = pane(i);
  if (!await askConfirm('Очистить корзину?', 'Все файлы в корзине облака будут удалены безвозвратно.', 'Очистить')) return;
  try { await api('POST', '/api/x/trash/empty', {src: p.src}); toast('Корзина очищена', 'success'); } catch (e) { toast(e.message, 'error'); }
}
async function del(i, trash = false) {
  const s = selected(i); if (!s.length) return toast('Ничего не выбрано', 'warning');
  const p = pane(i), what = s.length === 1 ? `«${s[0].name}»` : `выбранные объекты (${s.length})`;
  const folders = s.some(e => e.isDir) ? ' вместе с содержимым папок' : '';
  const ok = trash
    ? await askConfirm('В корзину?', `Будет перенесено в корзину облака: ${what}${folders}. Восстановить можно из корзины облака.`, 'В корзину')
    : await askConfirm('Удалить?', `Будет безвозвратно удалено: ${what}${folders}. Корзина не используется.`, 'Удалить');
  if (!ok) return;
  try { const r = await api('POST', '/api/x/delete', {src: p.src, paths: s.map(e => e.path), trash}); if (r.errors && r.errors.length) toast(r.errors[0], 'error'); else toast((trash ? 'В корзину: ' : 'Удалено: ') + r.deleted, 'success'); await refresh(i, false); } catch (e) { toast(e.message); }
}
async function transfer(op, from, to) {
  try {
    await api('POST', '/api/x/transfer', {op, src: from.src, paths: from.paths, dst: to.src, dstDir: to.dir, conflict: X.conflict});
    toast((op === 'copy' ? 'Копирование' : 'Перемещение') + ' запущено', 'info'); X.dockOpen = true; pollTasks();
  } catch (e) { toast(e.message); }
}
async function bulk(op, i) {
  const s = selected(i); if (!s.length) return toast('Ничего не выбрано', 'warning');
  const p = pane(i); let to;
  if (X.layout === '1') { to = await pickFolder(op === 'copy' ? 'Копировать в…' : 'Переместить в…', p.src, p.path); if (!to) return; }
  else { const q = pane(other(i)); to = {src: q.src, dir: q.path}; if (q.err) return toast('Соседняя панель недоступна', 'warning'); }
  if (op === 'move' && p.src === to.src && to.dir === p.path) return toast('Источник и назначение совпадают', 'warning');
  transfer(op, {src: p.src, paths: s.map(e => e.path)}, to);
}
async function publish(i, e) {
  const p = pane(i);
  try {
    const r = await api('POST', '/api/x/publish', {src: p.src, path: e.path});
    const o = modal(`<h3>Публичная ссылка</h3><p class="muted small">Любой, у кого есть ссылка, сможет открыть «${esc(e.name)}».</p>
      <div class="fi"><input type="text" id="pub-url" readonly value="${esc(r.url)}"></div>
      <div class="foot"><button class="btn danger-o" type="button" id="pub-off">Закрыть доступ</button><span class="grow"></span><button class="btn" type="button" id="pub-copy">Скопировать</button><button class="btn primary" type="button" data-act="closemodal">Готово</button></div>`);
    const inp = o.querySelector('#pub-url'); inp.select();
    o.querySelector('#pub-copy').onclick = async () => { try { await navigator.clipboard.writeText(r.url); toast('Ссылка скопирована', 'success'); } catch (er) { inp.select(); document.execCommand && document.execCommand('copy'); toast('Ссылка выделена — нажмите Ctrl+C', 'info'); } };
    o.querySelector('#pub-off').onclick = async () => { try { await api('POST', '/api/x/publish', {src: p.src, path: e.path, unpublish: true}); C.closeModals(); toast('Доступ закрыт', 'success'); } catch (er) { toast(er.message); } };
  } catch (er) { toast(er.message); }
}

async function props(i) {
  const s = selected(i), p = pane(i);
  const list = s.length ? s : [{name: baseName(p.path), path: p.path, isDir: true, size: 0, mtime: 0, root: true}];
  if (list.length > 1) {
    const files = list.filter(e => !e.isDir);
    return void modal(`<h3>Выбрано объектов: ${list.length}</h3><dl class="props"><dt>Папок</dt><dd>${list.length - files.length}</dd><dt>Файлов</dt><dd>${files.length}</dd><dt>Размер файлов</dt><dd>${fmtBytes(files.reduce((a, e) => a + e.size, 0))}</dd><dt>Расположение</dt><dd>${esc(srcName(p.src))}: ${esc(p.path)}</dd></dl>
      <div class="foot"><button class="btn primary" data-xm="close">Закрыть</button></div>`, 'narrow').addEventListener('click', ev => { if (ev.target.closest('[data-xm]') || ev.target.classList.contains('overlay')) ev.currentTarget.remove(); });
  }
  const e = list[0];
  let info = {}; try { info = await api('GET', `/api/x/info?src=${enc(p.src)}&path=${enc(e.path)}`); } catch (er) { info = {entry: e, mime: ''}; }
  const en = info.entry || e, img = !en.isDir && /\.(jpe?g|png|gif|webp|bmp|avif)$/i.test(en.name) && en.size < 2e7;
  const o = modal(`<div class="pr-h">${fileIcon(en)}<div><h3 title="${esc(en.name)}">${esc(en.name)}</h3><div class="muted small">${esc(typeName(en))}</div></div></div>
    ${img ? `<div class="pr-img"><img alt="" src="${esc(urlFor(p.src, en.path))}"></div>` : ''}
    <dl class="props"><dt>Расположение</dt><dd>${esc(srcName(p.src))}: ${esc(parent(en.path))}</dd>
      <dt>Размер</dt><dd id="pr-size">${en.isDir ? '<button class="btn sm" data-xm="deep">Посчитать размер папки</button>' : fmtBytes(en.size) + (en.size >= 1024 ? ` <span class="muted">(${en.size.toLocaleString('ru-RU')} байт)</span>` : '')}</dd>
      <dt>Изменён</dt><dd>${en.mtime ? fmtDate(en.mtime * 1000) : '—'}</dd>${info.mime && !en.isDir ? `<dt>MIME-тип</dt><dd>${esc(info.mime)}</dd>` : ''}${en.md5 ? `<dt>MD5</dt><dd><code>${esc(en.md5)}</code></dd>` : ''}
      <dt>Полный путь</dt><dd><code>${esc(en.path)}</code></dd></dl>
    <div class="foot">${!en.isDir ? '<button class="btn" data-xm="dl">' + I.download + ' Скачать</button>' : ''}<button class="btn primary" data-xm="close">Закрыть</button></div>`, 'narrow');
  o.addEventListener('click', async ev => {
    const t = ev.target.closest('[data-xm]');
    if (ev.target === o || (t && t.dataset.xm === 'close')) return o.remove();
    if (!t) return;
    if (t.dataset.xm === 'dl') download(p.src, [en]);
    if (t.dataset.xm === 'deep') {
      $('#pr-size', o).innerHTML = '<span class="spin sm"></span> считаю…';
      try { const r = await api('GET', `/api/x/info?src=${enc(p.src)}&path=${enc(en.path)}&deep=1`), s = r.stats;
        $('#pr-size', o).innerHTML = `${fmtBytes(s.size)}${s.truncated ? ' <span class="muted">(не меньше — папка очень большая)</span>' : ''}<div class="muted small">файлов: ${s.files} · папок: ${s.dirs}</div>`; } catch (er) { $('#pr-size', o).textContent = er.message; }
    }
  });
}

// ---------- выбор папки назначения ----------
function pickFolder(title, src0, path0) {
  return new Promise(res => {
    let src = src0, path = path0 === '/' ? '/' : path0;
    const o = modal(`<h3>${esc(title)}</h3><div class="row"><select id="pf-src">${X.sources.map(s => `<option value="${esc(s.id)}">${s.id === 'local' ? 'NAS' : '☁ ' + esc(s.name)}</option>`).join('')}</select><div class="pf-path small" id="pf-path"></div></div>
      <div class="dirs" id="pf-list"></div><div id="pf-msg"></div><div class="foot"><button class="btn" data-xm="no">Отмена</button><button class="btn primary" data-xm="ok">Выбрать эту папку</button></div>`, 'narrow');
    o._confirm = res; $('#pf-src', o).value = src;
    const draw = async () => {
      $('#pf-path', o).textContent = path; $('#pf-list', o).innerHTML = '<div class="muted nodirs">Загрузка…</div>'; $('#pf-msg', o).innerHTML = '';
      try {
        const d = await api('GET', `/api/x/list?src=${enc(src)}&path=${enc(path)}`);
        $('#pf-list', o).innerHTML = (path !== '/' ? `<div data-pf="${esc(parent(path))}">${IC.up} ..</div>` : '') + d.entries.filter(e => e.isDir).sort((a, b) => a.name.localeCompare(b.name, 'ru'))
          .map(e => `<div data-pf="${esc(e.path)}">${IC.folder} ${esc(e.name)}</div>`).join('') || '<div class="muted nodirs">Подпапок нет</div>';
      } catch (e) { $('#pf-list', o).innerHTML = ''; $('#pf-msg', o).innerHTML = alertBox('error', e.message); }
    };
    o.addEventListener('click', ev => {
      const t = ev.target.closest('[data-xm]'), d = ev.target.closest('[data-pf]');
      if (d) { path = d.dataset.pf; draw(); } else if (ev.target === o || (t && t.dataset.xm === 'no')) { o._confirm = null; o.remove(); res(null); }
      else if (t && t.dataset.xm === 'ok') { o._confirm = null; o.remove(); res({src, dir: path}); }
    });
    $('#pf-src', o).addEventListener('change', e => { src = e.target.value; path = '/'; draw(); });
    draw();
  });
}

// ---------- загрузка файлов с компьютера ----------
function enqueueUploads(items, src, dir) {
  if (!items.length) return;
  items.forEach(it => X.uploads.push({id: ++upSeq, name: it.file.name, rel: it.rel, file: it.file, src, dir, loaded: 0, total: it.file.size, status: 'queued', err: ''}));
  X.dockOpen = true; paintDock(); pumpUploads();
}
let upRunning = 0;
async function pumpUploads() {
  await C.ensureToken();
  while (upRunning < 2) {
    const u = X.uploads.find(x => x.status === 'queued'); if (!u) break;
    u.status = 'running'; upRunning++;
    sendOne(u).then(() => {
      upRunning--; paintDock();
      if (!X.uploads.some(x => x.status === 'queued' || x.status === 'running')) {
        const done = X.uploads.filter(x => x.status === 'done').length, bad = X.uploads.filter(x => x.status === 'error').length;
        if (done) toast(`Загружено файлов: ${done}${bad ? ', с ошибками: ' + bad : ''}`, bad ? 'warning' : 'success');
        X.panes.forEach((p, i) => { if (X.uploads.some(x => x.src === p.src && (x.dir === p.path || (x.dir + '/').startsWith(p.path === '/' ? '/' : p.path + '/')))) refresh(i); });
      }
      pumpUploads();
    });
  }
}
function sendOne(u) {
  return new Promise(res => {
    const dir = u.rel ? join(u.dir, u.rel).replace(/\/+/g, '/') : u.dir, x = new XMLHttpRequest(); u.xhr = x;
    x.open('PUT', C.U(`/api/x/file?src=${enc(u.src)}&path=${enc(dir)}&name=${enc(u.name)}&conflict=${X.conflict}`));
    x.setRequestHeader('X-Requested-With', 'cloudbackup'); x.setRequestHeader('Content-Type', 'application/octet-stream');
    if (C.token) x.setRequestHeader('X-SYNO-TOKEN', C.token);
    let last = 0, lt = performance.now();
    x.upload.onprogress = e => { u.loaded = e.loaded; u.total = e.total; const now = performance.now(); if (now - lt > 500) { u.speed = (e.loaded - last) / ((now - lt) / 1000); last = e.loaded; lt = now; paintDock(true); } };
    x.onload = () => { if (x.status === 200) { u.status = 'done'; u.loaded = u.total; } else { u.status = 'error'; try { u.err = JSON.parse(x.responseText).error; } catch (e) { u.err = 'Ошибка ' + x.status; } } res(); };
    x.onerror = () => { u.status = 'error'; u.err = 'нет связи с сервисом'; res(); };
    x.onabort = () => { u.status = 'canceled'; res(); };
    x.send(u.file);
  });
}
async function entriesFromDrop(dt) {
  const ents = [...(dt.items || [])].map(i => (i.webkitGetAsEntry ? i.webkitGetAsEntry() : null)).filter(Boolean);
  if (!ents.length) return [...(dt.files || [])].map(file => ({file, rel: ''}));
  const out = [];
  const walk = async (en, rel) => {
    if (en.isFile) { out.push({file: await new Promise((ok, no) => en.file(ok, no)), rel}); return; }
    const child = rel ? rel + '/' + en.name : en.name, rd = en.createReader(); let all = [];
    for (;;) { const b = await new Promise((ok, no) => rd.readEntries(ok, no)); if (!b.length) break; all = all.concat(b); }
    if (!all.length) return;
    for (const c of all) await walk(c, child);
  };
  for (const en of ents) await walk(en, '');
  return out;
}
function pickFiles(i) {
  const inp = document.createElement('input'); inp.type = 'file'; inp.multiple = true;
  inp.onchange = () => { const p = pane(i); enqueueUploads([...inp.files].map(file => ({file, rel: ''})), p.src, p.path); };
  inp.click();
}

// ---------- очередь операций ----------
const fmtSpeed = v => v > 0 ? fmtBytes(v) + '/с' : '';
async function pollTasks() {
  try {
    const d = await api('GET', '/api/x/tasks'), first = !X.handled; X.tasks = d.tasks;
    if (first) X.handled = new Set();
    const now = performance.now();
    d.tasks.forEach(t => {
      const o = X.speeds[t.id];
      if (t.status === 'running') { if (o && now - o.t > 800) { X.speeds[t.id] = {t: now, d: t.done, v: Math.max(0, (t.done - o.d) / ((now - o.t) / 1000))}; } else if (!o) X.speeds[t.id] = {t: now, d: t.done, v: 0}; }
      if (!t.finished || X.handled.has(t.id)) return;
      X.handled.add(t.id);
      if (first) return; // завершённые до открытия страницы — без уведомлений
      const head = (t.op === 'copy' ? 'Копирование' : 'Перемещение');
      if (t.status === 'done') toast(`${head} завершено: ${t.names.length > 1 ? t.names.length + ' объектов' : t.names[0]}`, 'success');
      else if (t.status === 'error') toast(`${head}: ошибки (${t.errors.length}). ${t.errors[0] || ''}`, 'error');
      X.panes.forEach((p, i) => { if ((p.src === t.dst && p.path === t.dstDir) || (t.op === 'move' && p.src === t.src)) refresh(i); });
    });
  } catch (e) {}
  paintDock();
}
const progressBar = (done, total, indet) => `<div class="meter prog ${indet ? 'indet' : ''}"><i style="width:${indet ? 40 : total ? Math.min(100, done / total * 100).toFixed(1) : 0}%"></i></div>`;
function paintDock(light) {
  const el = $('#xp-dock'); if (!el) return;
  const act = X.tasks.filter(t => !t.finished).length + X.uploads.filter(u => u.status === 'queued' || u.status === 'running').length;
  const total = X.tasks.length + X.uploads.length;
  if (!total) { el.innerHTML = ''; el.classList.remove('has'); return; }
  el.classList.add('has');
  const rows = [
    ...X.uploads.slice().reverse().map(u => ({k: 'u' + u.id, icon: I.upload, title: `Загрузка: ${esc(u.rel ? u.rel + '/' : '')}${esc(u.name)}`, sub: `в ${esc(srcName(u.src))}: ${esc(u.dir)}`,
      status: u.status, err: u.err, bar: progressBar(u.loaded, u.total), meta: u.status === 'running' ? `${fmtBytes(u.loaded)} из ${fmtBytes(u.total)} ${fmtSpeed(u.speed)}` : '', cancel: u.status === 'running' || u.status === 'queued' ? `data-xa="cancelup" data-id="${u.id}"` : ''})),
    ...X.tasks.map(t => {
      const sp = X.speeds[t.id], run = !t.finished;
      return {k: 't' + t.id, icon: t.op === 'copy' ? I.copy : I.move, title: `${t.op === 'copy' ? 'Копирование' : 'Перемещение'}: ${esc(t.names.length > 1 ? t.names.length + ' объектов' : t.names[0])}`,
        sub: `${esc(srcName(t.src))} → ${esc(srcName(t.dst))}: ${esc(t.dstDir)}${run && t.current ? ' · ' + esc(t.current) : ''}`, status: t.status, err: (t.errors || []).join('\n'),
        bar: progressBar(t.done, t.total, run && !t.total), meta: run ? (t.total ? `${fmtBytes(t.done)} из ${fmtBytes(t.total)} · файлов ${t.filesDone}/${t.files} ${sp ? fmtSpeed(sp.v) : ''}` : t.status === 'scanning' ? 'подготовка…' : `объектов ${t.filesDone}/${t.files}`) : (t.files ? `файлов: ${t.filesDone}/${t.files}` : ''),
        cancel: run ? `data-xa="canceltask" data-id="${t.id}"` : ''};
    })];
  const stTag = s => ({done: '<span class="tag ok"><i class="dot"></i>готово</span>', error: '<span class="tag err"><i class="dot"></i>ошибки</span>', canceled: '<span class="tag muted">отменено</span>', queued: '<span class="tag muted">в очереди</span>'}[s] || '');
  el.innerHTML = `<div class="dk-h" data-xa="dock"><b>Операции</b><span class="tag ${act ? 'run' : 'muted'}">${act ? '<i class="dot"></i>активных: ' + act : 'нет активных'}</span><span class="grow"></span>
      <button class="btn sm" data-xa="clearops">Очистить завершённые</button><span class="dk-t">${X.dockOpen ? '▾' : '▸'}</span></div>
    ${X.dockOpen ? `<div class="dk-b">${rows.slice(0, 30).map(r => `<div class="dk-r"><span class="dk-i">${r.icon}</span><div class="dk-m"><div class="dk-t1">${r.title} ${stTag(r.status)}</div><div class="small muted dk-s">${r.sub}</div>
      ${['done', 'error', 'canceled'].includes(r.status) && r.status !== 'error' ? '' : r.bar}${r.meta ? `<div class="small muted">${r.meta}</div>` : ''}${r.err ? `<div class="small err-text dk-e">${esc(r.err)}</div>` : ''}</div>
      ${r.cancel ? `<button class="btn sm icon danger-o" ${r.cancel} data-tip="Отменить">${I.close}</button>` : ''}</div>`).join('')}</div>` : ''}`;
}

// ---------- меню ----------
let menuEl = null;
function closeMenu() { if (menuEl) { menuEl.remove(); menuEl = null; } }
function showMenu(x, y, items) {
  closeMenu();
  menuEl = document.createElement('div'); menuEl.className = 'xp-menu';
  menuEl.innerHTML = items.map(it => it === '-' ? '<hr>' : `<button data-m="${it.a}" ${it.off ? 'disabled' : ''} class="${it.danger ? 'danger' : ''}">${it.ic || ''}<span>${esc(it.t)}</span><kbd>${it.k || ''}</kbd></button>`).join('');
  document.body.appendChild(menuEl);
  const r = menuEl.getBoundingClientRect();
  menuEl.style.left = Math.max(6, Math.min(x, innerWidth - r.width - 6)) + 'px'; menuEl.style.top = Math.max(6, Math.min(y, innerHeight - r.height - 6)) + 'px';
  menuEl.addEventListener('click', e => { const b = e.target.closest('[data-m]'); if (!b || b.disabled) return; const a = b.dataset.m; closeMenu(); items.find(i => i.a === a).run(); });
}
function contextMenu(i, e, rowPath) {
  X.active = i; const p = pane(i);
  if (rowPath && !p.sel.has(rowPath)) selectOnly(i, rowPath); markActive(); paintFoot(i);
  const s = selected(i), one = s.length === 1, two = X.layout !== '1';
  const srcInfo = X.sources.find(x => x.id === p.src) || {};
  const items = s.length ? [
    {a: 'open', t: one && !s[0].isDir ? 'Открыть / скачать' : 'Открыть', ic: I.open, off: !one, run: () => openEntry(i, s[0]), k: 'Enter'},
    {a: 'dl', t: 'Скачать', ic: I.download, off: !s.some(x => !x.isDir), run: () => download(p.src, s)}, '-',
    {a: 'copy', t: two ? 'Копировать в другую панель' : 'Копировать в…', ic: I.copy, run: () => bulk('copy', i), k: 'F5'},
    {a: 'move', t: two ? 'Переместить в другую панель' : 'Переместить в…', ic: I.move, run: () => bulk('move', i), k: 'F6'}, '-',
    {a: 'ren', t: 'Переименовать', ic: I.rename, off: !one, run: () => rename(i), k: 'F2'},
    ...((X.sources.find(x => x.id === p.src) || {}).canPublish && one ? [{a: 'pub', t: 'Публичная ссылка…', ic: I.open, run: () => publish(i, s[0])}] : []),
    {a: 'info', t: 'Свойства', ic: I.info, run: () => props(i), k: 'Alt+Enter'},
    ...(srcInfo.canTrash ? [{a: 'trash', t: 'В корзину', ic: I.trash, run: () => del(i, true)}] : []),
    {a: 'del', t: srcInfo.canTrash ? 'Удалить безвозвратно' : 'Удалить', ic: I.trash, danger: true, run: () => del(i), k: 'Del'}] : [
    {a: 'mk', t: 'Новая папка', ic: I.newfolder, run: () => mkdir(i), k: 'F7'},
    {a: 'up', t: 'Загрузить файлы…', ic: I.upload, run: () => pickFiles(i)}, '-',
    {a: 'rf', t: 'Обновить', ic: I.refresh, run: () => refresh(i)},
    {a: 'info', t: 'Свойства папки', ic: I.info, run: () => props(i)}];
  showMenu(e.clientX, e.clientY, items);
}

// ---------- события ----------
function bindRoot() {
  const R = root;
  R.addEventListener('click', e => {
    closeMenu();
    const pe = e.target.closest('.xp-pane'), i = pe ? +pe.dataset.i : X.active;
    if (pe && X.active !== i) { X.active = i; markActive(); }
    const t = e.target.closest('[data-xa]');
    const row = e.target.closest('.xr');
    if (row && !t) { clickRow(i, row.dataset.p, e); return; }
    if (!t) { if (pe && e.target.closest('.xp-body') && !e.target.closest('.xr')) { pane(i).sel = new Set(); pane(i).cur = null; paintSel(i); paintFoot(i); } return; }
    const a = t.dataset.xa;
    if (a === 'addredit' && e.target === t) return editAddr(i);
    const p = pane(i);
    ({
      back: () => { if (p.hi > 0) { p.hi--; load(i, p.hist[p.hi]); } }, fwd: () => { if (p.hi < p.hist.length - 1) { p.hi++; load(i, p.hist[p.hi]); } },
      upDir: () => go(i, parent(p.path)), refresh: () => refresh(i), crumb: () => go(i, t.dataset.path),
      view: () => { p.view = t.dataset.v; paintHead(i); paintBody(i); save(); },
      sort: () => { p.dir = p.sort === t.dataset.k ? -p.dir : 1; p.sort = t.dataset.k; paintBody(i); save(); },
      emptytrash: () => emptyTrash(X.active), mkdir: () => mkdir(X.active), upload: () => pickFiles(X.active), download: () => { const s = selected(X.active); s.length ? download(pane(X.active).src, s) : toast('Ничего не выбрано', 'warning'); },
      copy: () => bulk('copy', X.active), move: () => bulk('move', X.active), rename: () => rename(X.active), info: () => props(X.active), delete: () => del(X.active),
      layout: () => { X.layout = t.dataset.v; save(); const pn = $('.xp-panes', R); pn.className = 'xp-panes l-' + X.layout; paintBar(); if (X.layout !== '1' && !pane(1).entries.length) load(1, pane(1).path, {push: true}); },
      swap: () => { X.panes.reverse(); X.panes.forEach((_, k) => { paintHead(k); paintBody(k); $('[data-filter]', paneEl(k)).value = pane(k).filter; }); save(); },
      dock: () => { if (e.target.closest('button')) return; X.dockOpen = !X.dockOpen; paintDock(); },
      clearops: async () => { await api('POST', '/api/x/tasks/clear', {}); X.uploads = X.uploads.filter(u => u.status === 'queued' || u.status === 'running'); pollTasks(); },
      canceltask: () => api('POST', `/api/x/tasks/${t.dataset.id}/cancel`, {}).then(pollTasks),
      cancelup: () => { const u = X.uploads.find(x => x.id === +t.dataset.id); if (u) { if (u.xhr && u.status === 'running') u.xhr.abort(); else u.status = 'canceled'; paintDock(); } },
    }[a] || (() => {}))();
  });
  R.addEventListener('dblclick', e => { const row = e.target.closest('.xr'); if (!row) return; const i = +row.closest('.xp-pane').dataset.i, en = pane(i).entries.find(x => x.path === row.dataset.p); if (en) openEntry(i, en); });
  R.addEventListener('contextmenu', e => { const pe = e.target.closest('.xp-body'); if (!pe) return; e.preventDefault(); const row = e.target.closest('.xr'); contextMenu(+pe.parentNode.dataset.i, e, row && row.dataset.p); });
  R.addEventListener('change', e => {
    if (e.target.id === 'xp-conflict') { X.conflict = e.target.value; save(); return; }
    if (e.target.classList.contains('xp-src')) { const i = +e.target.closest('.xp-pane').dataset.i, p = pane(i); p.src = e.target.value; p.hist = []; p.hi = -1; p.path = '/'; p.ok = false; X.active = i; load(i, '/', {push: true}); }
  });
  R.addEventListener('input', e => { if (e.target.matches('[data-filter]')) { const i = +e.target.closest('.xp-pane').dataset.i; pane(i).filter = e.target.value; pane(i).limit = 300; paintBody(i); } });
  R.addEventListener('scroll', e => {
    const b = e.target; if (!b.classList || !b.classList.contains('xp-body')) return; const i = +b.parentNode.dataset.i, p = pane(i);
    if (b.scrollTop + b.clientHeight > b.scrollHeight - 300 && visible(i).length > p.limit) { p.limit += 300; paintBody(i, true); }
  }, true);
  // перетаскивание
  let dragData = null;
  R.addEventListener('dragstart', e => {
    const row = e.target.closest('.xr'); if (!row) return; const i = +row.closest('.xp-pane').dataset.i, p = pane(i);
    if (!p.sel.has(row.dataset.p)) { selectOnly(i, row.dataset.p); paintSel(i); paintFoot(i); }
    dragData = {src: p.src, paths: [...p.sel], from: i};
    e.dataTransfer.setData('application/x-cbx', JSON.stringify(dragData)); e.dataTransfer.setData('text/plain', [...p.sel].map(baseName).join('\n')); e.dataTransfer.effectAllowed = 'copyMove';
    const g = document.createElement('div'); g.className = 'drag-ghost'; g.textContent = p.sel.size > 1 ? `${p.sel.size} объектов` : baseName(row.dataset.p); document.body.appendChild(g);
    e.dataTransfer.setDragImage(g, -8, -8); setTimeout(() => g.remove(), 0);
    R.classList.add('dragging');
  });
  const clearDrop = () => R.querySelectorAll('.drop, .drop-pane').forEach(n => n.classList.remove('drop', 'drop-pane'));
  R.addEventListener('dragend', () => { dragData = null; R.classList.remove('dragging'); clearDrop(); });
  R.addEventListener('dragover', e => {
    const pe = e.target.closest('.xp-pane'); if (!pe) return;
    const types = [...e.dataTransfer.types]; if (!types.includes('application/x-cbx') && !types.includes('Files')) return;
    e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; clearDrop();
    const row = e.target.closest('.xr[data-d="1"]');
    if (row && !(dragData && dragData.paths.includes(row.dataset.p) && dragData.src === pane(+pe.dataset.i).src)) row.classList.add('drop'); else pe.classList.add('drop-pane');
  });
  R.addEventListener('dragleave', e => { if (!R.contains(e.relatedTarget)) clearDrop(); });
  R.addEventListener('drop', async e => {
    const pe = e.target.closest('.xp-pane'); if (!pe) return; e.preventDefault(); clearDrop(); R.classList.remove('dragging');
    const i = +pe.dataset.i, p = pane(i), row = e.target.closest('.xr[data-d="1"]');
    const dir = row ? row.dataset.p : p.path;
    const raw = e.dataTransfer.getData('application/x-cbx');
    if (raw) {
      const d = JSON.parse(raw);
      if (d.src === p.src && d.paths.every(x => parent(x) === dir) ) return;
      if (d.paths.some(x => d.src === p.src && (dir === x || dir.startsWith(x + '/')))) return toast('Нельзя перенести папку внутрь самой себя', 'warning');
      const where = `${srcName(p.src)}: ${dir}`;
      showMenu(e.clientX, e.clientY, [{a: 'cp', t: 'Копировать сюда', ic: I.copy, run: () => transfer('copy', d, {src: p.src, dir})}, {a: 'mv', t: 'Переместить сюда', ic: I.move, run: () => transfer('move', d, {src: p.src, dir})}, '-', {a: 'no', t: 'Отмена', ic: I.close, run: () => {}}]);
      menuEl.insertAdjacentHTML('afterbegin', `<div class="mh">${esc(where)}</div>`); return;
    }
    if (e.dataTransfer.types.includes('Files')) { const items = await entriesFromDrop(e.dataTransfer); enqueueUploads(items, p.src, dir); }
  });
  // фокус на панели
  R.addEventListener('focusin', e => { const pe = e.target.closest('.xp-pane'); if (pe && X.active !== +pe.dataset.i) { X.active = +pe.dataset.i; markActive(); } });
}
function editAddr(i) {
  const el = $('.xp-addr', paneEl(i)), p = pane(i);
  el.innerHTML = `<input type="text" class="addr-in" value="${esc(p.path)}" spellcheck="false" aria-label="Путь">`;
  const inp = $('input', el); inp.focus(); inp.select();
  const done = ok => { const v = inp.value.trim(); paintHead(i); if (ok && v && v !== p.path) go(i, v.startsWith('/') ? v : '/' + v); };
  inp.addEventListener('keydown', e => { if (e.key === 'Enter') done(true); if (e.key === 'Escape') done(false); e.stopPropagation(); });
  inp.addEventListener('blur', () => done(false));
}
function bindGlobal() {
  if (bound) return; bound = true;
  document.addEventListener('click', e => { if (menuEl && !menuEl.contains(e.target)) closeMenu(); });
  addEventListener('blur', closeMenu);
  document.addEventListener('keydown', e => {
    if (C.view !== 'explorer' || !root || !document.body.contains(root)) return;
    if (e.key === 'Escape') { closeMenu(); }
    if (document.querySelector('.overlay')) return;
    const tag = (e.target.tagName || '').toLowerCase(), i = X.active, p = pane(i);
    if (tag === 'input' || tag === 'select' || tag === 'textarea') { if (e.key === 'Escape' && e.target.matches('[data-filter]')) { e.target.value = ''; p.filter = ''; paintBody(i); } return; }
    if (!e.target.closest('.xp')) return;
    const k = e.key, cols = gridCols(i), list = visible(i);
    const stop = () => { e.preventDefault(); e.stopPropagation(); };
    if (e.altKey && k === 'ArrowLeft') { stop(); return $(`.xp-pane[data-i="${i}"] [data-xa=back]`, root).click(); }
    if (e.altKey && k === 'ArrowRight') { stop(); return $(`.xp-pane[data-i="${i}"] [data-xa=fwd]`, root).click(); }
    if (e.altKey && k === 'Enter') { stop(); return props(i); }
    if (k === 'ArrowDown') { stop(); moveCursor(i, cols, e.shiftKey); } else if (k === 'ArrowUp') { stop(); moveCursor(i, -cols, e.shiftKey); }
    else if (k === 'ArrowRight' && cols > 1) { stop(); moveCursor(i, 1, e.shiftKey); } else if (k === 'ArrowLeft' && cols > 1) { stop(); moveCursor(i, -1, e.shiftKey); }
    else if (k === 'Home') { stop(); moveCursor(i, -1e9, e.shiftKey); } else if (k === 'End') { stop(); moveCursor(i, 1e9, e.shiftKey); }
    else if (k === 'PageDown') { stop(); moveCursor(i, 12 * cols, e.shiftKey); } else if (k === 'PageUp') { stop(); moveCursor(i, -12 * cols, e.shiftKey); }
    else if (k === 'Enter') { stop(); const s = selected(i); if (s.length === 1) openEntry(i, s[0]); }
    else if (k === 'Backspace') { stop(); if (p.path !== '/') go(i, parent(p.path)); }
    else if (k === 'F2') { stop(); rename(i); } else if (k === 'F5') { stop(); bulk('copy', i); } else if (k === 'F6') { stop(); bulk('move', i); }
    else if (k === 'F7') { stop(); mkdir(i); } else if (k === 'F8' || k === 'Delete') { stop(); del(i); }
    else if ((e.ctrlKey || e.metaKey) && k.toLowerCase() === 'a') { stop(); p.sel = new Set(list.map(x => x.path)); paintSel(i); paintFoot(i); }
    else if ((e.ctrlKey || e.metaKey) && k.toLowerCase() === 'f') { stop(); $('[data-filter]', paneEl(i)).focus(); }
    else if (k === 'Tab' && X.layout !== '1') { stop(); X.active = 1 - i; markActive(); $('.xp-body', paneEl(X.active)).focus(); }
    else if (k === 'Escape') { p.sel = new Set(); paintSel(i); paintFoot(i); }
    else if (k.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) { // быстрый поиск по первым буквам
      const t = Date.now(); typeBuf = (t - typeAt > 800 ? '' : typeBuf) + k.toLowerCase(); typeAt = t;
      const hit = list.find(x => x.name.toLowerCase().startsWith(typeBuf)); if (hit) { selectOnly(i, hit.path); paintSel(i); paintFoot(i); const r = paneEl(i).querySelector(`.xr[data-p="${CSS.escape(hit.path)}"]`); if (r) r.scrollIntoView({block: 'nearest'}); }
    }
  });
  setInterval(() => {
    if (C.view !== 'explorer' || document.hidden || !root || !document.body.contains(root)) return;
    tick++; const active = X.tasks.some(t => !t.finished);
    if (active || tick % 4 === 0) pollTasks();
  }, 1000);
}

window.CBX = {view};
})();
