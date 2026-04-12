// Map display names
var _mapNames = {
    'de_dust2': 'Dust II', 'de_inferno': 'Inferno', 'de_mirage': 'Mirage',
    'de_nuke': 'Nuke', 'de_overpass': 'Overpass', 'de_vertigo': 'Vertigo',
    'de_ancient': 'Ancient', 'de_anubis': 'Anubis', 'de_train': 'Train',
    'de_shortdust': 'Shortdust', 'de_lake': 'Lake',
    'cs_office': 'Office', 'cs_italy': 'Italy', 'cs_alpine': 'Alpine',
    'ar_baggage': 'Baggage', 'ar_shoots': 'Shoots', 'ar_pool_day': 'Pool Day'
};
function mapDisplayName(m) {
    if (!m) return '';
    if (_mapNames[m]) return _mapNames[m];
    var name = m.replace(/^(de|cs|ar)_/, '');
    return name.charAt(0).toUpperCase() + name.slice(1).replace(/_/g, ' ');
}

// ── Public Bracket Rendering ──

function renderBracketLayout(container, matches, renderFn, cardMinWidth) {
    if (!container || !matches.length) return;
    var minW = cardMinWidth || 220;

    // Group by round, index by (round, pos) for O(1) lookup
    var byKey = {};
    var maxRound = 0;
    for (var i = 0; i < matches.length; i++) {
        var m = matches[i];
        byKey[m.round + ':' + m.pos] = m;
        if (m.round > maxRound) maxRound = m.round;
    }

    function roundLabel(r) {
        if (r === maxRound) return 'Final';
        if (r === maxRound - 1) return 'Semis';
        return 'Round ' + r;
    }

    // Recursively build nested bracket HTML from the final round backward.
    // Each pair: [sources column] [connector] [match card]
    // CSS align-items:center ensures the target is always vertically centered
    // between its two feeder matches, regardless of card height differences.
    function buildSubtree(round, pos) {
        var match = byKey[round + ':' + pos];
        var card = match ? renderFn(match) : '<div class="text-xs text-slate-500 italic p-2">TBD</div>';

        if (round === 1) {
            return '<div class="bracket-card" data-round="' + round + '" style="min-width:' + minW + 'px">' + card + '</div>';
        }

        var s1 = buildSubtree(round - 1, pos * 2);
        var s2 = buildSubtree(round - 1, pos * 2 + 1);

        return '<div class="bracket-pair" style="display:flex;align-items:center">' +
            '<div class="bracket-sources" style="display:flex;flex-direction:column;gap:8px">' +
                s1 + s2 +
            '</div>' +
            '<div class="bracket-conn" style="width:24px;min-width:24px;align-self:stretch;position:relative"></div>' +
            '<div class="bracket-card" data-round="' + round + '" style="min-width:' + minW + 'px">' + card + '</div>' +
        '</div>';
    }

    // Build bracket tree from each final-round match
    var treeHtml = '';
    for (var p = 0; byKey[maxRound + ':' + p]; p++) {
        treeHtml += buildSubtree(maxRound, p);
    }

    container.innerHTML =
        '<div class="bracket-labels" style="position:relative;height:20px;min-width:max-content"></div>' +
        '<div class="bracket-tree" style="min-width:max-content;margin-top:40px">' + treeHtml + '</div>';

    // Equalize card widths per round so columns align
    var cards = container.querySelectorAll('.bracket-card');
    var maxWidths = {};
    for (var ci = 0; ci < cards.length; ci++) {
        var rd = cards[ci].getAttribute('data-round');
        var w = cards[ci].offsetWidth;
        if (!maxWidths[rd] || w > maxWidths[rd]) maxWidths[rd] = w;
    }
    for (var ci = 0; ci < cards.length; ci++) {
        var rd = cards[ci].getAttribute('data-round');
        cards[ci].style.width = maxWidths[rd] + 'px';
    }

    // Draw SVG connector lines using actual rendered positions
    drawBracketConnectors(container);

    // Position round labels above the actual bracket columns
    var labelsRow = container.querySelector('.bracket-labels');
    var cards = container.querySelectorAll('.bracket-card');
    var placed = {};
    for (var ci = 0; ci < cards.length; ci++) {
        var rd = cards[ci].getAttribute('data-round');
        if (placed[rd]) continue;
        placed[rd] = true;
        var cardRect = cards[ci].getBoundingClientRect();
        var containerRect = container.getBoundingClientRect();
        var lbl = document.createElement('div');
        lbl.className = 'text-xs text-slate-500';
        lbl.style.cssText = 'position:absolute;top:0;width:' + cardRect.width + 'px;text-align:center;left:' +
            (cardRect.left - containerRect.left) + 'px';
        lbl.textContent = roundLabel(parseInt(rd));
        labelsRow.appendChild(lbl);
    }
}

