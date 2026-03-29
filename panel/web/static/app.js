// Log viewer WebSocket
var _logServerName = null;
var _logPaused = false;
var _logBuffer = []; // buffer lines while paused
var _logShowEvents = false; // show game event lines in log viewer

function connectLogWS(serverName) {
    _logServerName = serverName;
    _logPaused = false;
    _logBuffer = [];
    _lastLogLine = null;
    updatePauseButton();

    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/server/' + serverName + '/logs/ws');
    var output = document.getElementById('log-output');
    var status = document.getElementById('log-status');
    var reconnectBtn = document.getElementById('log-reconnect');

    if (reconnectBtn) reconnectBtn.classList.add('hidden');

    ws.onopen = function() {
        if (status) {
            status.textContent = 'Connected';
            status.className = 'text-xs text-green-400';
        }
    };

    ws.onmessage = function(e) {
        if (_logPaused) {
            _logBuffer.push(e.data);
            // Cap buffer so memory doesn't blow up
            if (_logBuffer.length > 2000) _logBuffer.shift();
            updatePauseButton();
            return;
        }
        appendLogLine(e.data);
    };

    ws.onclose = function() {
        if (status) {
            status.textContent = 'Disconnected';
            status.className = 'text-xs text-red-400';
        }
        if (reconnectBtn) reconnectBtn.classList.remove('hidden');
    };

    ws.onerror = function() {
        if (status) {
            status.textContent = 'Error';
            status.className = 'text-xs text-red-400';
        }
    };
}

var _lastLogLine = null; // track last line element for dedup

function appendLogLine(text) {
    var output = document.getElementById('log-output');
    if (!output) return;

    // Check for game event prefix
    var isEvent = false;
    if (text.substring(0, 2) === 'E:') {
        isEvent = true;
        text = text.substring(2);
    }

    // Hide game events unless toggled on
    if (isEvent && !_logShowEvents) return;

    // Deduplicate consecutive identical lines
    if (_lastLogLine && _lastLogLine._logText === text) {
        _lastLogLine._logCount = (_lastLogLine._logCount || 1) + 1;
        var badge = _lastLogLine.querySelector('.log-count');
        if (!badge) {
            badge = document.createElement('span');
            badge.className = 'log-count text-yellow-400 ml-2';
            _lastLogLine.appendChild(badge);
        }
        badge.textContent = '[x' + _lastLogLine._logCount + ']';
        output.scrollTop = output.scrollHeight;
        return;
    }

    var line = document.createElement('div');
    line.textContent = text;
    line._logText = text;
    line._logCount = 1;
    if (isEvent) line.className = 'text-slate-500';
    output.appendChild(line);
    _lastLogLine = line;

    while (output.children.length > 5000) {
        output.removeChild(output.firstChild);
    }
    output.scrollTop = output.scrollHeight;
}

function toggleLogEvents() {
    _logShowEvents = !_logShowEvents;
    var btn = document.getElementById('log-events');
    if (btn) {
        btn.textContent = _logShowEvents ? 'Hide Events' : 'Raw';
        btn.className = _logShowEvents
            ? 'text-xs bg-orange-600 hover:bg-orange-500 text-white rounded px-2 py-1'
            : 'text-xs bg-slate-700 hover:bg-slate-600 text-white rounded px-2 py-1';
    }
}

function toggleLogPause() {
    _logPaused = !_logPaused;
    if (!_logPaused && _logBuffer.length > 0) {
        // Flush buffered lines
        for (var i = 0; i < _logBuffer.length; i++) {
            appendLogLine(_logBuffer[i]);
        }
        _logBuffer = [];
    }
    updatePauseButton();
}

function updatePauseButton() {
    var btn = document.getElementById('log-pause');
    if (!btn) return;
    if (_logPaused) {
        btn.textContent = 'Resume' + (_logBuffer.length > 0 ? ' (' + _logBuffer.length + ')' : '');
        btn.className = 'text-xs bg-orange-600 hover:bg-orange-500 text-white rounded px-2 py-1';
    } else {
        btn.textContent = 'Pause';
        btn.className = 'text-xs bg-slate-700 hover:bg-slate-600 text-white rounded px-2 py-1';
    }
}

