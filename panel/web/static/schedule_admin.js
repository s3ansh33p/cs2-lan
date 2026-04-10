// ── Admin Schedule Calendar ──
// Drag-and-drop editor. Depends on schedule_common.js.

var _items = [], _startMs = 0, _endMs = 0, _totalSlots = 0;
var _layer = null, _scroll = null, _drag = null;

// ── Drag state machine ──

function getSlot(e) {
    var r = _layer.getBoundingClientRect();
    return y2slot(e.clientY - r.top - GRID_PAD * SLOT_H, _totalSlots);
}

function attachDrag() {
    _layer.addEventListener('mousedown', function(e) {
        if (e.button !== 0) return;
        var rs = e.target.closest('[data-resize]'), el = e.target.closest('[data-id]');
        if (rs && el) startDrag('resize', e, el);
        else if (el) startDrag('move', e, el);
        else startDrag('create', e, null);
    });
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onEnd);
    // Touch
    _layer.addEventListener('touchstart', function(e) {
        if (e.touches.length !== 1) return;
        var t = e.touches[0], tgt = document.elementFromPoint(t.clientX, t.clientY);
        var fe = { clientY: t.clientY, target: tgt, preventDefault: function(){ e.preventDefault(); }, button: 0 };
        var rs = tgt && tgt.closest('[data-resize]'), el = tgt && tgt.closest('[data-id]');
        if (rs && el) startDrag('resize', fe, el);
        else if (el) startDrag('move', fe, el);
        else startDrag('create', fe, null);
    }, { passive: false });
    document.addEventListener('touchmove', function(e) {
        if (!_drag) return; e.preventDefault();
        onMove({ clientY: e.touches[0].clientY });
    }, { passive: false });
    document.addEventListener('touchend', function() { if (_drag) onEnd(); });
}

function startDrag(mode, e, el) {
    e.preventDefault();
    var slot = getSlot(e);
    _drag = { mode: mode, start: slot, cur: slot, moved: false, el: null, ghost: null };
    if (mode === 'move' && el) {
        _drag.id = +el.getAttribute('data-id');
        _drag.el = el;
        _drag.off = slot - (Math.round(parseInt(el.style.top) / SLOT_H) - GRID_PAD);
        el.style.opacity = '0.5'; el.style.zIndex = '10';
    } else if (mode === 'resize' && el) {
        _drag.id = +el.getAttribute('data-id');
        _drag.el = el; el.style.zIndex = '10';
    } else {
        var g = document.createElement('div');
        g.className = 'absolute left-0 right-0 rounded border border-orange-500/60 pointer-events-none';
        g.style.cssText = 'background:rgba(234,88,12,0.2);top:' + slot2y(slot + GRID_PAD) + 'px;height:' + (SLOT_H - ITEM_GAP) + 'px;z-index:10';
        _layer.appendChild(g); _drag.ghost = g;
    }
}

function onMove(e) {
    if (!_drag) return;
    var slot = getSlot(e);
    if (slot !== _drag.cur) _drag.moved = true;
    _drag.cur = slot;
    if (_drag.mode === 'create' && _drag.ghost) {
        var t = Math.min(_drag.start, slot), b = Math.max(_drag.start, slot);
        if (b - t < 1) b = t + 1;
        _drag.ghost.style.top = slot2y(t + GRID_PAD) + 'px'; _drag.ghost.style.height = (slot2y(b - t) - ITEM_GAP) + 'px';
    } else if (_drag.mode === 'move' && _drag.el) {
        var it = findItem(_drag.id); if (!it) return;
        var sMs = parsedt(it.start_at), eMs = it.end_at ? parsedt(it.end_at) : sMs + SLOT_MIN*60000;
        var dur = ms2slot(eMs, _startMs) - ms2slot(sMs, _startMs);
        var ns = Math.max(0, Math.min(_totalSlots - dur, slot - _drag.off));
        _drag.el.style.top = slot2y(ns + GRID_PAD) + 'px';
    } else if (_drag.mode === 'resize' && _drag.el) {
        var top = parseInt(_drag.el.style.top);
        _drag.el.style.height = Math.max(SLOT_H - ITEM_GAP, slot2y(slot + GRID_PAD) - top - ITEM_GAP) + 'px';
    }
}