function drawBracketConnectors(container) {
    var connectors = container.querySelectorAll('.bracket-conn');
    var svgNS = 'http://www.w3.org/2000/svg';

    for (var c = 0; c < connectors.length; c++) {
        var conn = connectors[c];
        var pair = conn.parentElement; // .bracket-pair
        var sources = conn.previousElementSibling; // .bracket-sources
        var target = conn.nextElementSibling; // .bracket-card

        if (!sources || !target) continue;

        // The two source children (either bracket-card or bracket-pair)
        var children = sources.children;
        if (children.length < 2) continue;

        var connRect = conn.getBoundingClientRect();
        var s1Rect = children[0].getBoundingClientRect();
        var s2Rect = children[1].getBoundingClientRect();
        var tgtRect = target.getBoundingClientRect();

        var y1 = s1Rect.top + s1Rect.height / 2 - connRect.top;
        var y2 = s2Rect.top + s2Rect.height / 2 - connRect.top;
        var yTgt = tgtRect.top + tgtRect.height / 2 - connRect.top;
        var midX = connRect.width / 2;
        var w = connRect.width;

        var svg = document.createElementNS(svgNS, 'svg');
        svg.style.cssText = 'position:absolute;top:0;left:0;width:100%;height:100%;overflow:visible';
        svg.setAttribute('width', w);
        svg.setAttribute('height', connRect.height);

        // Source 1 horizontal
        var l = document.createElementNS(svgNS, 'line');
        l.setAttribute('x1', 0); l.setAttribute('y1', y1);
        l.setAttribute('x2', midX); l.setAttribute('y2', y1);
        l.setAttribute('stroke', '#475569'); l.setAttribute('stroke-width', '1');
        svg.appendChild(l);

        // Source 2 horizontal
        l = document.createElementNS(svgNS, 'line');
        l.setAttribute('x1', 0); l.setAttribute('y1', y2);
        l.setAttribute('x2', midX); l.setAttribute('y2', y2);
        l.setAttribute('stroke', '#475569'); l.setAttribute('stroke-width', '1');
        svg.appendChild(l);

        // Vertical joining sources
        l = document.createElementNS(svgNS, 'line');
        l.setAttribute('x1', midX); l.setAttribute('y1', y1);
        l.setAttribute('x2', midX); l.setAttribute('y2', y2);
        l.setAttribute('stroke', '#475569'); l.setAttribute('stroke-width', '1');
        svg.appendChild(l);

        // Horizontal to target
        l = document.createElementNS(svgNS, 'line');
        l.setAttribute('x1', midX); l.setAttribute('y1', yTgt);
        l.setAttribute('x2', w); l.setAttribute('y2', yTgt);
        l.setAttribute('stroke', '#475569'); l.setAttribute('stroke-width', '1');
        svg.appendChild(l);

        conn.innerHTML = '';
        conn.appendChild(svg);
    }
}

// ── Double Elimination Bracket Rendering ──

