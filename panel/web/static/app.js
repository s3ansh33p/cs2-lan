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
        .then(function(r) { if (!r.ok) throw new Error(r.status); return r.text(); })
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
    var orig = btn.textContent;
    navigator.clipboard.writeText(cmd).then(function() {
        btn.textContent = 'Copied!';
        btn.classList.remove('bg-slate-600');
        btn.classList.add('bg-green-600');
        setTimeout(function() {
            btn.textContent = orig;
            btn.classList.remove('bg-green-600');
            btn.classList.add('bg-slate-600');
        }, 2000);
    }).catch(function() {
        btn.textContent = 'Failed';
        setTimeout(function() { btn.textContent = orig; }, 2000);
    });
}

// ── Bracket Live Updates via WebSocket ──

var _bracketWS = null;
var _bracketRetries = 0;
var _lastBracketJSON = '';
var _lastTournamentStatus = '';
function connectBracketWS() {
    if (_bracketWS) { try { _bracketWS.close(); } catch(e) {} }

    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/ws');
    _bracketWS = ws;

    ws.onmessage = function(e) {
        try {
            _bracketRetries = 0;
            var data = JSON.parse(e.data);
            if (data.type === 'bracket') {
                // If tournament status changed, reload to get new page structure
                if (_lastTournamentStatus && data.status && data.status !== _lastTournamentStatus) {
                    location.reload();
                    return;
                }
                if (data.status) _lastTournamentStatus = data.status;

                // Update bracket if data changed
                if (data.bracket) {
                    var key = JSON.stringify(data.bracket);
                    if (key !== _lastBracketJSON) {
                        _lastBracketJSON = key;
                        var container = document.querySelector('.bracket-container');
                        if (container) renderPublicBracket(data.bracket);
                    }
                }

                // Update connect info if provided
                if (data.connectInfo !== undefined) {
                    _serverIP = '';
                    _serverPassword = '';
                }
            }
        } catch(err) { console.error('[bracket-ws] error:', err); }
    };

    ws.onclose = function() {
        _bracketWS = null;
        var delay = Math.min(5000 * Math.pow(2, _bracketRetries), 60000);
        _bracketRetries++;
        setTimeout(connectBracketWS, delay);
    };
}

