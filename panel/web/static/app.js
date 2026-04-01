// Map display names
var _mapNames = {
    'de_dust2': 'Dust II', 'de_inferno': 'Inferno', 'de_mirage': 'Mirage',
    'de_nuke': 'Nuke', 'de_overpass': 'Overpass', 'de_vertigo': 'Vertigo',
    'de_ancient': 'Ancient', 'de_anubis': 'Anubis', 'de_train': 'Train',
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
        lbl.style.cssText = 'position:absolute;top:0;width:' + minW + 'px;text-align:center;left:' +
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

function renderPublicBracket(matches) {
    renderBracketLayout(document.querySelector('.bracket-container'), matches, renderPublicMatch);
}

function renderPublicMatch(m) {
    var t1class = m.winner && m.winner === m.team1.id ? 'text-green-400 font-bold' : m.winner && m.winner !== m.team1.id ? 'text-slate-500' : 'text-slate-200';
    var t2class = m.winner && m.winner === m.team2.id ? 'text-green-400 font-bold' : m.winner && m.winner !== m.team2.id ? 'text-slate-500' : 'text-slate-200';
    var t1name = m.team1.name || (m.isBye ? '' : 'TBD');
    var t2name = m.team2.name || (m.isBye ? '' : 'TBD');

    if (m.isBye) {
        var byeTeam = m.team1.name || m.team2.name || 'BYE';
        return '<div class="bg-slate-700/30 border border-slate-600/50 rounded p-2 text-xs text-slate-500 text-center italic">' +
            byeTeam + ' (bye)</div>';
    }

    var boLabel = m.bestOf > 1 ? '<span class="text-xs text-yellow-500">Bo' + m.bestOf + '</span>' : '';

    var html = '<div class="bg-slate-700 border border-slate-600 rounded overflow-hidden">';
    // Team rows
    html += '<div class="flex items-center justify-between px-3 py-1.5 border-b border-slate-600/50">';
    html += '<span class="text-sm ' + t1class + '">' + t1name + '</span>' + boLabel;
    html += '</div>';
    html += '<div class="flex items-center justify-between px-3 py-1.5">';
    html += '<span class="text-sm ' + t2class + '">' + t2name + '</span>';
    html += '</div>';

    // Game scores
    if (m.games && m.games.length > 0) {
        html += '<div class="border-t border-slate-600/50">';
        for (var g = 0; g < m.games.length; g++) {
            var game = m.games[g];
            html += '<div class="px-3 py-1 flex items-center gap-2 text-xs">';
            html += '<span class="text-slate-400">' + (mapDisplayName(game.map) || 'Game ' + game.num) + '</span>';
            html += '<span class="text-slate-300 font-mono">' + game.t1 + '-' + game.t2 + '</span>';
            if (game.status === 'completed') html += formatHalfScores(game);
            if (game.status === 'live') {
                html += '<span class="bg-orange-500/20 text-orange-400 font-bold rounded px-1.5 py-0.5">LIVE</span>';
                var connectCmd = buildConnectCmd(game);
                if (connectCmd) {
                    html += '<button onclick="copyConnect(this, \'' + connectCmd.replace(/'/g, "\\'") + '\')" class="ml-auto bg-slate-600 hover:bg-slate-500 text-white rounded px-2 py-0.5 text-xs">Connect</button>';
                }
            }
            if (game.status === 'completed' && game.id) {
                var statsLabel = (mapDisplayName(game.map) || 'Game ' + game.num) + ' \u2014 ' + t1name + ' ' + game.t1 + ':' + game.t2 + ' ' + t2name;
                html += '<button onclick="showMatchStats(' + game.id + ', \'' + statsLabel.replace(/'/g, "\\'") + '\')" class="text-slate-400 hover:text-white ml-auto">Stats</button>';
            }
            html += '</div>';
        }
        html += '</div>';
    }
    html += '</div>';
    return html;
}

// Show match stats in the section below the bracket
function showMatchStats(gameId, title) {
    var section = document.getElementById('match-stats-section');
    var titleEl = document.getElementById('match-stats-title');
    var contentEl = document.getElementById('match-stats-content');
    if (!section || !contentEl) return;

    titleEl.textContent = title || 'Match Stats';
    contentEl.innerHTML = '<div class="text-sm text-slate-500">Loading...</div>';
    section.classList.remove('hidden');
    section.scrollIntoView({behavior: 'smooth', block: 'nearest'});

    fetch('/game/' + gameId + '/stats')
        .then(function(r) { return r.text(); })
        .then(function(html) {
            contentEl.innerHTML = html;
        })
        .catch(function() {
            contentEl.innerHTML = '<div class="text-sm text-red-400">Failed to load stats.</div>';
        });
}

function closeMatchStats() {
    var section = document.getElementById('match-stats-section');
    if (section) section.classList.add('hidden');
}

// Build connect command string for a live game
function buildConnectCmd(game) {
    var ip = (typeof _serverIP !== 'undefined') ? _serverIP : '';
    var pw = (typeof _serverPassword !== 'undefined') ? _serverPassword : '';
    if (!ip || !game.port) return '';
    var cmd = 'connect ' + ip + ':' + game.port;
    if (pw) cmd += '; password ' + pw;
    return cmd;
}

// Copy connect command and show "Copied!" feedback
function copyConnect(btn, cmd) {
    navigator.clipboard.writeText(cmd);
    var orig = btn.textContent;
    btn.textContent = 'Copied!';
    btn.classList.remove('bg-slate-600');
    btn.classList.add('bg-green-600');
    setTimeout(function() {
        btn.textContent = orig;
        btn.classList.remove('bg-green-600');
        btn.classList.add('bg-slate-600');
    }, 2000);
}

// ── Bracket Live Updates via WebSocket ──

function connectBracketWS() {
    var container = document.querySelector('.bracket-container');
    if (!container) return;

    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/ws');

    ws.onmessage = function(e) {
        try {
            var data = JSON.parse(e.data);
            if (data.type === 'bracket' && data.bracket) {
                renderPublicBracket(data.bracket);
            }
        } catch(err) {}
    };

    ws.onclose = function() {
        setTimeout(connectBracketWS, 5000);
    };
}

