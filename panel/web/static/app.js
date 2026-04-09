function renderPublicBracket(matches) {
    renderBracketLayout(document.querySelector('.bracket-container'), matches, renderPublicMatch);
}

function renderPublicMatch(m) {
    var t1class = m.winner && m.winner === m.team1.id ? 'text-green-400 font-bold' : m.winner && m.winner !== m.team1.id ? 'text-slate-500' : 'text-slate-200';
    var t2class = m.winner && m.winner === m.team2.id ? 'text-green-400 font-bold' : m.winner && m.winner !== m.team2.id ? 'text-slate-500' : 'text-slate-200';
    var t1name = m.team1.name || (m.isBye ? '' : 'TBD');
    var t2name = m.team2.name || (m.isBye ? '' : 'TBD');
    if (!m.team1.name && !m.isBye) t1class = 'text-slate-600 italic';
    if (!m.team2.name && !m.isBye) t2class = 'text-slate-600 italic';

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

// ── Public Team AJAX Actions ──

function publicCreateTeam(form) {
    var data = new URLSearchParams(new FormData(form));
    if (typeof _tournamentID !== 'undefined' && _tournamentID) data.set('tournament_id', _tournamentID);
    fetch('/teams', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'},
        body: data
    });
    form.querySelector('input[name="name"]').value = '';
    return false;
}

function publicAddMember(teamId, form) {
    var data = new URLSearchParams(new FormData(form));
    if (typeof _tournamentID !== 'undefined' && _tournamentID) data.set('tournament_id', _tournamentID);
    fetch('/teams/' + teamId + '/members', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'},
        body: data
    });
    form.querySelector('input[name="steam_name"]').value = '';
    return false;
}

function publicRemoveMember(teamId, memberId) {
    var body = '';
    if (typeof _tournamentID !== 'undefined' && _tournamentID) body = 'tournament_id=' + _tournamentID;
    fetch('/teams/' + teamId + '/members/' + memberId + '/delete', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest', 'Content-Type': 'application/x-www-form-urlencoded'},
        body: body
    });
}

function publicRenameTeam(teamId, btn) {
    var row = btn.closest('.flex.justify-between');
    var span = row.querySelector('.font-medium');
    var current = span.textContent;
    var input = document.createElement('input');
    input.type = 'text';
    input.value = current;
    input.className = 'bg-slate-600 border border-slate-500 rounded px-1 py-0.5 text-sm text-white flex-1 min-w-0 focus:outline-none';
    span.replaceWith(input);
    input.focus();
    input.select();
    function save() {
        var name = input.value.trim();
        if (!name || name === current) {
            var s = document.createElement('span');
            s.className = 'font-medium text-sm truncate';
            s.textContent = current;
            input.replaceWith(s);
            return;
        }
        var renameBody = 'name=' + encodeURIComponent(name);
        if (typeof _tournamentID !== 'undefined' && _tournamentID) renameBody += '&tournament_id=' + _tournamentID;
        fetch('/teams/' + teamId + '/rename', {
            method: 'POST',
            headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest'},
            body: renameBody
        });
    }
    input.addEventListener('blur', save);
    input.addEventListener('keydown', function(e) { if (e.key === 'Enter') { e.preventDefault(); input.blur(); } if (e.key === 'Escape') { input.value = current; input.blur(); } });
}