function renderDoubleElimBracket(container, matches, renderFn, cardMinWidth) {
    if (!container || !matches.length) return;

    var winners = [];
    var losers = [];
    var grandFinal = [];

    for (var i = 0; i < matches.length; i++) {
        var section = matches[i].section || 'winners';
        if (section === 'grand_final') {
            grandFinal.push(matches[i]);
        } else if (section === 'losers') {
            losers.push(matches[i]);
        } else {
            winners.push(matches[i]);
        }
    }

    var html = '';

    // Winners Bracket section
    if (winners.length > 0) {
        html += '<div class="mb-8">';
        html += '<h3 class="text-lg font-semibold mb-2 text-emerald-400">Winners Bracket</h3>';
        html += '<div class="overflow-x-auto"><div class="bracket-section bracket-section-winners flex gap-8 items-start min-w-max py-4"></div></div>';
        html += '</div>';
    }

    // Losers Bracket section
    if (losers.length > 0) {
        html += '<div class="mb-8">';
        html += '<h3 class="text-lg font-semibold mb-2 text-red-400">Losers Bracket</h3>';
        html += '<div class="overflow-x-auto"><div class="bracket-section bracket-section-losers flex gap-8 items-start min-w-max py-4"></div></div>';
        html += '</div>';
    }

    // Grand Final section
    if (grandFinal.length > 0) {
        html += '<div class="mb-8">';
        html += '<h3 class="text-lg font-semibold mb-2 text-amber-400">Grand Final</h3>';
        html += '<div class="bracket-section bracket-section-grand-final flex justify-center py-4"></div>';
        html += '</div>';
    }

    container.innerHTML = html;

    // Render each section using the existing bracket layout logic
    if (winners.length > 0) {
        var winnersContainer = container.querySelector('.bracket-section-winners');
        renderBracketLayout(winnersContainer, winners, renderFn, cardMinWidth);
    }

    if (losers.length > 0) {
        var losersContainer = container.querySelector('.bracket-section-losers');
        // Use slightly smaller cards for losers bracket
        var losersMinWidth = cardMinWidth ? Math.round(cardMinWidth * 0.85) : 190;
        renderBracketLayout(losersContainer, losers, renderFn, losersMinWidth);
    }

    if (grandFinal.length > 0) {
        var gfContainer = container.querySelector('.bracket-section-grand-final');
        // Render grand final match(es) as simple centered cards
        var gfMinWidth = cardMinWidth || 220;
        var gfHtml = '';
        for (var g = 0; g < grandFinal.length; g++) {
            gfHtml += '<div class="bracket-card" style="min-width:' + gfMinWidth + 'px">' + renderFn(grandFinal[g]) + '</div>';
        }
        gfContainer.innerHTML = gfHtml;
    }
}

function hasDoubleElim(matches) {
    for (var i = 0; i < matches.length; i++) {
        var section = matches[i].section;
        if (section === 'losers' || section === 'grand_final') return true;
    }
    return false;
}

function isRoundRobin(matches) {
    return matches.length > 0 && matches.every(function(m) { return (m.section || 'winners') === 'group'; });
}

function isHybrid(matches) {
    var hasGroup = false;
    var hasPlayoff = false;
    for (var i = 0; i < matches.length; i++) {
        var s = matches[i].section || 'winners';
        if (s === 'group') hasGroup = true;
        if (s === 'winners' || s === 'losers' || s === 'grand_final') hasPlayoff = true;
    }
    return hasGroup && hasPlayoff;
}

// ── Hybrid Bracket Rendering ──