function onEnd() {
    if (!_drag) return;
    var d = _drag; _drag = null;
    if (d.mode === 'create') {
        if (d.ghost) d.ghost.remove();
        var s = Math.min(d.start, d.cur), e = Math.max(d.start, d.cur);
        if (e - s < 1) e = s + 1;
        openModal({ mode: 'create', startAt: ms2iso(slot2ms(s, _startMs)), endAt: ms2iso(slot2ms(e, _startMs)), title: '', description: '', color: 'blue' });
    } else if (d.mode === 'move') {
        if (d.el) { d.el.style.opacity = ''; d.el.style.zIndex = ''; }
        if (d.moved) {
            var it = findItem(d.id); if (!it) return;
            var sMs = parsedt(it.start_at), eMs = it.end_at ? parsedt(it.end_at) : sMs + SLOT_MIN*60000;
            var dur = eMs - sMs, durSlots = ms2slot(eMs, _startMs) - ms2slot(sMs, _startMs);
            var ns = Math.max(0, Math.min(_totalSlots - durSlots, d.cur - d.off));
            var nMs = slot2ms(ns, _startMs);
            apiUpdate(d.id, ms2iso(nMs), ms2iso(nMs + dur), it.title, it.description, it.color);
        } else openModal({ mode: 'edit', id: d.id });
    } else if (d.mode === 'resize') {
        if (d.el) d.el.style.zIndex = '';
        if (d.moved) {
            var it = findItem(d.id); if (!it) return;
            var topSlot = Math.round(parseInt(d.el.style.top) / SLOT_H) - GRID_PAD;
            var ne = Math.max(topSlot + 1, d.cur);
            apiUpdate(d.id, it.start_at, ms2iso(slot2ms(ne, _startMs)), it.title, it.description, it.color);
        }
    }
}

// ── Modal ──

