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

// Dashboard WebSocket
function connectDashboardWS() {
    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/api/dashboard/ws');

    ws.onmessage = function(e) {
        try {
            var data = JSON.parse(e.data);
            if (data.type === 'dashboard') renderDashboard(data.servers);
        } catch(err) {}
    };

    ws.onclose = function() {
        setTimeout(connectDashboardWS, 3000);
    };
}

function renderDashboard(servers) {
    var el = document.getElementById('dashboard-servers');
    if (!el) return;

    if (!servers || !servers.length) {
        el.innerHTML = '<div class="bg-slate-800 border border-slate-700 rounded-lg px-4 py-12 text-center text-slate-500">' +
            'No servers running. <a href="/launch" class="text-orange-400 hover:underline">Launch one</a>.</div>';
        return;
    }

    var html = '';
    for (var i = 0; i < servers.length; i++) {
        var s = servers[i];
        var statusText = s.status === 'running'
            ? '<span class="text-green-400 text-xs">Running</span>'
            : '<span class="text-slate-400 text-xs">' + s.status + '</span>';

        var mapName = s.map || '-';
        var scoreHtml = '';
        if (s.score && (s.score.round > 0 || s.score.ct > 0 || s.score.t > 0)) {
            scoreHtml = '<div class="flex items-center gap-2 text-xs">' +
                '<span class="text-blue-400 font-bold">CT ' + s.score.ct + '</span>' +
                '<span class="text-slate-500">-</span>' +
                '<span class="text-yellow-400 font-bold">' + s.score.t + ' T</span>' +
                '<span class="text-slate-500">R' + s.score.round + '</span>' +
                '</div>';
        }

        var modeLabel = s.mode ? s.mode.charAt(0).toUpperCase() + s.mode.slice(1) : '-';

        // Card layout (works on all screen sizes)
        html += '<a href="/server/' + s.name + '" class="block bg-slate-800 border border-slate-700 rounded-lg p-4 hover:bg-slate-700/50 transition-colors">' +
            '<div class="flex items-center justify-between mb-2">' +
                '<div class="flex items-center gap-2">' +
                    '<span class="text-orange-400 font-medium">' + (s.alias || s.name) + '</span>' +
                    '<span class="text-slate-500 text-xs">:' + s.port + '</span>' +
                '</div>' +
                '<div class="flex items-center gap-3">' +
                    statusText +
                '</div>' +
            '</div>' +
            '<div class="flex items-center justify-between">' +
                '<div class="flex items-center gap-3 text-xs text-slate-300">' +
                    '<span>' + modeLabel + '</span>' +
                    '<span class="flex items-center gap-1"><img src="/static/icons/map/' + mapName + '.svg" class="h-4 w-4 opacity-60 rounded" onerror="this.style.display=\'none\'">' + mapName + '</span>' +
                    '<span>' + s.playerCount + '/' + s.maxPlayers + ' players</span>' +
                '</div>' +
                scoreHtml +
            '</div>' +
            '</a>';
    }
    el.innerHTML = html;
}

var _currentGameMode = '';
var _inWarmup = true;
var _statMode = 0; // 0 = K/D/A/MVP, 1 = HS%/KDR/ADR/EF/UD
var _statModeLabels = ['Kills Deaths Assists MVPs', 'HS% KDR ADR UD EF'];

