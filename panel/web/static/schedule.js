// ── Public Schedule View ──
// Read-only calendar grid with live "now" line. Depends on schedule_common.js.

function initPublic() {
    var c = document.getElementById('schedule-grid');
    if (!c) return;
    var items = (typeof _scheduleItems !== 'undefined' && _scheduleItems) ? _scheduleItems : [];
    var siteName = (typeof _siteName !== 'undefined' && _siteName) ? _siteName : '';

    if (!_eventStart || !_eventEnd || !items.length) {
        c.innerHTML = '<div class="text-center py-20">' +
            (siteName ? '<h1 class="text-3xl font-bold text-white mb-4">' + siteName + '</h1>' : '') +
            '<h2 class="text-2xl font-bold text-slate-400 mb-2">No Schedule Yet</h2>' +
            '<p class="text-slate-500">Check back later for the event schedule.</p></div>';
        return;
    }

    var startMs = parsedt(_eventStart), endMs = parsedt(_eventEnd);
    var r = renderGrid('schedule-grid', items, startMs, endMs, {});
    if (!r) return;

    createNowLine(r.layer, startMs, endMs);
    connectScheduleWS(function(items) {
        renderItems(r.layer, items, startMs, {});
    });

    // Scroll to now line on load
    setTimeout(function() {
        var nowMs = Date.now();
        if (nowMs >= startMs && nowMs <= endMs) {
            var y = slot2y(ms2slot(nowMs, startMs) + GRID_PAD);
            r.scroll.scrollTop = Math.max(0, y - r.scroll.clientHeight / 3);
        }
    }, 100);
}

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initPublic);
else initPublic();
