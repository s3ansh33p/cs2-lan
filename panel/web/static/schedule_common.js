// ── Shared Schedule Grid ──
// Used by both admin calendar and public schedule view.

var SLOT_H = 30;       // px per 30-min slot
var SLOT_MIN = 30;
var GUTTER_W = 72;     // px for time labels
var GRID_PAD = 1;      // extra slots breathing room top and bottom
var ITEM_GAP = 3;      // px vertical gap between stacked items
var COLORS = {
    green:  { bg: '#2d6a4f', border: '#40916c', text: '#d8f3dc' },
    yellow: { bg: '#b45309', border: '#d97706', text: '#fef3c7' },
    red:    { bg: '#991b1b', border: '#dc2626', text: '#fecaca' },
    pink:   { bg: '#9d174d', border: '#db2777', text: '#fce7f3' },
    blue:   { bg: '#1e40af', border: '#3b82f6', text: '#dbeafe' },
    grey:   { bg: '#475569', border: '#64748b', text: '#cbd5e1' },
    dark:   { bg: '#334155', border: '#475569', text: '#cbd5e1' },
    purple: { bg: '#6b21a8', border: '#9333ea', text: '#e9d5ff' },
    teal:   { bg: '#115e59', border: '#14b8a6', text: '#ccfbf1' },
    orange: { bg: '#c2410c', border: '#f97316', text: '#ffedd5' }
};
var COLOR_KEYS = ['blue','green','yellow','orange','red','pink','purple','teal','grey','dark'];

// ── Time utilities ──

function parsedt(s) {
    if (!s) return 0;
    var p = s.split('T'); if (p.length < 2) return 0;
    var d = p[0].split('-'), t = p[1].split(':');
    return new Date(+d[0], +d[1] - 1, +d[2], +t[0], +t[1] || 0).getTime();
}
function ms2slot(ms, start) { return (ms - start) / (SLOT_MIN * 60000); }
function slot2ms(slot, start) { return start + slot * SLOT_MIN * 60000; }
function slot2y(slot) { return slot * SLOT_H; }
function y2slot(y, total) { return Math.max(0, Math.min(total, Math.round(y / SLOT_H))); }

function fmt12(ms) {
    var d = new Date(ms), h = d.getHours(), m = d.getMinutes();
    var s = h >= 12 ? 'pm' : 'am', h12 = h % 12 || 12;
    return m === 0 ? h12 + s : h12 + ':' + (m < 10 ? '0' : '') + m + s;
}
function fmtday(ms) {
    var d = new Date(ms);
    return ['Sun','Mon','Tue','Wed','Thu','Fri','Sat'][d.getDay()];
}
function ms2iso(ms) {
    var d = new Date(ms), p = function(n) { return n < 10 ? '0' + n : '' + n; };
    return d.getFullYear() + '-' + p(d.getMonth()+1) + '-' + p(d.getDate()) + 'T' + p(d.getHours()) + ':' + p(d.getMinutes());
}

// ── Overlap layout ──

function layoutOverlaps(items) {
    if (!items.length) return;
    var ends = [];
    for (var i = 0; i < items.length; i++) {
        var it = items[i], placed = false;
        for (var l = 0; l < ends.length; l++) {
            if (ends[l] <= it._s) { it._lane = l; ends[l] = it._e; placed = true; break; }
        }
        if (!placed) { it._lane = ends.length; ends.push(it._e); }
    }
    var gs = 0, ge = items[0]._e, gm = items[0]._lane;
    for (var i = 1; i <= items.length; i++) {
        var s = (i < items.length) ? items[i]._s : Infinity;
        if (s >= ge) {
            var t = gm + 1;
            for (var j = gs; j < i; j++) items[j]._lanes = t;
            gs = i;
            if (i < items.length) { ge = items[i]._e; gm = items[i]._lane; }
        } else if (i < items.length) {
            if (items[i]._e > ge) ge = items[i]._e;
            if (items[i]._lane > gm) gm = items[i]._lane;
        }
    }
}

// ── Grid + Item rendering ──
// opts: { interactive }
// Returns { layer, scroll, totalSlots }

function renderGrid(containerId, rawItems, startMs, endMs, opts) {
    var container = document.getElementById(containerId);
    if (!container) return null;
    opts = opts || {};

    var totalSlots = Math.ceil((endMs - startMs) / (SLOT_MIN * 60000));
    if (totalSlots <= 0) return null;

    var visSlots = totalSlots + GRID_PAD * 2;
    var totalH = visSlots * SLOT_H;
    var cursor = opts.interactive ? ' cursor-crosshair' : '';
    var html = '<div class="relative overflow-y-auto" style="max-height:75vh" id="' + containerId + '-scroll">';
    html += '<div class="relative" style="height:' + totalH + 'px;margin-left:' + GUTTER_W + 'px">';

    var lastDay = '';
    for (var slot = -GRID_PAD; slot <= totalSlots + GRID_PAD; slot++) {
        var ms = slot2ms(slot, startMs), d = new Date(ms), y = slot2y(slot + GRID_PAD);
        if (d.getMinutes() !== 0) continue;
        html += '<div class="absolute border-t border-slate-600" style="top:' + y + 'px;left:0;right:0">';
        if (slot >= 0 && slot <= totalSlots) {
            var ds = fmtday(ms), dl = '';
            if (ds !== lastDay) { dl = '<span class="block text-xs text-slate-500 leading-tight">' + ds + '</span>'; lastDay = ds; }
            html += '<span class="absolute text-xs text-slate-400 text-right" style="width:' + (GUTTER_W-8) + 'px;left:-' + GUTTER_W + 'px;top:-1px;transform:translateY(-50%)">' + dl + fmt12(ms) + '</span>';
        }
        html += '</div>';
    }

    html += '<div id="' + containerId + '-layer" class="absolute inset-0' + cursor + '"></div>';
    html += '</div></div>';
    container.innerHTML = html;

    var layer = document.getElementById(containerId + '-layer');
    var scroll = document.getElementById(containerId + '-scroll');

    renderItems(layer, rawItems, startMs, opts);

    return { layer: layer, scroll: scroll, totalSlots: totalSlots };
}