// Game state WebSocket (players + killfeed) - renders from JSON client-side
function connectGameWS(serverName) {
    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/server/' + serverName + '/game/ws');

    ws.onmessage = function(e) {
        try {
            var data = JSON.parse(e.data);
            switch (data.type) {
                case 'players':
                    if (data.score) renderScore(data.score);
                    renderPlayers(data.players);
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
        _currentGameMode = score.mode;
    }
    var mapEl = document.getElementById('score-map');
    if (mapEl && score.map) {
        mapEl.innerHTML = '<img src="/static/icons/map/' + score.map + '.svg" class="h-4 w-4 opacity-60 rounded" onerror="this.style.display=\'none\'">' + score.map;
    }
    _inWarmup = !!score.warmup;

    // Round history bar
    var histEl = document.getElementById('round-history');
    if (!histEl || !score.rounds || !score.rounds.length) return;
    histEl.classList.remove('hidden');

    var html = '';
    for (var i = 0; i < score.rounds.length; i++) {
        var r = score.rounds[i];
        // Insert half-time divider
        if (score.half > 0 && i > 0 && score.rounds[i - 1].r <= score.half && r.r > score.half) {
            html += '<span class="w-px h-6 bg-slate-500 mx-1"></span>';
        }

        var color = r.w === 'CT' ? 'ct' : 't';
        var icon = '';
        switch (r.rs) {
            case 'elimination':
                icon = '/static/icons/ui/kill.svg';
                break;
            case 'bomb':
                icon = '/static/icons/equipment/planted_c4.svg';
                break;
            case 'defuse':
                icon = '/static/icons/equipment/defuser.svg';
                break;
            case 'time':
                icon = '/static/icons/ui/timer.svg';
                break;
        }

        // White SVGs — use CSS filter to colorize: blue for CT, gold for T
        var filter = color === 'ct'
            ? 'filter: brightness(0) saturate(100%) invert(55%) sepia(90%) saturate(500%) hue-rotate(190deg);'
            : 'filter: brightness(0) saturate(100%) invert(70%) sepia(90%) saturate(400%) hue-rotate(5deg);';
        html += '<img src="' + icon + '" class="h-5 w-5" style="' + filter + '" title="Round ' + r.r + ': ' + r.w + ' (' + r.rs + ')">';
    }
    histEl.innerHTML = html;
}

function playerTeamBadge(team) {
    if (team === 'CT') return '<span class="inline-block px-1.5 py-0.5 rounded text-xs font-bold bg-blue-500/20 text-blue-400">CT</span>';
    if (team === 'T') return '<span class="inline-block px-1.5 py-0.5 rounded text-xs font-bold bg-yellow-500/20 text-yellow-400">T</span>';
    return '<span class="text-slate-600 text-xs">-</span>';
}

function playerEquipIcons(p) {
    var html = '';
    if (p.bomb) html += '<img src="/static/icons/equipment/c4.svg" class="h-5 opacity-80" title="C4">';
    if (p.helmet) html += '<img src="/static/icons/equipment/helmet.svg" class="h-5 opacity-80" title="Helmet">';
    if (p.armor) html += '<img src="/static/icons/equipment/kevlar.svg" class="h-5 opacity-80" title="Kevlar">';
    if (p.defuser) html += '<img src="/static/icons/equipment/defuser.svg" class="h-5 opacity-80" title="Defuse Kit">';
    if (p.weapons) {
        for (var w = 0; w < p.weapons.length; w++) {
            html += weaponIcon(p.weapons[w]);
        }
    }
    if (p.grenades) {
        for (var g = 0; g < p.grenades.length; g++) {
            html += '<img src="/static/icons/equipment/' + p.grenades[g] + '.svg" class="h-4 opacity-60" title="' + p.grenades[g] + '" onerror="this.style.display=\'none\'">';
        }
    }
    return html;
}

function renderPlayers(players) {
    _lastPlayers = players;
    var el = document.getElementById('player-list');
    if (!el) return;

    // Update player count and stat toggle visibility
    var countEl = document.getElementById('player-count');
    if (countEl) {
        var online = 0;
        for (var c = 0; c < players.length; c++) { if (players[c].online) online++; }
        countEl.textContent = '(' + online + ')';
    }
    var toggleEl = document.getElementById('stat-toggle');
    if (toggleEl) {
        toggleEl.style.display = (_currentGameMode === 'armsrace') ? 'none' : '';
    }

    if (!players.length) {
        el.innerHTML = '<div class="px-4 py-8 text-center text-slate-500 text-sm">No players connected</div>';
        return;
    }

    // Split players by team
    var ctPlayers = [], tPlayers = [], otherPlayers = [];
    for (var i = 0; i < players.length; i++) {
        if (players[i].team === 'CT') ctPlayers.push(players[i]);
        else if (players[i].team === 'T') tPlayers.push(players[i]);
        else otherPlayers.push(players[i]);
    }

    var showAlive = !_inWarmup && _currentGameMode && _currentGameMode !== 'deathmatch' && _currentGameMode !== 'armsrace';

    var isArmsRace = _currentGameMode === 'armsrace';

    if (isArmsRace) {
        // Sort by level descending, then kills descending
        players.sort(function(a, b) {
            function arLvl(p) { return Math.max(0, Math.floor((p.k - (p.knifek || 0)) / 2) + (p.knifek || 0) - (p.knifed || 0)); }
            var la = arLvl(a), lb = arLvl(b);
            if (la !== lb) return lb - la;
            return b.k - a.k;
        });
    }
    var statHeaders, statCols, extraHeaders, extraCols;

    if (isArmsRace) {
        // Arms race: K D A KDR HS% ZeusK KnifeK Lvl — no money or equipment
        statHeaders = '<th class="px-4 py-2 font-medium text-center" title="Kills">K</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Deaths">D</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Assists">A</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Kill/Death Ratio">KDR</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Headshot Percentage">HS%</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Zeus Kills">Zeus</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Knife Kills">Knife</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Level (every 2 kills)">Lvl</th>';
        statCols = '<col style="width:50px"><col style="width:50px"><col style="width:50px"><col style="width:60px"><col style="width:65px"><col style="width:55px"><col style="width:55px"><col style="width:50px">';
        extraHeaders = '';
        extraCols = '';
    } else if (_statMode === 0) {
        statHeaders = '<th class="px-4 py-2 font-medium text-center" title="Kills">K</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Deaths">D</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Assists">A</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Most Valuable Player">MVPs</th>';
        statCols = '<col style="width:50px"><col style="width:50px"><col style="width:50px"><col style="width:50px">';
        extraHeaders = '<th class="px-4 py-2 font-medium text-right">Money</th><th class="px-4 py-2 font-medium">Equipment</th>';
        extraCols = '<col style="width:80px"><col>';
    } else {
        statHeaders = '<th class="px-4 py-2 font-medium text-center" title="Headshot Percentage">HS%</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Kill/Death Ratio">KDR</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Average Damage per Round">ADR</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Enemies Flashed">EF</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Utility Damage">UD</th>';
        statCols = '<col style="width:75px"><col style="width:60px"><col style="width:60px"><col style="width:50px"><col style="width:50px">';
        extraHeaders = '<th class="px-4 py-2 font-medium text-right">Money</th><th class="px-4 py-2 font-medium">Equipment</th>';
        extraCols = '<col style="width:80px"><col>';
    }

    var tableHeader = '<colgroup><col>' + statCols + extraCols +
        '<col style="width:60px"></colgroup>' +
        '<tr class="border-b border-slate-700 text-slate-400 text-left">' +
        '<th class="px-4 py-2 font-medium">Name</th>' +
        statHeaders + extraHeaders +
        '<th class="px-4 py-2 font-medium">Ping</th>' +
        '</tr>';

    function statCells(p) {
        if (isArmsRace) {
            var lvl = Math.max(0, Math.floor((p.k - (p.knifek || 0)) / 2) + (p.knifek || 0) - (p.knifed || 0));
            return '<td class="px-4 py-2 text-green-400 text-center">' + p.k + '</td>' +
                '<td class="px-4 py-2 text-red-400 text-center">' + p.d + '</td>' +
                '<td class="px-4 py-2 text-yellow-400 text-center">' + p.a + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.zeusk || 0) + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.knifek || 0) + '</td>' +
                '<td class="px-4 py-2 text-orange-400 text-center font-medium">' + lvl + '</td>';
        }
        if (_statMode === 0) {
            return '<td class="px-4 py-2 text-green-400 text-center">' + p.k + '</td>' +
                '<td class="px-4 py-2 text-red-400 text-center">' + p.d + '</td>' +
                '<td class="px-4 py-2 text-yellow-400 text-center">' + p.a + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.mvp || 0) + '</td>';
        }
        return '<td class="px-4 py-2 text-slate-300 text-center">' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</td>' +
            '<td class="px-4 py-2 text-slate-300 text-center">' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</td>' +
            '<td class="px-4 py-2 text-slate-300 text-center">' + (p.adr ? p.adr.toFixed(0) : '-') + '</td>' +
            '<td class="px-4 py-2 text-slate-300 text-center">' + (p.ef || 0) + '</td>' +
            '<td class="px-4 py-2 text-slate-300 text-center">' + (p.ud ? p.ud.toFixed(0) : '0') + '</td>';
    }

    function extraCells(p) {
        if (isArmsRace) return '';
        var money = p.money ? '$' + p.money.toLocaleString() : '';
        var equip = playerEquipIcons(p);
        return '<td class="px-4 py-2 text-green-300 text-right font-mono text-xs">' + money + '</td>' +
            '<td class="px-4 py-2"><div class="flex flex-wrap items-center gap-1.5">' + equip + '</div></td>';
    }

    function playerRow(p) {
        var isDead = showAlive && p.online && !p.alive;
        var opacity = !p.online ? ' opacity-50' : (isDead ? ' opacity-40' : '');
        var name = p.name;
        if (p.bot) name = '<span class="text-slate-400">(BOT)</span> ' + name;
        if (!p.online) name += ' <span class="text-slate-500 text-xs">(offline)</span>';
        if (isDead) name += ' <img src="/static/icons/ui/kill.svg" class="h-3.5 inline-block opacity-50" title="Dead">';
        var ping = !p.online ? '-' : (p.bot ? '-' : p.ping + 'ms');
        var rowClasses = isArmsRace ? '' : 'border-b border-slate-700/50';
        var nameColor = isArmsRace ? teamColor(p.team) : 'text-white';
        return '<tr class="' + rowClasses + opacity + '">' +
            '<td class="px-4 py-2 ' + nameColor + '">' + name + '</td>' +
            statCells(p) + extraCells(p) +
            '<td class="px-4 py-2 text-slate-300">' + ping + '</td>' +
            '</tr>';
    }

    function playerCard(p) {
        var isDead = showAlive && p.online && !p.alive;
        var opacity = !p.online ? ' opacity-50' : (isDead ? ' opacity-40' : '');
        var name = p.name;
        if (p.bot) name = '<span class="text-slate-400">(BOT)</span> ' + name;
        if (!p.online) name += ' <span class="text-slate-500 text-xs">(offline)</span>';
        if (isDead) name += ' <img src="/static/icons/ui/kill.svg" class="h-3.5 inline-block opacity-50" title="Dead">';
        var money = !isArmsRace && p.money ? '$' + p.money.toLocaleString() : '';
        var ping = !p.online ? '-' : (p.bot ? '-' : p.ping + 'ms');
        var equip = isArmsRace ? '' : playerEquipIcons(p);
        var borderColor = isArmsRace ? '' : (p.team === 'CT' ? ' border-l-2 border-blue-400' : (p.team === 'T' ? ' border-l-2 border-yellow-400' : ''));
        var cardNameColor = isArmsRace ? teamColor(p.team) : 'text-white';

        var statsHtml;
        if (isArmsRace) {
            statsHtml = '<span class="text-green-400">K: ' + p.k + '</span>' +
                '<span class="text-red-400">D: ' + p.d + '</span>' +
                '<span class="text-yellow-400">A: ' + p.a + '</span>' +
                '<span class="text-slate-300">KDR: ' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</span>' +
                '<span class="text-slate-300">HS: ' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</span>' +
                '<span class="text-slate-300">Zeus: ' + (p.zeusk || 0) + '</span>' +
                '<span class="text-slate-300">Knife: ' + (p.knifek || 0) + '</span>' +
                '<span class="text-orange-400">Lvl: ' + Math.max(0, Math.floor((p.k - (p.knifek || 0)) / 2) + (p.knifek || 0) - (p.knifed || 0)) + '</span>';
        } else if (_statMode === 0) {
            statsHtml = '<span class="text-green-400">K: ' + p.k + '</span>' +
                '<span class="text-red-400">D: ' + p.d + '</span>' +
                '<span class="text-yellow-400">A: ' + p.a + '</span>' +
                '<span class="text-slate-300">MVP: ' + (p.mvp || 0) + '</span>';
        } else {
            statsHtml = '<span class="text-slate-300">HS: ' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</span>' +
                '<span class="text-slate-300">KDR: ' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</span>' +
                '<span class="text-slate-300">ADR: ' + (p.adr ? p.adr.toFixed(0) : '-') + '</span>' +
                '<span class="text-slate-300">EF: ' + (p.ef || 0) + '</span>' +
                '<span class="text-slate-300">UD: ' + (p.ud ? p.ud.toFixed(0) : '0') + '</span>';
        }

        return '<div class="bg-slate-700/30 rounded-lg p-3' + opacity + borderColor + '">' +
            '<div class="flex items-center gap-2 mb-1.5">' +
                '<span class="' + cardNameColor + ' text-sm font-medium flex-1">' + name + '</span>' +
                (money ? '<span class="text-green-300 font-mono text-xs">' + money + '</span>' : '') +
                '<span class="text-slate-400 text-xs">' + ping + '</span>' +
            '</div>' +
            '<div class="flex items-center gap-3 mb-2 text-xs">' + statsHtml + '</div>' +
            (equip ? '<div class="flex flex-wrap items-center gap-1.5">' + equip + '</div>' : '') +
            '</div>';
    }

    // Desktop: single table
    var html = '<table class="w-full text-sm hidden sm:table" style="table-layout:fixed">' + tableHeader + '<tbody>';

    var totalCols = isArmsRace ? 10 : (_statMode === 0 ? 8 : 9);

    if (isArmsRace) {
        // Flat list sorted by score, team indicated by left border on each row
        for (var i = 0; i < players.length; i++) html += playerRow(players[i]);
    } else {
        if (ctPlayers.length) {
            html += '<tr><td colspan="' + totalCols + '" class="px-4 py-1.5 text-xs font-bold text-blue-400 bg-blue-500/5 border-l-2 border-blue-400">Counter-Terrorists</td></tr>';
            for (var i = 0; i < ctPlayers.length; i++) html += playerRow(ctPlayers[i]);
        }

        if (tPlayers.length) {
            html += '<tr><td colspan="' + totalCols + '" class="px-4 py-1.5 text-xs font-bold text-yellow-400 bg-yellow-500/5 border-l-2 border-yellow-400">Terrorists</td></tr>';
            for (var i = 0; i < tPlayers.length; i++) html += playerRow(tPlayers[i]);
        }

        if (otherPlayers.length) {
            for (var i = 0; i < otherPlayers.length; i++) html += playerRow(otherPlayers[i]);
        }
    }

    html += '</tbody></table>';

    // Mobile cards
    html += '<div class="sm:hidden space-y-2 p-3">';
    if (isArmsRace) {
        for (var i = 0; i < players.length; i++) html += playerCard(players[i]);
    } else {
        if (ctPlayers.length) {
            html += '<div class="text-xs font-bold text-blue-400 px-1 pb-1">Counter-Terrorists</div>';
            for (var i = 0; i < ctPlayers.length; i++) html += playerCard(ctPlayers[i]);
        }
        if (tPlayers.length) {
            if (ctPlayers.length) html += '<div class="border-t border-slate-700 my-2"></div>';
            html += '<div class="text-xs font-bold text-yellow-400 px-1 pb-1">Terrorists</div>';
            for (var i = 0; i < tPlayers.length; i++) html += playerCard(tPlayers[i]);
        }
        if (otherPlayers.length) {
            for (var i = 0; i < otherPlayers.length; i++) html += playerCard(otherPlayers[i]);
        }
    }
    html += '</div>';

    el.innerHTML = html;
}