function clearLogs() {
    var output = document.getElementById('log-output');
    if (output) output.innerHTML = '';
    _lastLogLine = null;
}

function reconnectLogs() {
    if (_logServerName) {
        appendLogLine('--- Reconnecting... ---');
        connectLogWS(_logServerName);
    }
}

// Game state WebSocket (players + killfeed) - renders from JSON client-side
function connectGameWS(serverName) {
    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/server/' + serverName + '/game/ws');

    ws.onmessage = function(e) {
        try {
            var data = JSON.parse(e.data);
            switch (data.type) {
                case 'players':
                    renderPlayers(data.players);
                    if (data.score) renderScore(data.score);
                    break;
                case 'killfeed':
                    // Full killfeed replace (initial load)
                    renderKillfeed(data.killfeed);
                    break;
                case 'kill':
                    // Incremental: append new kills
                    appendKills(data.kills);
                    break;
            }
        } catch(err) {}
    };

    ws.onclose = function() {
        setTimeout(function() { connectGameWS(serverName); }, 3000);
    };
}

function renderScore(score) {
    var bar = document.getElementById('score-bar');
    if (!bar) return;
    document.getElementById('score-ct').textContent = score.ct;
    document.getElementById('score-t').textContent = score.t;
    document.getElementById('score-round').textContent = score.round;
    var modeEl = document.getElementById('score-mode');
    if (modeEl && score.mode) {
        modeEl.textContent = score.mode.charAt(0).toUpperCase() + score.mode.slice(1);
    }
    bar.classList.remove('hidden');
}

function renderPlayers(players) {
    var el = document.getElementById('player-list');
    if (!el) return;

    if (!players.length) {
        el.innerHTML = '<div class="px-4 py-8 text-center text-slate-500 text-sm">No players connected</div>';
        return;
    }

    var html = '<table class="w-full text-sm"><thead><tr class="border-b border-slate-700 text-slate-400 text-left">' +
        '<th class="px-4 py-2 font-medium w-8">Team</th>' +
        '<th class="px-4 py-2 font-medium">Name</th>' +
        '<th class="px-4 py-2 font-medium text-center">K</th>' +
        '<th class="px-4 py-2 font-medium text-center">D</th>' +
        '<th class="px-4 py-2 font-medium text-center">A</th>' +
        '<th class="px-4 py-2 font-medium text-center">K/D</th>' +
        '<th class="px-4 py-2 font-medium text-right">Money</th>' +
        '<th class="px-4 py-2 font-medium">Equipment</th>' +
        '<th class="px-4 py-2 font-medium">Ping</th>' +
        '</tr></thead><tbody>';

    for (var i = 0; i < players.length; i++) {
        var p = players[i];
        var opacity = p.online ? '' : ' opacity-50';
        var teamBadge = p.team === 'CT'
            ? '<span class="inline-block px-1.5 py-0.5 rounded text-xs font-bold bg-blue-500/20 text-blue-400">CT</span>'
            : p.team === 'T'
            ? '<span class="inline-block px-1.5 py-0.5 rounded text-xs font-bold bg-yellow-500/20 text-yellow-400">T</span>'
            : '<span class="text-slate-600 text-xs">-</span>';

        var name = esc(p.name);
        if (p.bot) name = '<span class="text-slate-400">(BOT)</span> ' + name;
        if (!p.online) name += ' <span class="text-slate-500 text-xs">(offline)</span>';

        var kd = p.d === 0 ? '-' : (p.k / p.d).toFixed(1);

        // Money
        var money = p.money ? '$' + p.money.toLocaleString() : '-';

        // Equipment: armor/helmet/defuser indicators + weapons + grenades
        var equip = '';
        if (p.armor) equip += '<span class="inline-block px-1 py-0.5 rounded text-xs bg-sky-900/50 text-sky-400" title="Kevlar">' + (p.helmet ? 'K+H' : 'K') + '</span> ';
        if (p.defuser) equip += '<span class="inline-block px-1 py-0.5 rounded text-xs bg-purple-900/50 text-purple-400" title="Defuse Kit">D</span> ';
        if (p.weapons) {
            for (var w = 0; w < p.weapons.length; w++) {
                equip += '<span class="inline-block px-1.5 py-0.5 rounded text-xs bg-slate-700 text-slate-300">' + esc(p.weapons[w]) + '</span> ';
            }
        }
        if (p.grenades) {
            for (var g = 0; g < p.grenades.length; g++) {
                equip += '<span class="inline-block px-1.5 py-0.5 rounded text-xs bg-emerald-900/50 text-emerald-400">' + esc(p.grenades[g]) + '</span> ';
            }
        }

        var ping = !p.online ? '-' : (p.bot ? '-' : p.ping + 'ms');

        html += '<tr class="border-b border-slate-700/50' + opacity + '">' +
            '<td class="px-4 py-2">' + teamBadge + '</td>' +
            '<td class="px-4 py-2 text-white">' + name + '</td>' +
            '<td class="px-4 py-2 text-green-400 text-center">' + p.k + '</td>' +
            '<td class="px-4 py-2 text-red-400 text-center">' + p.d + '</td>' +
            '<td class="px-4 py-2 text-yellow-400 text-center">' + p.a + '</td>' +
            '<td class="px-4 py-2 text-slate-300 text-center">' + kd + '</td>' +
            '<td class="px-4 py-2 text-green-300 text-right font-mono text-xs">' + money + '</td>' +
            '<td class="px-4 py-2"><div class="flex flex-wrap gap-1">' + equip + '</div></td>' +
            '<td class="px-4 py-2 text-slate-300">' + ping + '</td>' +
            '</tr>';
    }
    html += '</tbody></table>';
    el.innerHTML = html;
}