// ── Now line ──

var _nowLineInterval = null;

function createNowLine(layer, startMs, endMs) {
    // Clean up any previous now-line interval
    if (_nowLineInterval) { clearInterval(_nowLineInterval); _nowLineInterval = null; }

    var line = document.createElement('div');
    line.className = 'absolute right-0 pointer-events-none';
    line.style.cssText = 'height:2px;background:#f97316;z-index:20;display:none;left:0';
    var dot = document.createElement('div');
    dot.style.cssText = 'position:absolute;left:-4px;top:-4px;width:10px;height:10px;border-radius:50%;background:#f97316';
    line.appendChild(dot);
    layer.parentElement.appendChild(line);

    function update() {
        var now = Date.now();
        if (now < startMs || now > endMs) { line.style.display = 'none'; return; }
        line.style.display = '';
        line.style.top = slot2y(ms2slot(now, startMs) + GRID_PAD) + 'px';
    }
    update();

    // Sync to nearest :00 or :30 seconds
    var s = new Date(), sec = s.getSeconds(), ms = s.getMilliseconds();
    var toNext = ((sec < 30 ? 30 : 60) - sec) * 1000 - ms;
    setTimeout(function() {
        update();
        _nowLineInterval = setInterval(update, 30000);
    }, toNext);

    return { update: update, el: line };
}

function renderItems(layer, rawItems, startMs, opts) {
    if (!layer) return;
    opts = opts || {};

    var prepared = [];
    for (var i = 0; i < rawItems.length; i++) {
        var it = rawItems[i];
        var s = parsedt(it.start_at), e = it.end_at ? parsedt(it.end_at) : s + SLOT_MIN * 60000;
        prepared.push({ id: it.id, _s: ms2slot(s, startMs), _e: ms2slot(e, startMs), _sMs: s, _eMs: e,
            title: it.title, desc: it.description || '', color: it.color, hasEnd: !!it.end_at });
    }
    prepared.sort(function(a, b) { return a._s - b._s; });
    if (prepared.length) layoutOverlaps(prepared);

    var html = '';
    for (var i = 0; i < prepared.length; i++) {
        var p = prepared[i];
        var top = slot2y(p._s + GRID_PAD), h = Math.max(SLOT_H, slot2y(p._e + GRID_PAD) - top) - ITEM_GAP;
        var left = (p._lane / (p._lanes || 1) * 100), w = (100 / (p._lanes || 1));
        var c = COLORS[p.color] || COLORS.blue;

        html += '<div class="absolute rounded border overflow-hidden select-none' + (opts.interactive ? '' : ' pointer-events-none') + '"' +
            ' data-id="' + p.id + '"' +
            ' style="top:' + top + 'px;height:' + h + 'px;left:' + left + '%;width:calc(' + w + '% - 4px);' +
            'background:' + c.bg + ';border-color:' + c.border + ';color:' + c.text + '">';

        // Title
        html += '<div class="px-2 py-1 text-xs font-medium truncate">' + p.title + '</div>';

        if (h > SLOT_H) {
            html += '<div class="px-2 text-xs">' + fmt12(p._sMs);
            if (p.hasEnd) html += ' - ' + fmt12(p._eMs);
            html += '</div>';
        }

        if (p.desc) {
            html += '<div class="px-2 pt-2 text-xs pointer-events-auto [&_a]:underline [&_a]:text-orange-300">' + p.desc + '</div>';
        }

        // Resize handle (admin only)
        if (opts.interactive) {
            html += '<div class="absolute bottom-0 left-0 right-0 h-2 cursor-s-resize hover:bg-white/20" data-resize="1"></div>';
        }
        html += '</div>';
    }
    layer.innerHTML = html;
}

// ── WebSocket ──

function connectScheduleWS(onUpdate) {
    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/schedule/ws');
    var retries = 0;
    ws.onmessage = function(e) {
        try {
            retries = 0;
            var d = JSON.parse(e.data);
            if (d.type === 'schedule') onUpdate(d.items || [], d.eventStart, d.eventEnd);
        } catch(err) { console.error('[schedule-ws]', err); }
    };
    ws.onclose = function() {
        var delay = Math.min(5000 * Math.pow(2, retries), 60000);
        retries++;
        setTimeout(function() { connectScheduleWS(onUpdate); }, delay);
    };
}