function openModal(opts) {
    if (opts.mode === 'edit') {
        var it = findItem(opts.id); if (!it) return;
        opts.startAt = it.start_at; opts.endAt = it.end_at; opts.title = it.title; opts.description = it.description; opts.color = it.color;
    }
    var ex = document.getElementById('sched-modal'); if (ex) ex.remove();
    var isEdit = opts.mode === 'edit';
    var selColor = opts.color || 'blue';

    // Build color picker circles
    var colorPicker = '<div class="flex gap-2 flex-wrap" id="m-colors">';
    for (var i = 0; i < COLOR_KEYS.length; i++) {
        var k = COLOR_KEYS[i], c = COLORS[k];
        var ring = k === selColor ? 'ring-2 ring-white ring-offset-2 ring-offset-slate-800' : 'ring-1 ring-white/20';
        colorPicker += '<button type="button" data-color="' + k + '" class="w-7 h-7 rounded-full cursor-pointer ' + ring + '" style="background:' + c.bg + ';border:2px solid ' + c.border + '" title="' + k + '"></button>';
    }
    colorPicker += '</div>';

    var h = '<div id="sched-modal" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50">' +
        '<div class="bg-slate-800 border border-slate-700 rounded-lg p-6 w-full max-w-md mx-4" onclick="event.stopPropagation()">' +
        '<h3 class="text-lg font-semibold mb-4">' + (isEdit ? 'Edit Item' : 'New Item') + '</h3><div class="space-y-3">' +
        '<div><label class="block text-sm text-slate-400 mb-1">Title</label><input id="m-title" type="text" value="' + opts.title + '" class="w-full bg-slate-700 border border-slate-600 rounded px-3 py-2 text-sm text-white focus:outline-none focus:border-orange-500"></div>' +
        '<div class="grid grid-cols-1 sm:grid-cols-2 gap-3"><div><label class="block text-sm text-slate-400 mb-1">Start</label><input id="m-start" type="datetime-local" value="' + opts.startAt + '" class="w-full bg-slate-700 border border-slate-600 rounded px-3 py-2 text-sm text-white focus:outline-none focus:border-orange-500"></div>' +
        '<div><label class="block text-sm text-slate-400 mb-1">End</label><input id="m-end" type="datetime-local" value="' + opts.endAt + '" class="w-full bg-slate-700 border border-slate-600 rounded px-3 py-2 text-sm text-white focus:outline-none focus:border-orange-500"></div></div>' +
        '<div><label class="block text-sm text-slate-400 mb-1">Description</label>' +
        '<div class="flex gap-1 mb-1">' +
        '<button type="button" onclick="document.execCommand(\'bold\')" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-2 py-0.5 text-xs">Bold</button>' +
        '<button type="button" onclick="document.execCommand(\'italic\')" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-2 py-0.5 text-xs">Italic</button>' +
        '<button type="button" id="m-link-btn" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-2 py-0.5 text-xs">Link</button>' +
        '</div>' +
        '<div id="m-link-row" class="hidden flex gap-1 mb-1">' +
        '<input id="m-link-url" type="text" placeholder="/tournament/5 or https://..." class="flex-1 bg-slate-600 border border-slate-500 rounded px-2 py-1 text-xs text-white focus:outline-none">' +
        '<button type="button" id="m-link-insert" class="bg-orange-600 hover:bg-orange-700 text-white rounded px-2 py-1 text-xs">Insert</button>' +
        '<button type="button" id="m-link-cancel" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-2 py-1 text-xs">Cancel</button>' +
        '</div>' +
        '<div contenteditable="true" id="m-desc" class="w-full bg-slate-700 border border-slate-600 rounded px-3 py-2 text-sm text-white focus:outline-none focus:border-orange-500 min-h-[2.5rem] [&_a]:underline [&_a]:text-orange-300">' + opts.description + '</div></div>' +
        '<div><label class="block text-sm text-slate-400 mb-1">Color</label>' + colorPicker + '</div></div>' +
        '<div class="flex gap-2 mt-4"><button id="m-save" class="bg-orange-600 hover:bg-orange-700 text-white font-medium rounded px-4 py-2 text-sm">Save</button>' +
        '<button id="m-cancel" class="bg-slate-600 hover:bg-slate-500 text-white font-medium rounded px-4 py-2 text-sm">Cancel</button>' +
        (isEdit ? '<button id="m-del" class="bg-red-600/20 hover:bg-red-600/40 text-red-400 font-medium rounded px-4 py-2 text-sm ml-auto">Delete</button>' : '') +
        '</div></div></div>';
    document.body.insertAdjacentHTML('beforeend', h);
    var modal = document.getElementById('sched-modal');
    document.getElementById('m-title').focus();

    // Link button: select text first, then click Link to wrap it
    var linkRow = document.getElementById('m-link-row');
    var linkUrl = document.getElementById('m-link-url');
    var savedRange = null;
    document.getElementById('m-link-btn').onclick = function() {
        var sel = window.getSelection();
        if (sel && sel.rangeCount > 0) savedRange = sel.getRangeAt(0).cloneRange();
        linkRow.classList.remove('hidden');
        linkUrl.value = '';
        linkUrl.focus();
    };
    document.getElementById('m-link-cancel').onclick = function() {
        linkRow.classList.add('hidden');
        savedRange = null;
    };
    linkUrl.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') { e.preventDefault(); document.getElementById('m-link-insert').click(); }
        if (e.key === 'Escape') { linkRow.classList.add('hidden'); savedRange = null; }
    });
    document.getElementById('m-link-insert').onclick = function() {
        var url = linkUrl.value.trim();
        if (!url) return;
        var desc = document.getElementById('m-desc');
        desc.focus();
        if (savedRange) {
            var sel = window.getSelection();
            sel.removeAllRanges();
            sel.addRange(savedRange);
            var text = sel.toString() || url;
            document.execCommand('insertHTML', false, '<a href="' + url + '">' + text + '</a>');
        } else {
            document.execCommand('insertHTML', false, '<a href="' + url + '">' + url + '</a>');
        }
        linkRow.classList.add('hidden');
        savedRange = null;
        addLinkRemoveButtons();
    };

    // Add [x] buttons next to links in the editor
    function addLinkRemoveButtons() {
        var desc = document.getElementById('m-desc');
        if (!desc) return;
        // Remove old [x] buttons
        var old = desc.querySelectorAll('.link-remove');
        for (var i = 0; i < old.length; i++) old[i].remove();
        // Add new ones after each <a>
        var links = desc.querySelectorAll('a');
        for (var i = 0; i < links.length; i++) {
            var x = document.createElement('span');
            x.className = 'link-remove inline-block cursor-pointer text-red-400 hover:text-red-300 text-xs ml-0.5 select-none';
            x.textContent = '[x]';
            x.contentEditable = 'false';
            x.setAttribute('data-link-remove', '');
            links[i].after(x);
        }
    }
    // Handle [x] clicks
    document.getElementById('m-desc').addEventListener('click', function(e) {
        if (e.target.hasAttribute('data-link-remove')) {
            var link = e.target.previousElementSibling;
            if (link && link.tagName === 'A') {
                link.replaceWith(document.createTextNode(link.textContent));
            }
            e.target.remove();
        }
    });
    addLinkRemoveButtons();

    // Color picker click handler
    document.getElementById('m-colors').addEventListener('click', function(e) {
        var btn = e.target.closest('[data-color]'); if (!btn) return;
        selColor = btn.getAttribute('data-color');
        var all = document.getElementById('m-colors').querySelectorAll('button');
        for (var i = 0; i < all.length; i++) {
            all[i].className = 'w-7 h-7 rounded-full cursor-pointer ' +
                (all[i].getAttribute('data-color') === selColor ? 'ring-2 ring-white ring-offset-2 ring-offset-slate-800' : 'ring-1 ring-white/20');
        }
    });

    modal.addEventListener('click', function(e) { if (e.target === modal) modal.remove(); });
    document.getElementById('m-cancel').onclick = function() { modal.remove(); };
    document.getElementById('m-title').addEventListener('keydown', function(e) {
        if (e.key === 'Enter') { e.preventDefault(); document.getElementById('m-save').click(); }
        if (e.key === 'Escape') modal.remove();
    });
    document.getElementById('m-save').onclick = function() {
        var t = document.getElementById('m-title').value.trim(), sa = document.getElementById('m-start').value;
        // Strip [x] remove buttons before saving
        var descEl = document.getElementById('m-desc');
        var removes = descEl.querySelectorAll('.link-remove');
        for (var ri = 0; ri < removes.length; ri++) removes[ri].remove();
        var ea = document.getElementById('m-end').value, desc = descEl.innerHTML.trim();
        if (!t || !sa) return;
        if (isEdit) apiUpdate(opts.id, sa, ea, t, desc, selColor); else apiCreate(sa, ea, t, desc, selColor);
        modal.remove();
    };
    if (isEdit) document.getElementById('m-del').onclick = function() {
        if (confirm('Delete "' + opts.title + '"?')) { apiDelete(opts.id); modal.remove(); }
    };
}