function renderKillfeed(killfeed) {
    var el = document.getElementById('killfeed');
    if (!el) return;

    if (!killfeed.length) {
        el.innerHTML = '<div class="px-4 py-8 text-center text-slate-500 text-sm">No kills yet</div>';
        return;
    }

    var html = '<div class="space-y-1 p-4 text-sm killfeed-inner">';
    for (var i = 0; i < killfeed.length; i++) {
        var k = killfeed[i];
        if (k.sys) {
            html += '<div class="flex items-center gap-2 py-1">' +
                '<span class="flex-1 border-t border-slate-600"></span>' +
                '<span class="text-orange-400 text-xs font-medium">' + esc(k.msg) + '</span>' +
                '<span class="flex-1 border-t border-slate-600"></span>' +
                '<span class="text-slate-600 text-xs">' + esc(k.time) + '</span>' +
                '</div>';
        } else {
            html += '<div class="flex items-center gap-2">' +
                '<span class="text-white">' + esc(k.killer) + '</span>' +
                '<span class="text-slate-500 text-xs">' + esc(k.weapon) + (k.hs ? ' HS' : '') + '</span>' +
                '<span class="text-red-400">' + esc(k.victim) + '</span>' +
                '<span class="text-slate-600 text-xs ml-auto">' + esc(k.time) + '</span>' +
                '</div>';
        }
    }
    html += '</div>';
    el.innerHTML = html;
}

function appendKills(kills) {
    var el = document.getElementById('killfeed');
    if (!el) return;

    // Find or create the inner container
    var container = el.querySelector('.killfeed-inner');
    if (!container) {
        // First kill — replace "No kills yet" placeholder
        el.innerHTML = '<div class="space-y-1 p-4 text-sm killfeed-inner"></div>';
        container = el.querySelector('.killfeed-inner');
    }

    for (var i = 0; i < kills.length; i++) {
        var k = kills[i];
        var div = document.createElement('div');
        if (k.sys) {
            div.className = 'flex items-center gap-2 py-1';
            div.innerHTML = '<span class="flex-1 border-t border-slate-600"></span>' +
                '<span class="text-orange-400 text-xs font-medium">' + esc(k.msg) + '</span>' +
                '<span class="flex-1 border-t border-slate-600"></span>' +
                '<span class="text-slate-600 text-xs">' + esc(k.time) + '</span>';
        } else {
            div.className = 'flex items-center gap-2';
            div.innerHTML = '<span class="text-white">' + esc(k.killer) + '</span>' +
                '<span class="text-slate-500 text-xs">' + esc(k.weapon) + (k.hs ? ' HS' : '') + '</span>' +
                '<span class="text-red-400">' + esc(k.victim) + '</span>' +
                '<span class="text-slate-600 text-xs ml-auto">' + esc(k.time) + '</span>';
        }
        // Insert at top (newest first)
        container.insertBefore(div, container.firstChild);
    }

    // Cap at 20 entries
    while (container.children.length > 20) {
        container.removeChild(container.lastChild);
    }
}