function renderHybridBracket(container, matches, standings, renderFn, cardMinWidth) {
    if (!container) return;
    if (!matches || !matches.length) {
        container.innerHTML = '<p class="text-slate-500 text-sm">No matches generated yet.</p>';
        return;
    }

    var groupMatches = [];
    var playoffMatches = [];
    for (var i = 0; i < matches.length; i++) {
        var s = matches[i].section || 'winners';
        if (s === 'group') {
            groupMatches.push(matches[i]);
        } else {
            playoffMatches.push(matches[i]);
        }
    }

    var html = '';

    // Compact group standings
    if (standings && standings.length > 0) {
        var standingsByGroup = {};
        var groupOrder = [];
        for (var s = 0; s < standings.length; s++) {
            var sg = standings[s].groupId || 0;
            if (!standingsByGroup[sg]) {
                standingsByGroup[sg] = [];
                groupOrder.push(sg);
            }
            standingsByGroup[sg].push(standings[s]);
        }
        groupOrder.sort(function(a, b) { return a - b; });

        html += '<div class="mb-6">';
        html += '<h3 class="text-lg font-semibold mb-3 text-slate-300">Group Stage Results</h3>';
        html += '<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-' + Math.min(groupOrder.length, 3) + ' gap-4">';
        for (var gi = 0; gi < groupOrder.length; gi++) {
            var gid = groupOrder[gi];
            var groupStandings = standingsByGroup[gid] || [];
            html += '<div class="bg-slate-700/30 border border-slate-600/50 rounded-lg overflow-hidden">';
            html += '<div class="px-3 py-2 bg-slate-700/50 text-sm font-semibold text-emerald-400">Group ' + String.fromCharCode(65 + gid) + '</div>';
            html += '<table class="w-full text-xs">';
            html += '<thead><tr class="text-slate-500 border-b border-slate-600">';
            html += '<th class="text-left px-2 py-1">#</th>';
            html += '<th class="text-left px-2 py-1">Team</th>';
            html += '<th class="px-1 py-1 text-center">W</th>';
            html += '<th class="px-1 py-1 text-center">L</th>';
            html += '<th class="px-1 py-1 text-center">Pts</th>';
            html += '</tr></thead><tbody>';
            for (var si = 0; si < groupStandings.length; si++) {
                var st = groupStandings[si];
                var rowClass = si === 0 ? 'text-green-400' : 'text-slate-300';
                html += '<tr class="' + rowClass + ' border-b border-slate-700/50">';
                html += '<td class="px-2 py-1 text-slate-500">' + (si + 1) + '</td>';
                html += '<td class="px-2 py-1 font-medium">' + (st.teamName || 'TBD') + '</td>';
                html += '<td class="px-1 py-1 text-center">' + st.wins + '</td>';
                html += '<td class="px-1 py-1 text-center">' + st.losses + '</td>';
                html += '<td class="px-1 py-1 text-center font-bold">' + st.points + '</td>';
                html += '</tr>';
            }
            html += '</tbody></table></div>';
        }
        html += '</div></div>';
    }

    // Playoff bracket
    if (playoffMatches.length > 0) {
        html += '<div class="mb-4">';
        html += '<h3 class="text-lg font-semibold mb-3 text-slate-300">Playoffs</h3>';
        html += '<div class="playoff-bracket-container flex gap-8 items-start min-w-max py-4"></div>';
        html += '</div>';
    }

    container.innerHTML = html;

    // Render the playoff bracket into its container
    if (playoffMatches.length > 0) {
        var playoffContainer = container.querySelector('.playoff-bracket-container');
        if (hasDoubleElim(playoffMatches)) {
            renderDoubleElimBracket(playoffContainer, playoffMatches, renderFn, cardMinWidth);
        } else {
            renderBracketLayout(playoffContainer, playoffMatches, renderFn, cardMinWidth);
        }
    }
}

// ── Round Robin Bracket Rendering ──