function renderKillfeed(killfeed) {
    var el = document.getElementById('killfeed');
    if (!el) return;

    if (!killfeed.length) {
        el.innerHTML = '<div class="px-4 py-8 text-center text-slate-500 text-sm">No kills yet</div>';
        return;
    }

    var html = '<div class="space-y-1.5 p-4 text-sm killfeed-inner">';
    for (var i = 0; i < killfeed.length; i++) {
        html += renderKillEntry(killfeed[i]);
    }
    html += '</div>';
    el.innerHTML = html;
}

var _weaponAliases = {'c4': 'planted_c4', 'weapon_c4': 'planted_c4'};

function weaponIcon(weapon) {
    if (!weapon) return '';
    var file = _weaponAliases[weapon] || weapon;
    return '<img src="/static/icons/equipment/' + file + '.svg" alt="' + weapon + '" class="h-4 inline-block opacity-80" onerror="this.style.display=\'none\'">';
}

function teamColor(team) {
    if (team === 'CT') return 'text-blue-400';
    if (team === 'T') return 'text-yellow-400';
    return 'text-white';
}

function renderKillEntry(k) {
    if (k.sys) {
        return '<div class="flex items-center gap-2 py-1">' +
            '<span class="flex-1 border-t border-slate-600"></span>' +
            '<span class="text-orange-400 text-xs font-medium">' + k.msg + '</span>' +
            '<span class="flex-1 border-t border-slate-600"></span>' +
            '<span class="text-slate-600 text-xs">' + k.time + '</span>' +
            '</div>';
    }
    // No killer (bomb kill, world kill)
    if (!k.killer) {
        return '<div class="flex items-center gap-2">' +
            '<span class="flex items-center gap-1">' + weaponIcon(k.weapon) + '</span>' +
            '<span class="' + teamColor(k.vt) + ' text-xs">' + k.victim + '</span>' +
            '<span class="text-slate-600 text-xs ml-auto">' + k.time + '</span>' +
            '</div>';
    }
    if (k.killer && k.killer === k.victim) {
        var tc = teamColor(k.vt);
        return '<div class="flex items-center gap-2">' +
            '<span class="' + tc + ' text-xs">' + k.victim + '</span>' +
            '<img src="/static/icons/deathnotice/icon_suicide.svg" class="h-4 inline-block opacity-80" alt="suicide">' +
            '<span class="flex items-center gap-1">' + weaponIcon(k.weapon) + '</span>' +
            '<span class="text-slate-600 text-xs ml-auto">' + k.time + '</span>' +
            '</div>';
    }
    function dnIcon(src, alt) { return '<img src="/static/icons/deathnotice/' + src + '" class="h-3.5 inline-block opacity-80" alt="' + alt + '">'; }

    // [blind] killer + [flash assist] assist [in air] weapon [noscope] [through smoke] [wallbang] [headshot] victim
    var blindIcon = k.bk ? dnIcon('blind_kill.svg', 'Blind Kill') + ' ' : '';
    var flashAssistIcon = (k.assist && k.fa) ? '<img src="/static/icons/equipment/flashbang_assist.svg" class="h-3.5 inline-block opacity-80" alt="Flash Assist"> ' : '';
    var killerSide = blindIcon + '<span class="' + teamColor(k.kt) + ' text-xs">' + k.killer + '</span>';
    if (k.assist) {
        killerSide += '<span class="text-slate-500 text-xs"> + </span>' + flashAssistIcon + '<span class="' + teamColor(k.at) + ' text-xs">' + k.assist + '</span>';
    }

    var inAirIcon = k.ia ? dnIcon('inairkill.svg', 'In Air') + ' ' : '';
    var weaponHtml = inAirIcon + weaponIcon(k.weapon);
    if (k.ns) weaponHtml += ' ' + dnIcon('noscope.svg', 'Noscope');
    if (k.ts) weaponHtml += ' ' + dnIcon('smoke_kill.svg', 'Through Smoke');
    if (k.wb) weaponHtml += ' ' + dnIcon('penetrate.svg', 'Wallbang');
    if (k.hs) weaponHtml += ' ' + dnIcon('icon_headshot.svg', 'Headshot');

    return '<div class="flex items-center gap-2">' +
        '<span class="flex items-center gap-1">' + killerSide + '</span>' +
        '<span class="flex items-center gap-1">' + weaponHtml + '</span>' +
        '<span class="' + teamColor(k.vt) + ' text-xs">' + k.victim + '</span>' +
        '<span class="text-slate-600 text-xs ml-auto">' + k.time + '</span>' +
        '</div>';
}