function esc(s) {
    if (!s) return '';
    var d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
}

// RCON autocomplete
var _rconCommands = [
    // Server management
    'status', 'stats', 'quit', 'restart', 'mp_restartgame 1',
    // Map
    'changelevel de_dust2', 'changelevel de_inferno', 'changelevel de_mirage',
    'changelevel de_nuke', 'changelevel de_overpass', 'changelevel de_ancient',
    'changelevel de_anubis', 'changelevel de_vertigo', 'changelevel de_train',
    'changelevel', 'maps *',
    // Players
    'kick', 'kickid', 'banid', 'users',
    // Bots
    'bot_add', 'bot_add_ct', 'bot_add_t', 'bot_kick', 'bot_kill',
    'bot_difficulty 0', 'bot_difficulty 1', 'bot_difficulty 2', 'bot_difficulty 3',
    'bot_quota', 'bot_stop 1', 'bot_stop 0', 'bot_knives_only',
    // Match
    'mp_restartgame 1', 'mp_warmup_end', 'mp_warmup_start',
    'mp_warmuptime', 'mp_maxrounds', 'mp_overtime_enable 1',
    'mp_halftime_pausetimer 1', 'mp_match_can_clinch 1',
    // Gameplay
    'mp_roundtime', 'mp_roundtime_defuse', 'mp_freezetime',
    'mp_buytime', 'mp_buy_anywhere 1', 'mp_startmoney',
    'mp_free_armor 1', 'mp_free_armor 0',
    'mp_death_drop_gun 1', 'mp_death_drop_grenade 1',
    'sv_cheats 1', 'sv_cheats 0', 'noclip', 'god',
    'give weapon_ak47', 'give weapon_awp', 'give weapon_m4a1',
    'mp_autoteambalance 0', 'mp_autoteambalance 1',
    'mp_limitteams 0',
    // Economy
    'mp_startmoney 16000', 'mp_startmoney 800',
    'mp_afterroundmoney 0', 'cash_team_bonus_shorthanded 0',
    // Logging
    'log on', 'log off', 'sv_logecho 1', 'sv_logecho 0', 'mp_logdetail 3',
    // Practice
    'sv_infinite_ammo 1', 'sv_infinite_ammo 0',
    'sv_grenade_trajectory_prac_pipreview 1',
    'mp_roundtime_defuse 60', 'mp_freezetime 0', 'mp_buytime 99999',
    'sv_showimpacts 1', 'sv_showimpacts 0',
    // Pause
    'mp_pause_match', 'mp_unpause_match', 'pause', 'unpause',
    // Say
    'say', 'say_team',
    // Exec
    'exec', 'exec gamemode_competitive', 'exec gamemode_casual', 'exec gamemode_deathmatch',
];
var _rconHistory = [];
var _rconSelectedIdx = -1;

