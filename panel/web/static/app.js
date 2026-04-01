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

function renderBracketLayout(container, matches, renderFn) {
    if (!container || !matches.length) return;

    var rounds = {};
    var maxRound = 0;
    for (var i = 0; i < matches.length; i++) {
        var m = matches[i];
        if (!rounds[m.round]) rounds[m.round] = [];
        rounds[m.round].push(m);
        if (m.round > maxRound) maxRound = m.round;
    }

    var html = '<div class="bracket-grid" style="display:flex;align-items:stretch;gap:0;min-width:max-content">';
    for (var r = 1; r <= maxRound; r++) {
        var roundMatches = rounds[r] || [];
        var roundLabel = r === maxRound ? 'Final' : r === maxRound - 1 ? 'Semis' : 'Round ' + r;
        html += '<div style="display:flex;flex-direction:column;min-width:220px">';
        html += '<div class="text-xs text-slate-500 text-center mb-2">' + roundLabel + '</div>';
        html += '<div style="display:flex;flex-direction:column;justify-content:space-around;flex:1;gap:8px">';
        for (var j = 0; j < roundMatches.length; j++) {
            html += renderFn(roundMatches[j]);
        }
        html += '</div></div>';
        // Connector lines between rounds
        if (r < maxRound) {
            html += '<div style="display:flex;flex-direction:column;justify-content:space-around;flex:1;width:24px;min-width:24px">';
            var nextRound = rounds[r + 1] || [];
            for (var k = 0; k < nextRound.length; k++) {
                html += '<div style="flex:1;display:flex;flex-direction:column;justify-content:center;position:relative">';
                html += '<svg style="position:absolute;inset:0;width:100%;height:100%" preserveAspectRatio="none">';
                html += '<line x1="0" y1="25%" x2="50%" y2="25%" stroke="#475569" stroke-width="1"/>';
                html += '<line x1="0" y1="75%" x2="50%" y2="75%" stroke="#475569" stroke-width="1"/>';
                html += '<line x1="50%" y1="25%" x2="50%" y2="75%" stroke="#475569" stroke-width="1"/>';
                html += '<line x1="50%" y1="50%" x2="100%" y2="50%" stroke="#475569" stroke-width="1"/>';
                html += '</svg>';
                html += '</div>';
            }
            html += '</div>';
        }
    }
    html += '</div>';
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

    fetch('/bracket/game/' + gameId + '/stats')
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
    var ws = new WebSocket(protocol + '//' + location.host + '/bracket/ws');

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