function renderRoundRobinBracket(container, matches, standings, renderFn) {
    if (!container) return;
    if (!matches || !matches.length) {
        container.innerHTML = '<p class="text-slate-500 text-sm">No matches generated yet.</p>';
        return;
    }

    // Group matches by groupId
    var groups = {};
    var groupOrder = [];
    for (var i = 0; i < matches.length; i++) {
        var gid = matches[i].groupId || 0;
        if (!groups[gid]) {
            groups[gid] = [];
            groupOrder.push(gid);
        }
        groups[gid].push(matches[i]);
    }
    groupOrder.sort(function(a, b) { return a - b; });

    // Group standings by groupId
    var standingsByGroup = {};
    if (standings) {
        for (var s = 0; s < standings.length; s++) {
            var sg = standings[s].groupId || 0;
            if (!standingsByGroup[sg]) standingsByGroup[sg] = [];
            standingsByGroup[sg].push(standings[s]);
        }
    }

    var html = '';
    var multiGroup = groupOrder.length > 1;

    for (var gi = 0; gi < groupOrder.length; gi++) {
        var gid = groupOrder[gi];
        var groupMatches = groups[gid];
        var groupStandings = standingsByGroup[gid] || [];

        html += '<div class="mb-8">';
        if (multiGroup) {
            html += '<h3 class="text-lg font-semibold mb-3 text-emerald-400">Group ' + String.fromCharCode(65 + gid) + '</h3>';
        }

        // Standings table
        if (groupStandings.length > 0) {
            html += '<div class="bg-slate-700/30 border border-slate-600/50 rounded-lg overflow-hidden mb-4">';
            html += '<table class="w-full text-sm">';
            html += '<thead><tr class="text-slate-400 border-b border-slate-600">';
            html += '<th class="text-left px-3 py-2 w-8">#</th>';
            html += '<th class="text-left px-3 py-2">Team</th>';
            html += '<th class="px-2 py-2 text-center">W</th>';
            html += '<th class="px-2 py-2 text-center">L</th>';
            html += '<th class="px-2 py-2 text-center">+/-</th>';
            html += '<th class="px-2 py-2 text-center">RD</th>';
            html += '<th class="px-2 py-2 text-center">Pts</th>';
            html += '</tr></thead><tbody>';
            for (var si = 0; si < groupStandings.length; si++) {
                var st = groupStandings[si];
                var rowClass = si === 0 ? 'text-green-400' : 'text-slate-300';
                html += '<tr class="' + rowClass + ' border-b border-slate-700/50">';
                html += '<td class="px-3 py-1.5 text-slate-500">' + (si + 1) + '</td>';
                html += '<td class="px-3 py-1.5 font-medium">' + (st.teamName || 'TBD') + '</td>';
                html += '<td class="px-2 py-1.5 text-center">' + st.wins + '</td>';
                html += '<td class="px-2 py-1.5 text-center">' + st.losses + '</td>';
                var diffStr = st.mapDiff > 0 ? '+' + st.mapDiff : '' + st.mapDiff;
                html += '<td class="px-2 py-1.5 text-center">' + diffStr + '</td>';
                var rdStr = st.roundDiff > 0 ? '+' + st.roundDiff : '' + st.roundDiff;
                html += '<td class="px-2 py-1.5 text-center">' + rdStr + '</td>';
                html += '<td class="px-2 py-1.5 text-center font-bold">' + st.points + '</td>';
                html += '</tr>';
            }
            html += '</tbody></table></div>';
        }

        // Match list grouped by round
        var byRound = {};
        var roundOrder = [];
        for (var mi = 0; mi < groupMatches.length; mi++) {
            var r = groupMatches[mi].round;
            if (!byRound[r]) {
                byRound[r] = [];
                roundOrder.push(r);
            }
            byRound[r].push(groupMatches[mi]);
        }
        roundOrder.sort(function(a, b) { return a - b; });

        html += '<div class="space-y-3">';
        for (var ri = 0; ri < roundOrder.length; ri++) {
            var round = roundOrder[ri];
            var roundMatches = byRound[round];
            html += '<div>';
            html += '<div class="text-xs text-slate-500 mb-1 font-medium">Matchday ' + round + '</div>';
            html += '<div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">';
            for (var mi = 0; mi < roundMatches.length; mi++) {
                html += '<div class="rr-match">' + renderFn(roundMatches[mi]) + '</div>';
            }
            html += '</div></div>';
        }
        html += '</div></div>';
    }

    container.innerHTML = html;
}

// Format half-time score splits with CT/T coloring
function formatHalfScores(game) {
    if (!game.h1ct && !game.h1t && !game.h2ct && !game.h2t) return '';
    var t1ct = game.t1ct;
    var h1_t1 = t1ct ? game.h1ct : game.h1t;
    var h1_t2 = t1ct ? game.h1t : game.h1ct;
    var h2_t1 = t1ct ? game.h2t : game.h2ct;
    var h2_t2 = t1ct ? game.h2ct : game.h2t;
    var ct = 'color:#60a5fa', t = 'color:#facc15';
    var h1_t1_c = t1ct ? ct : t;
    var h1_t2_c = t1ct ? t : ct;
    var h2_t1_c = t1ct ? t : ct;
    var h2_t2_c = t1ct ? ct : t;
    return '<span class="text-slate-500 font-mono">(' +
        '<span style="' + h1_t1_c + '">' + h1_t1 + '</span>:<span style="' + h1_t2_c + '">' + h1_t2 + '</span>' +
        ' ; ' +
        '<span style="' + h2_t1_c + '">' + h2_t1 + '</span>:<span style="' + h2_t2_c + '">' + h2_t2 + '</span>' +
        ')</span>';
}