function initRconAutocomplete() {
    var input = document.getElementById('rcon-input');
    var suggestions = document.getElementById('rcon-suggestions');
    if (!input || !suggestions) return;

    input.addEventListener('input', function() {
        var val = input.value.trim().toLowerCase();
        if (val.length === 0) {
            suggestions.classList.add('hidden');
            return;
        }
        var matches = getMatches(val);
        if (matches.length === 0) {
            suggestions.classList.add('hidden');
            return;
        }
        renderSuggestions(matches);
        suggestions.classList.remove('hidden');
        _rconSelectedIdx = -1;
    });

    input.addEventListener('keydown', function(e) {
        var items = suggestions.querySelectorAll('.rcon-suggestion');
        if (e.key === 'ArrowDown') {
            e.preventDefault();
            _rconSelectedIdx = Math.min(_rconSelectedIdx + 1, items.length - 1);
            updateSelection(items);
        } else if (e.key === 'ArrowUp') {
            e.preventDefault();
            _rconSelectedIdx = Math.max(_rconSelectedIdx - 1, -1);
            updateSelection(items);
        } else if (e.key === 'Tab' || (e.key === 'Enter' && _rconSelectedIdx >= 0)) {
            if (_rconSelectedIdx >= 0 && items.length > 0) {
                e.preventDefault();
                input.value = items[_rconSelectedIdx].dataset.cmd;
                suggestions.classList.add('hidden');
                _rconSelectedIdx = -1;
                if (e.key === 'Tab') input.focus();
            }
        } else if (e.key === 'Escape') {
            suggestions.classList.add('hidden');
            _rconSelectedIdx = -1;
        }
    });

    // Hide on blur (slight delay so clicks on suggestions register)
    input.addEventListener('blur', function() {
        setTimeout(function() { suggestions.classList.add('hidden'); }, 150);
    });
}

function getMatches(query) {
    var seen = {};
    var results = [];
    // History first (most recent)
    for (var i = _rconHistory.length - 1; i >= 0; i--) {
        var cmd = _rconHistory[i];
        if (cmd.toLowerCase().indexOf(query) === 0 && !seen[cmd]) {
            seen[cmd] = true;
            results.push({cmd: cmd, isHistory: true});
        }
    }
    // Then built-in commands
    for (var j = 0; j < _rconCommands.length; j++) {
        var c = _rconCommands[j];
        if (c.toLowerCase().indexOf(query) === 0 && !seen[c]) {
            seen[c] = true;
            results.push({cmd: c, isHistory: false});
        }
    }
    return results.slice(0, 12);
}

function renderSuggestions(matches) {
    var suggestions = document.getElementById('rcon-suggestions');
    suggestions.innerHTML = '';
    for (var i = 0; i < matches.length; i++) {
        var div = document.createElement('div');
        div.className = 'rcon-suggestion px-3 py-1.5 text-sm text-slate-300 hover:bg-slate-600 cursor-pointer flex items-center justify-between';
        div.dataset.cmd = matches[i].cmd;
        div.textContent = matches[i].cmd;
        if (matches[i].isHistory) {
            var badge = document.createElement('span');
            badge.className = 'text-xs text-slate-500';
            badge.textContent = 'recent';
            div.appendChild(badge);
        }
        div.addEventListener('mousedown', function(e) {
            e.preventDefault();
            var input = document.getElementById('rcon-input');
            input.value = this.dataset.cmd;
            document.getElementById('rcon-suggestions').classList.add('hidden');
            input.focus();
        });
        suggestions.appendChild(div);
    }
}

function updateSelection(items) {
    for (var i = 0; i < items.length; i++) {
        if (i === _rconSelectedIdx) {
            items[i].classList.add('bg-slate-600');
        } else {
            items[i].classList.remove('bg-slate-600');
        }
    }
}

// RCON: clear input after submit, save to history, scroll output
document.addEventListener('htmx:afterRequest', function(e) {
    if (e.detail.elt.id === 'rcon-form') {
        var input = e.detail.elt.querySelector('input[name=command]');
        if (input && input.value.trim()) {
            // Add to history (dedup)
            var cmd = input.value.trim();
            var idx = _rconHistory.indexOf(cmd);
            if (idx >= 0) _rconHistory.splice(idx, 1);
            _rconHistory.push(cmd);
            if (_rconHistory.length > 50) _rconHistory.shift();
        }
        if (input) input.value = '';
        var output = document.getElementById('rcon-output');
        if (output) output.scrollTop = output.scrollHeight;
        document.getElementById('rcon-suggestions').classList.add('hidden');
    }
});

document.addEventListener('DOMContentLoaded', function() {
    initRconAutocomplete();
});