function renderPublicTeams(teams, teamSize, canRegister, status) {
    var container = document.getElementById('public-teams-container');
    if (!container) return;

    if ((!teams || teams.length === 0) && !canRegister) {
        container.innerHTML = '';
        return;
    }

    var html = '<div class="bg-slate-800 border border-slate-700 rounded-lg p-4 sm:p-6">';

    if (canRegister) {
        // Registration mode
        html += '<h2 class="text-lg font-semibold mb-4">Register a Team</h2>';
        html += '<form onsubmit="return publicCreateTeam(this)" class="flex flex-col sm:flex-row gap-2 mb-6">';
        html += '<input type="text" name="name" placeholder="Team name" required class="flex-1 bg-slate-700 border border-slate-600 rounded px-3 py-2 text-sm text-white focus:outline-none focus:border-orange-500">';
        html += '<button type="submit" class="bg-orange-600 hover:bg-orange-700 text-white font-medium rounded px-4 py-2 text-sm">Create Team</button>';
        html += '</form>';

        if (teams && teams.length > 0) {
            html += '<h3 class="text-sm font-medium text-slate-400 mb-3">Teams (' + teams.length + ')</h3>';
            html += '<div class="space-y-3">';
            for (var i = 0; i < teams.length; i++) {
                var t = teams[i];
                html += '<div class="bg-slate-700/50 rounded p-3">';
                html += '<div class="flex items-center justify-between mb-2 min-w-0">';
                html += '<span class="font-medium text-sm truncate min-w-0">' + t.name + '</span>';
                html += '<span class="flex items-center gap-2">';
                html += '<button onclick="publicRenameTeam(' + t.id + ', this)" class="text-slate-400 hover:text-white text-xs" title="Rename"><svg class="w-3.5 h-3.5 inline" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z"/></svg></button>';
                html += '<span class="text-xs text-slate-500">' + (t.members ? t.members.length : 0) + '/' + teamSize + ' players</span>';
                html += '</span></div>';
                if (t.members && t.members.length > 0) {
                    html += '<ul class="text-xs text-slate-400 space-y-1 mb-2">';
                    for (var j = 0; j < t.members.length; j++) {
                        var m = t.members[j];
                        html += '<li class="flex items-center justify-between min-w-0"><span class="truncate min-w-0">' + m.steamName + '</span>';
                        html += '<button onclick="publicRemoveMember(' + t.id + ',' + m.id + ')" class="bg-red-600 hover:bg-red-700 rounded px-1.5 py-1" title="Remove player"><img src="/static/icons/ui/friendremove.svg" class="w-4 h-4"></button></li>';
                    }
                    html += '</ul>';
                }
                html += '<form onsubmit="return publicAddMember(' + t.id + ', this)" class="flex gap-1">';
                html += '<input type="text" name="steam_name" placeholder="Steam name" required class="flex-1 bg-slate-600 border border-slate-500 rounded px-2 py-1 text-xs text-white focus:outline-none">';
                html += '<button type="submit" class="bg-slate-600 hover:bg-slate-500 rounded px-1.5 py-1" title="Add player"><img src="/static/icons/ui/addplayer.svg" class="w-4 h-4"></button>';
                html += '</form></div>';
            }
            html += '</div>';
        } else {
            html += '<p class="text-slate-500 text-sm">No teams registered yet. Be the first!</p>';
        }
    } else if (teams && teams.length > 0) {
        // Read-only collapsed mode
        var collapsed = status === 'active' || status === 'completed' || status === 'locked';
        html += '<button onclick="var c=this.nextElementSibling;c.classList.toggle(\'hidden\');this.querySelector(\'.arrow\').textContent=c.classList.contains(\'hidden\')?\'\\u25B8\':\'\\u25BE\'" class="flex items-center gap-2 w-full text-left">';
        html += '<h2 class="text-lg font-semibold">Teams (' + teams.length + ')</h2>';
        html += '<span class="arrow text-slate-400">' + (collapsed ? '\u25B8' : '\u25BE') + '</span></button>';
        html += '<div class="' + (collapsed ? 'hidden ' : '') + 'mt-4"><div class="grid grid-cols-1 sm:grid-cols-2 gap-3">';
        for (var i = 0; i < teams.length; i++) {
            var t = teams[i];
            html += '<div class="bg-slate-700/50 rounded p-3"><span class="font-medium text-sm">' + t.name + '</span>';
            if (t.members && t.members.length > 0) {
                html += '<ul class="text-xs text-slate-400 mt-1">';
                for (var j = 0; j < t.members.length; j++) {
                    html += '<li>' + t.members[j].steamName + '</li>';
                }
                html += '</ul>';
            }
            html += '</div>';
        }
        html += '</div></div>';
    }

    html += '</div>';
    container.innerHTML = html;
}

// ── Bracket Live Updates via WebSocket ──

var _bracketWS = null;
var _bracketRetries = 0;
var _lastBracketJSON = '';
var _lastTournamentStatus = '';
function connectBracketWS() {
    // No live updates for completed tournaments
    if (typeof _tournamentStatus !== 'undefined' && _tournamentStatus === 'completed') return;

    if (_bracketWS) { try { _bracketWS.close(); } catch(e) {} }

    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var wsPath = '/ws';
    if (typeof _tournamentID !== 'undefined' && _tournamentID) {
        wsPath = '/tournament/' + _tournamentID + '/ws';
    }
    var ws = new WebSocket(protocol + '//' + location.host + wsPath);
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

                // Update teams
                if (data.teams !== undefined) {
                    var ts = (typeof _teamSize !== 'undefined') ? _teamSize : (data.teamSize || 5);
                    var cr = data.canRegister || false;
                    var st = data.status || '';
                    renderPublicTeams(data.teams, ts, cr, st);
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