function appendKills(kills) {
    var el = document.getElementById('killfeed');
    if (!el) return;

    var container = el.querySelector('.killfeed-inner');
    if (!container) {
        el.innerHTML = '<div class="space-y-1.5 p-4 text-sm killfeed-inner"></div>';
        container = el.querySelector('.killfeed-inner');
    }

    for (var i = 0; i < kills.length; i++) {
        var wrapper = document.createElement('div');
        wrapper.innerHTML = renderKillEntry(kills[i]);
        var entry = wrapper.firstChild;
        container.insertBefore(entry, container.firstChild);
    }

    // Cap at 20 entries
    while (container.children.length > 20) {
        container.removeChild(container.lastChild);
    }
}


// Map pools by game mode
var _mapPools = {
    competitive: ['de_ancient', 'de_anubis', 'de_dust2', 'de_inferno', 'de_mirage', 'de_nuke', 'de_overpass', 'de_vertigo'],
    casual: ['de_ancient', 'de_anubis', 'de_dust2', 'de_inferno', 'de_mirage', 'de_nuke', 'de_overpass', 'de_vertigo', 'cs_italy', 'cs_office'],
    deathmatch: ['de_ancient', 'de_anubis', 'de_dust2', 'de_inferno', 'de_mirage', 'de_nuke', 'de_overpass', 'de_vertigo'],
    armsrace: ['ar_baggage', 'ar_pool_day', 'ar_shoots'],
    demolition: ['ar_baggage', 'ar_pool_day', 'ar_shoots'],
    wingman: ['de_ancient', 'de_anubis', 'de_dust2', 'de_inferno', 'de_mirage', 'de_nuke', 'de_overpass', 'de_vertigo']
};
var _allMaps = ['de_ancient', 'de_anubis', 'de_dust2', 'de_inferno', 'de_mirage', 'de_nuke', 'de_overpass', 'de_vertigo',
    'cs_alpine', 'cs_italy', 'cs_office', 'ar_baggage', 'ar_pool_day', 'ar_shoots',
    'de_ancient_night', 'ar_shoots_night', 'de_poseidon', 'de_sanctum', 'de_stronghold', 'de_warden'];

// RCON autocomplete
var _rconCommands = [
    // Server management
    'status', 'stats', 'quit', 'restart', 'mp_restartgame 1',
    // Map
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
// Add changelevel for all maps
for (var mi = 0; mi < _allMaps.length; mi++) _rconCommands.push('changelevel ' + _allMaps[mi]);

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

var _lastPlayers = [];

function cycleStatMode() {
    _statMode = (_statMode + 1) % _statModeLabels.length;
    var btn = document.getElementById('stat-toggle');
    if (btn) btn.textContent = _statModeLabels[_statMode];
    if (_lastPlayers.length) renderPlayers(_lastPlayers);
}

function renameServer(name) {
    var current = document.getElementById('server-title').textContent;
    var alias = prompt('Rename server:', current);
    if (alias === null) return;
    fetch('/server/' + name + '/rename', {
        method: 'POST',
        headers: {'Content-Type': 'application/x-www-form-urlencoded'},
        body: 'alias=' + encodeURIComponent(alias)
    }).then(function() {
        document.getElementById('server-title').textContent = alias || name;
    });
}

document.addEventListener('DOMContentLoaded', function() {
    initRconAutocomplete();
});