// ── API ──

// API calls are fire-and-forget — the WebSocket pushes authoritative state.
function apiCreate(sa, ea, t, desc, cl) {
    fetch('/admin/schedule/create', { method:'POST',
        headers:{'Content-Type':'application/x-www-form-urlencoded','X-Requested-With':'XMLHttpRequest'},
        body:'start_at='+encodeURIComponent(sa)+'&end_at='+encodeURIComponent(ea)+'&title='+encodeURIComponent(t)+'&description='+encodeURIComponent(desc)+'&color='+encodeURIComponent(cl)
    });
}
function apiUpdate(id, sa, ea, t, desc, cl) {
    fetch('/admin/schedule/'+id+'/update', { method:'POST',
        headers:{'Content-Type':'application/x-www-form-urlencoded','X-Requested-With':'XMLHttpRequest'},
        body:'start_at='+encodeURIComponent(sa)+'&end_at='+encodeURIComponent(ea)+'&title='+encodeURIComponent(t)+'&description='+encodeURIComponent(desc)+'&color='+encodeURIComponent(cl)
    });
}
function apiDelete(id) {
    fetch('/admin/schedule/'+id+'/delete', { method:'POST', headers:{'X-Requested-With':'XMLHttpRequest'} });
}
function findItem(id) { for(var i=0;i<_items.length;i++) if(_items[i].id===id) return _items[i]; return null; }
function rerender() { renderItems(_layer, _items, _startMs, { interactive: true }); }

// ── Init ──

function initAdmin() {
    var c = document.getElementById('schedule-grid');
    if (!c) return;
    _items = (typeof _scheduleItems !== 'undefined' && _scheduleItems) ? _scheduleItems : [];
    if (!_eventStart || !_eventEnd) {
        c.innerHTML = '<div class="text-center py-16 text-slate-500"><p class="text-lg mb-2">Event bounds not configured</p>' +
            '<p class="text-sm">Set event times in <a href="/admin/settings" class="text-orange-400 hover:text-orange-300 underline">Settings</a> first.</p></div>';
        return;
    }
    _startMs = parsedt(_eventStart); _endMs = parsedt(_eventEnd);
    var r = renderGrid('schedule-grid', _items, _startMs, _endMs, { interactive: true });
    if (!r) return;
    _layer = r.layer; _scroll = r.scroll; _totalSlots = r.totalSlots;
    attachDrag();
    createNowLine(_layer, _startMs, _endMs);
    connectScheduleWS(function(items) { _items = items; rerender(); });
}

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initAdmin);
else initAdmin();
