// Server status indicator (next to title)
function setServerStatus(status) {
    var el = document.getElementById('server-status');
    if (!el) return;
    if (!status) {
        el.classList.add('hidden');
        el.textContent = '';
        return;
    }
    el.classList.remove('hidden');
    el.className = 'text-xs font-medium rounded px-2 py-0.5';
    if (status === 'stopped') {
        el.className += ' bg-red-500/20 text-red-400';
        el.textContent = 'Stopped';
    } else if (status === 'restarting') {
        el.className += ' bg-orange-500/20 text-orange-400';
        el.textContent = 'Restarting\u2026';
    }
}

// Log viewer WebSocket
var _logServerName = null;
var _logPaused = false;
var _logBuffer = []; // buffer lines while paused
var _logShowEvents = false; // show game event lines in log viewer
var _logRetries = 0;
var _logMaxRetries = 3;

function connectLogWS(serverName) {
    if (_serverStopped) return;
    _logServerName = serverName;
    _logPaused = false;
    _logBuffer = [];
    _lastLogLine = null;
    updatePauseButton();

    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/admin/server/' + serverName + '/logs/ws');
    var output = document.getElementById('log-output');
    var status = document.getElementById('log-status');
    var reconnectBtn = document.getElementById('log-reconnect');
    var _gotError = false;

    if (reconnectBtn) reconnectBtn.classList.add('hidden');

    ws.onopen = function() {
        _logRetries = 0;
        if (status) {
            status.textContent = 'Connected';
            status.className = 'text-xs text-green-400';
        }
    };

    ws.onmessage = function(e) {
        // Server sent an error (e.g. container not found) — stop retrying
        if (typeof e.data === 'string' && e.data.indexOf('Error:') === 0) {
            _gotError = true;
            _logRetries = _logMaxRetries;
            if (status) {
                status.textContent = 'Disconnected';
                status.className = 'text-xs text-red-400';
            }
            if (reconnectBtn) reconnectBtn.classList.remove('hidden');
            return;
        }
        if (_logPaused) {
            _logBuffer.push(e.data);
            if (_logBuffer.length > 2000) _logBuffer.shift();
            updatePauseButton();
            return;
        }
        appendLogLine(e.data);
    };

    ws.onclose = function() {
        if (_serverStopped || _gotError) return;
        if (_logRetries < _logMaxRetries) {
            _logRetries++;
            if (status) {
                status.textContent = 'Reconnecting (' + _logRetries + '/' + _logMaxRetries + ')...';
                status.className = 'text-xs text-yellow-400';
            }
            setTimeout(function() { connectLogWS(serverName); }, 5000);
        } else {
            if (status) {
                status.textContent = 'Disconnected';
                status.className = 'text-xs text-red-400';
            }
            if (reconnectBtn) reconnectBtn.classList.remove('hidden');
        }
    };

    ws.onerror = function() {};
}

function resetLogRetries() {
    _logRetries = 0;
}

function setLogStatus(text, className) {
    var status = document.getElementById('log-status');
    if (status) {
        status.textContent = text;
        status.className = className;
    }
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

    // Strip timestamp prefix for dedup comparison (L MM/DD/YYYY - HH:MM:SS: )
    function stripLogTs(s) {
        if (s.length > 25 && s.charAt(0) === 'L' && s.charAt(1) === ' ') return s.substring(25);
        return s;
    }

    // Deduplicate consecutive identical lines (ignoring timestamps)
    if (_lastLogLine && stripLogTs(_lastLogLine._logText) === stripLogTs(text)) {
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
        resetLogRetries();
        appendLogLine('--- Reconnecting... ---');
        connectLogWS(_logServerName);
    }
}

// Dashboard WebSocket
var _dashWS = null;
var _dashRetries = 0;
var _lastDashJSON = '';
function connectDashboardWS() {
    if (_dashWS) { try { _dashWS.close(); } catch(e) {} }
    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/admin/api/dashboard/ws');
    _dashWS = ws;

    ws.onmessage = function(e) {
        try {
            _dashRetries = 0;
            var data = JSON.parse(e.data);
            if (data.type === 'dashboard') {
                    var key = JSON.stringify(data.servers) + JSON.stringify(data.host);
                    if (key !== _lastDashJSON) {
                        _lastDashJSON = key;
                        renderDashboard(data.servers);
                        renderHostStats(data.host);
                    }
                }
        } catch(err) { console.error('[ws] error:', err); }
    };

    ws.onclose = function() {
        _dashWS = null;
        var delay = Math.min(3000 * Math.pow(2, _dashRetries), 60000);
        _dashRetries++;
        setTimeout(connectDashboardWS, delay);
    };
}

function renderDashboard(servers) {
    var el = document.getElementById('dashboard-servers');
    if (!el) return;

    if (!servers || !servers.length) {
        el.innerHTML = '<div class="bg-slate-800 border border-slate-700 rounded-lg px-4 py-12 text-center text-slate-500">' +
            'No servers running. <a href="/admin/launch" class="text-orange-400 hover:underline">Launch one</a>.</div>';
        return;
    }

    var html = '';
    for (var i = 0; i < servers.length; i++) {
        var s = servers[i];
        var statusText;
        if (s.status === 'running') {
            statusText = '<span class="text-green-400 text-xs">Running</span>';
        } else if (s.status === 'restarting') {
            statusText = '<span class="text-orange-400 text-xs">Restarting\u2026</span>';
        } else if (s.status === 'stopping') {
            statusText = '<span class="text-red-400 text-xs">Stopping\u2026</span>';
        } else {
            statusText = '<span class="text-slate-400 text-xs">' + s.status + '</span>';
        }

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

        var mode = (s.score && s.score.mode) ? s.score.mode : s.mode;
        var modeLabel = mode ? mode.charAt(0).toUpperCase() + mode.slice(1) : '-';

        // Card layout (works on all screen sizes)
        html += '<a href="/admin/server/' + s.name + '" class="block bg-slate-800 border border-slate-700 rounded-lg p-4 hover:bg-slate-700/50 transition-colors">' +
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
            (s.status === 'running' ? '<div class="flex items-center gap-3 text-xs text-slate-500 mt-2">' +
                '<span>CPU ' + s.cpu.toFixed(1) + '%</span>' +
                '<span>Mem ' + fmtMem(s.memMB) + '</span>' +
            '</div>' : '') +
            '</a>';
    }
    el.innerHTML = html;
}

function fmtMem(mb) {
    if (mb >= 1024) return (mb / 1024).toFixed(1) + ' GB';
    return mb.toFixed(0) + ' MB';
}

var _hostCoresExpanded = false;

function renderHostStats(host) {
    var el = document.getElementById('dashboard-host');
    if (!el || !host) return;

    var coreToggle = host.corePcts && host.corePcts.length > 0
        ? '<button onclick="_hostCoresExpanded=!_hostCoresExpanded;renderHostStats(_lastHostData)" class="text-slate-500 hover:text-slate-300 transition-colors cursor-pointer">' +
            (_hostCoresExpanded ? '\u25B2' : '\u25BC') + '</button>'
        : '';

    var html = '<div class="bg-slate-800 border border-slate-700 rounded-lg px-4 py-3">' +
        '<div class="flex items-center gap-6 text-xs text-slate-300">' +
            '<span class="text-slate-500 font-medium">Host</span>' +
            '<span class="flex items-center gap-1">CPU ' + host.cpu.toFixed(1) + '% (' + host.cores + ' cores) ' + coreToggle + '</span>' +
            '<span>Mem ' + fmtMem(host.memUsedMB) + ' / ' + fmtMem(host.memTotalMB) + '</span>' +
            '<span class="text-slate-600">|</span>' +
            '<span class="text-slate-500 font-medium">Panel</span>' +
            '<span>' + fmtMem(host.panelMemMB) + '</span>' +
        '</div>';

    if (_hostCoresExpanded && host.corePcts && host.corePcts.length > 0) {
        html += '<div class="grid gap-1.5 mt-3 pt-3 border-t border-slate-700" style="grid-template-columns: repeat(auto-fill, minmax(120px, 1fr))">';
        for (var i = 0; i < host.corePcts.length; i++) {
            var pct = host.corePcts[i];
            var barColor = pct > 80 ? 'bg-red-500' : pct > 50 ? 'bg-orange-500' : 'bg-green-500';
            html += '<div class="flex items-center gap-2 text-xs">' +
                '<span class="text-slate-500 w-8">C' + i + '</span>' +
                '<div class="flex-1 bg-slate-700 rounded-full h-1.5">' +
                    '<div class="' + barColor + ' h-1.5 rounded-full" style="width:' + Math.min(pct, 100).toFixed(1) + '%"></div>' +
                '</div>' +
                '<span class="text-slate-400 w-10 text-right">' + pct.toFixed(0) + '%</span>' +
            '</div>';
        }
        html += '</div>';
    }

    html += '</div>';
    el.innerHTML = html;
    _lastHostData = host;
}
var _lastHostData = null;

var _currentGameMode = '';
var _lastScoreMap = '';
var _inWarmup = false;
var _statMode = 0; // 0 = K/D/A/MVP, 1 = HS%/KDR/ADR/EF/UD
var _statModeLabels = ['Kills Deaths Assists MVPs', 'HS% KDR ADR UD EF'];
var _showDeadEquip = false;

// Game state WebSocket (players + killfeed) - renders from JSON client-side
var _serverStopped = false;
var _serverRestarting = false;

var _gameWS = null;
function connectGameWS(serverName) {
    if (_gameWS) { try { _gameWS.close(); } catch(e) {} }
    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/admin/server/' + serverName + '/game/ws');
    _gameWS = ws;

    var _wsConnected = false;

    ws.onopen = function() {
        _wsConnected = true;
        if (_serverRestarting) {
            _serverRestarting = false;
            setServerStatus(null);
            // Reconnect log WS too
            if (_logServerName) connectLogWS(_logServerName);
        }
    };

    ws.onmessage = function(e) {
        try {
            var data = JSON.parse(e.data);
            switch (data.type) {
                case 'players':
                    if (data.score) renderScore(data.score);
                    renderPlayers(data.players);
                    break;
                case 'killfeed':
                    renderKillfeed(data.killfeed);
                    break;
                case 'kill':
                    appendKills(data.kills);
                    break;
                case 'server_status':
                    if (data.status === 'stopped') {
                        _serverStopped = true;
                        setServerStatus('stopped');
                        setLogStatus('Disconnected', 'text-xs text-red-400');
                    } else if (data.status === 'restarting') {
                        _serverRestarting = true;
                        resetLogRetries();
                        setServerStatus('restarting');
                    }
                    break;
            }
        } catch(err) { console.error('[ws] error:', err); }
    };

    ws.onclose = function() {
        if (_serverStopped) return;
        // If WS never connected and not restarting, server doesn't exist — redirect
        if (!_wsConnected && !_serverRestarting) {
            window.location.href = '/admin';
            return;
        }
        setTimeout(function() { connectGameWS(serverName); }, 3000);
    };

    ws.onerror = function() {};
}

function renderScore(score) {
    var bar = document.getElementById('score-bar');
    if (!bar) return;

    var modeEl = document.getElementById('score-mode');
    if (modeEl && score.mode) {
        modeEl.textContent = score.mode.charAt(0).toUpperCase() + score.mode.slice(1);
        if (score.mode !== _currentGameMode) {
            _currentGameMode = score.mode;
            updateRconMapDropdown(score.mode);
        }
    }
    var mapEl = document.getElementById('score-map');
    if (mapEl && score.map && score.map !== _lastScoreMap) {
        _lastScoreMap = score.map;
        mapEl.innerHTML = '<img src="/static/icons/map/' + score.map + '.svg" class="h-4 w-4 opacity-60 rounded" onerror="this.style.display=\'none\'">' + score.map;
    }
    _inWarmup = !!score.warmup;

    var noRounds = _currentGameMode === 'deathmatch' || _currentGameMode === 'armsrace';

    // Show/hide round-based elements
    var ctEl = document.getElementById('score-ct-wrap');
    var tEl = document.getElementById('score-t-wrap');
    var roundEl = document.getElementById('score-round-wrap');
    if (ctEl) ctEl.style.display = noRounds ? 'none' : '';
    if (tEl) tEl.style.display = noRounds ? 'none' : '';
    if (roundEl) roundEl.style.display = noRounds ? 'none' : '';

    if (!noRounds) {
        document.getElementById('score-ct').textContent = score.ct;
        document.getElementById('score-t').textContent = score.t;
        document.getElementById('score-round').textContent = score.round;

        // Swap tournament team labels on side switch
        if (typeof _team1Name !== 'undefined' && _team1Name && _currentGameMode !== 'casual') {
            var team1IsCT = _team1StartsCT;
            if (score.half > 0 && score.round > score.half) {
                team1IsCT = !team1IsCT;
                var maxR = score.maxRounds || (score.half * 2);
                if (maxR > 0 && score.round > maxR) {
                    var otHalf = Math.floor((score.round - maxR - 1) / 3) % 2;
                    if (otHalf === 1) team1IsCT = !team1IsCT;
                }
            }
            var ctLabel = document.getElementById('score-ct-label');
            var tLabel = document.getElementById('score-t-label');
            if (ctLabel) ctLabel.textContent = team1IsCT ? _team1Name : _team2Name;
            if (tLabel) tLabel.textContent = team1IsCT ? _team2Name : _team1Name;
        }
    }

    // Paused badge
    var pauseEl = document.getElementById('score-paused');
    if (pauseEl) {
        pauseEl.style.display = score.paused ? '' : 'none';
    }

    // Round history bar
    var histEl = document.getElementById('round-history');
    if (!histEl) return;
    if (noRounds || !score.rounds || !score.rounds.length) {
        histEl.innerHTML = '';
        histEl.classList.add('hidden');
        return;
    }
    histEl.classList.remove('hidden');

    function roundIcon(r) {
        var icon = '';
        switch (r.rs) {
            case 'elimination': icon = '/static/icons/ui/kill.svg'; break;
            case 'bomb': icon = '/static/icons/equipment/planted_c4.svg'; break;
            case 'defuse': icon = '/static/icons/equipment/defuser.svg'; break;
            case 'time': icon = '/static/icons/ui/timer.svg'; break;
        }
        var color = r.w === 'CT' ? 'ct' : 't';
        var filter = color === 'ct'
            ? 'filter: brightness(0) saturate(100%) invert(55%) sepia(90%) saturate(500%) hue-rotate(190deg);'
            : 'filter: brightness(0) saturate(100%) invert(70%) sepia(90%) saturate(400%) hue-rotate(5deg);';
        return '<img src="' + icon + '" class="h-4 w-4" style="' + filter + '" title="Round ' + r.r + ': ' + r.w + ' (' + r.rs + ')">';
    }

    var half = score.half || 0;
    var maxR = score.maxRounds || (half * 2);
    var blank = '<span class="inline-block w-4 h-4"></span>';
    var noHalves = _currentGameMode === 'demolition' || _currentGameMode === 'casual';

    function getPeriod(roundNum) {
        // Demolition: single section, no halves
        if (noHalves) return { section: 'reg', idx: 0 };
        // No half time detected yet — all rounds in first half
        if (half === 0) return { section: 'reg', idx: 0 };
        if (roundNum <= half) return { section: 'reg', idx: 0 };
        if (maxR > 0 && roundNum <= maxR) return { section: 'reg', idx: 1 };
        // Overtime
        var otRound = roundNum - maxR; // 1-based within all OT
        var otNum = Math.ceil(otRound / 6); // which OT (1-based)
        var withinOT = otRound - (otNum - 1) * 6; // 1-6 within this OT
        var otHalf = withinOT <= 3 ? 0 : 1; // first or second half of this OT
        return { section: 'ot', otNum: otNum, idx: otHalf };
    }

    function ctOnTop(period) {
        if (noHalves) return true; // Demolition: CT always on top
        if (period.section === 'reg') return period.idx === 0;
        return period.idx === 0;
    }

    // Collect periods in order for rendering
    var sections = []; // [{label, top, bottom}]
    var sectionMap = {}; // key -> {top:'', bottom:''}

    function sectionKey(period) {
        if (period.section === 'reg') return 'reg_' + period.idx;
        return 'ot' + period.otNum + '_' + period.idx;
    }

    function ensureSection(period) {
        var key = sectionKey(period);
        if (!sectionMap[key]) {
            var label = '';
            if (period.section === 'reg' && period.idx === 0 && !noHalves) label = 'First Half';
            else if (period.section === 'reg' && period.idx === 1) label = 'Second Half';
            else if (period.section === 'ot' && period.idx === 0) label = 'OT' + period.otNum;
            sectionMap[key] = { key: key, label: label, top: '', bottom: '' };
            sections.push(sectionMap[key]);
        }
        return sectionMap[key];
    }

    // Create regulation sections
    ensureSection({ section: 'reg', idx: 0 });
    if (!noHalves) ensureSection({ section: 'reg', idx: 1 });

    for (var i = 0; i < score.rounds.length; i++) {
        var r = score.rounds[i];
        var period = getPeriod(r.r);
        var sec = ensureSection(period);
        var icon = roundIcon(r);
        var isTop = (r.w === 'CT') === ctOnTop(period);

        if (isTop) {
            sec.top += icon;
            sec.bottom += blank;
        } else {
            sec.top += blank;
            sec.bottom += icon;
        }
    }

    // Build grid: each section is a column pair (top+bottom), with dividers between
    var cols = [];
    for (var s = 0; s < sections.length; s++) {
        if (s > 0) cols.push('auto'); // divider column
        cols.push('1fr');
    }
    var html = '<div class="grid grid-rows-[auto_1fr_1fr] gap-0.5" style="grid-template-columns:' + cols.join(' ') + '">';

    // Row 0: labels
    for (var s = 0; s < sections.length; s++) {
        if (s > 0) html += '<div class="row-span-3 w-px bg-slate-500 mx-0.5"></div>';
        html += '<div class="text-center text-xs text-slate-500 font-medium px-1">' + (sections[s].label || '') + '</div>';
    }
    // Row 1: top (CT started)
    for (var s = 0; s < sections.length; s++) {
        var rounding = s === 0 ? ' rounded-tl' : (s === sections.length - 1 ? ' rounded-tr' : '');
        html += '<div class="flex items-center gap-0.5 px-1.5 py-1 bg-slate-700/30 min-h-6' + rounding + '">' + sections[s].top + '</div>';
    }
    // Row 2: bottom (T started)
    for (var s = 0; s < sections.length; s++) {
        var rounding = s === 0 ? ' rounded-bl' : (s === sections.length - 1 ? ' rounded-br' : '');
        html += '<div class="flex items-center gap-0.5 px-1.5 py-1 bg-slate-700/30 min-h-6' + rounding + '">' + sections[s].bottom + '</div>';
    }
    html += '</div>';
    histEl.innerHTML = html;
}


function playerEquipIcons(p) {
    if (!_showDeadEquip && !p.alive && p.online) return '';
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
        for (var c = 0; c < players.length; c++) {
            var pc = players[c];
            if (pc.online && pc.team !== 'S' && pc.name !== 'SourceTV' && (!pc.name || pc.name.indexOf('CSTV') === -1)) online++;
        }
        countEl.textContent = '(' + online + ')';
    }
    var toggleEl = document.getElementById('stat-toggle');
    if (toggleEl) {
        toggleEl.style.display = (_currentGameMode === 'armsrace' || _currentGameMode === 'deathmatch') ? 'none' : '';
    }

    if (!players.length) {
        el.innerHTML = '<div class="px-4 py-8 text-center text-slate-500 text-sm">No players connected</div>';
        return;
    }

    // Split players by team
    var ctPlayers = [], tPlayers = [], spectators = [];
    for (var i = 0; i < players.length; i++) {
        var p = players[i];
        if (p.team === 'CT') ctPlayers.push(p);
        else if (p.team === 'T') tPlayers.push(p);
        else spectators.push(p);
    }

    var showAlive = !_inWarmup && _currentGameMode && _currentGameMode !== 'deathmatch' && _currentGameMode !== 'armsrace';

    var isArmsRace = _currentGameMode === 'armsrace';
    var isDeathmatch = _currentGameMode === 'deathmatch';

    // Arms race sort moved below after activePlayers is built
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
    } else if (isDeathmatch) {
        // Deathmatch: K D A KDR HS% — no money or equipment
        statHeaders = '<th class="px-4 py-2 font-medium text-center" title="Kills">K</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Deaths">D</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Assists">A</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Kill/Death Ratio">KDR</th>' +
            '<th class="px-4 py-2 font-medium text-center" title="Headshot Percentage">HS%</th>';
        statCols = '<col style="width:50px"><col style="width:50px"><col style="width:50px"><col style="width:60px"><col style="width:65px">';
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
            var lvl = p.level || 0;
            return '<td class="px-4 py-2 text-green-400 text-center">' + p.k + '</td>' +
                '<td class="px-4 py-2 text-red-400 text-center">' + p.d + '</td>' +
                '<td class="px-4 py-2 text-yellow-400 text-center">' + p.a + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.zeusk || 0) + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.knifek || 0) + '</td>' +
                '<td class="px-4 py-2 text-orange-400 text-center font-medium">' + lvl + '</td>';
        }
        if (isDeathmatch) {
            return '<td class="px-4 py-2 text-green-400 text-center">' + p.k + '</td>' +
                '<td class="px-4 py-2 text-red-400 text-center">' + p.d + '</td>' +
                '<td class="px-4 py-2 text-yellow-400 text-center">' + p.a + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</td>' +
                '<td class="px-4 py-2 text-slate-300 text-center">' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</td>';
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
        if (isArmsRace || isDeathmatch) return '';
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
        var noTeamSplit = isArmsRace || isDeathmatch;
        var rowClasses = noTeamSplit ? '' : 'border-b border-slate-700/50';
        var nameColor = noTeamSplit ? teamColor(p.team) : 'text-white';
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
        var noEconMode = isArmsRace || isDeathmatch;
        var money = !noEconMode && p.money ? '$' + p.money.toLocaleString() : '';
        var ping = !p.online ? '-' : (p.bot ? '-' : p.ping + 'ms');
        var equip = noEconMode ? '' : playerEquipIcons(p);
        var borderColor = noEconMode ? '' : (p.team === 'CT' ? ' border-l-2 border-blue-400' : (p.team === 'T' ? ' border-l-2 border-yellow-400' : ''));
        var cardNameColor = noEconMode ? teamColor(p.team) : 'text-white';

        var statsHtml;
        if (isDeathmatch) {
            statsHtml = '<span class="text-green-400">K: ' + p.k + '</span>' +
                '<span class="text-red-400">D: ' + p.d + '</span>' +
                '<span class="text-yellow-400">A: ' + p.a + '</span>' +
                '<span class="text-slate-300">KDR: ' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</span>' +
                '<span class="text-slate-300">HS: ' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</span>';
        } else if (isArmsRace) {
            statsHtml = '<span class="text-green-400">K: ' + p.k + '</span>' +
                '<span class="text-red-400">D: ' + p.d + '</span>' +
                '<span class="text-yellow-400">A: ' + p.a + '</span>' +
                '<span class="text-slate-300">KDR: ' + (p.kdr ? p.kdr.toFixed(2) : '-') + '</span>' +
                '<span class="text-slate-300">HS: ' + (p.hsp ? p.hsp.toFixed(1) + '%' : '-') + '</span>' +
                '<span class="text-slate-300">Zeus: ' + (p.zeusk || 0) + '</span>' +
                '<span class="text-slate-300">Knife: ' + (p.knifek || 0) + '</span>' +
                '<span class="text-orange-400">Lvl: ' + (p.level || 0) + '</span>';
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

    var totalCols = isArmsRace ? 10 : (isDeathmatch ? 7 : (_statMode === 0 ? 8 : 9));

    var activePlayers = ctPlayers.concat(tPlayers);
    if (isArmsRace) {
        activePlayers.sort(function(a, b) {
            var la = a.level || 0, lb = b.level || 0;
            if (la !== lb) return lb - la;
            return b.k - a.k;
        });
    } else if (isDeathmatch) {
        activePlayers.sort(function(a, b) {
            // Sort by kills desc, then deaths asc, then assists desc
            if (a.k !== b.k) return b.k - a.k;
            if (a.d !== b.d) return a.d - b.d;
            return b.a - a.a;
        });
    }
    if (isArmsRace || isDeathmatch) {
        // Flat list, team color on player name
        for (var i = 0; i < activePlayers.length; i++) html += playerRow(activePlayers[i]);
    } else {
        if (ctPlayers.length) {
            html += '<tr><td colspan="' + totalCols + '" class="px-4 py-1.5 text-xs font-bold text-blue-400 bg-blue-500/5 border-l-2 border-blue-400">Counter-Terrorists</td></tr>';
            for (var i = 0; i < ctPlayers.length; i++) html += playerRow(ctPlayers[i]);
        }

        if (tPlayers.length) {
            html += '<tr><td colspan="' + totalCols + '" class="px-4 py-1.5 text-xs font-bold text-yellow-400 bg-yellow-500/5 border-l-2 border-yellow-400">Terrorists</td></tr>';
            for (var i = 0; i < tPlayers.length; i++) html += playerRow(tPlayers[i]);
        }

    }

    if (spectators.length) {
        html += '<tr><td colspan="' + totalCols + '" class="px-4 py-1.5 text-xs font-medium text-slate-500 bg-slate-700/20">Spectators (' + spectators.length + ')</td></tr>';
        for (var i = 0; i < spectators.length; i++) {
            var sp = spectators[i];
            var ping = !sp.online ? '-' : (sp.bot ? '-' : sp.ping + 'ms');
            html += '<tr class="opacity-50"><td class="px-4 py-2 text-slate-400">' + sp.name + '</td>' +
                '<td colspan="' + (totalCols - 2) + '"></td>' +
                '<td class="px-4 py-2 text-slate-500">' + ping + '</td></tr>';
        }
    }

    html += '</tbody></table>';

    // Mobile cards
    html += '<div class="sm:hidden space-y-2 p-3">';
    if (isArmsRace || isDeathmatch) {
        for (var i = 0; i < activePlayers.length; i++) html += playerCard(activePlayers[i]);
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
    }
    if (spectators.length) {
        html += '<div class="border-t border-slate-700 my-2"></div>';
        html += '<div class="text-xs font-medium text-slate-500 px-1 pb-1">Spectators (' + spectators.length + ')</div>';
        for (var i = 0; i < spectators.length; i++) {
            var sp = spectators[i];
            var ping = !sp.online ? '-' : (sp.bot ? '-' : sp.ping + 'ms');
            html += '<div class="bg-slate-700/20 rounded-lg p-3 opacity-50">' +
                '<div class="flex items-center gap-2">' +
                    '<span class="text-slate-400 text-sm flex-1">' + sp.name + '</span>' +
                    '<span class="text-slate-500 text-xs">' + ping + '</span>' +
                '</div></div>';
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
    // Action event (bomb plant, defuse) — has killer + message + weapon icon, no victim
    if (k.msg && k.killer && !k.victim) {
        return '<div class="flex items-center gap-2">' +
            '<span class="' + teamColor(k.kt) + ' text-xs">' + k.killer + '</span>' +
            '<span class="flex items-center gap-1">' + weaponIcon(k.weapon) + '</span>' +
            '<span class="text-slate-400 text-xs">' + k.msg + '</span>' +
            '<span class="text-slate-600 text-xs ml-auto">' + k.time + '</span>' +
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
        (k.victim === 'chicken'
            ? '<img src="/static/icons/ui/zoo.svg" class="h-4 inline-block opacity-80" title="Chicken">'
            : '<span class="' + teamColor(k.vt) + ' text-xs">' + k.victim + '</span>') +
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


// RCON quick action buttons
var _rconServerName = '';

function rconQuick(cmds) {
    if (!_rconServerName) return;
    var commands = cmds.split(';');
    var output = document.getElementById('rcon-output');

    function sendNext(i) {
        if (i >= commands.length) return;
        var cmd = commands[i].trim();
        if (!cmd) { sendNext(i + 1); return; }

        fetch('/admin/server/' + _rconServerName + '/rcon', {
            method: 'POST',
            headers: {'Content-Type': 'application/x-www-form-urlencoded'},
            body: 'command=' + encodeURIComponent(cmd)
        }).then(function(r) { if (!r.ok) throw new Error(r.status); return r.text(); }).then(function(html) {
            if (output) {
                output.insertAdjacentHTML('beforeend', html);
                output.scrollTop = output.scrollHeight;
            }
            sendNext(i + 1);
        });
    }
    sendNext(0);
}

function rconQuickMode() {
    var modeSel = document.getElementById('rcon-mode-select');
    var mapSel = document.getElementById('rcon-map-select');
    if (!modeSel || !modeSel.value || !mapSel || !mapSel.value) return;
    // Set game_type/game_mode cvars then changelevel to apply
    var modes = {
        competitive: 'game_type 0;game_mode 1',
        casual:      'game_type 0;game_mode 0',
        wingman:     'game_type 0;game_mode 2',
        armsrace:    'game_type 1;game_mode 0',
        demolition:  'game_type 1;game_mode 1',
        deathmatch:  'game_type 1;game_mode 2'
    };
    var cvars = modes[modeSel.value];
    if (cvars) rconQuick(cvars + ';changelevel ' + mapSel.value);
}

function rconQuickMap() {
    var sel = document.getElementById('rcon-map-select');
    if (sel && sel.value) rconQuick('changelevel ' + sel.value);
}

function updateRconMapDropdown(mode) {
    var sel = document.getElementById('rcon-map-select');
    if (!sel) return;
    var current = sel.value;
    var preferred = _mapPools[mode || _currentGameMode] || _allMaps;
    var preferredSet = {};
    for (var i = 0; i < preferred.length; i++) preferredSet[preferred[i]] = true;

    // Other maps not in the preferred pool
    var others = [];
    for (var j = 0; j < _allMaps.length; j++) {
        if (!preferredSet[_allMaps[j]]) others.push(_allMaps[j]);
    }

    sel.innerHTML = '';
    // Preferred maps first
    for (var i = 0; i < preferred.length; i++) {
        var opt = document.createElement('option');
        opt.value = preferred[i];
        opt.textContent = preferred[i];
        if (preferred[i] === current) opt.selected = true;
        sel.appendChild(opt);
    }
    // Divider + other maps
    if (others.length) {
        var divider = document.createElement('option');
        divider.disabled = true;
        divider.textContent = '───────────';
        sel.appendChild(divider);
        for (var k = 0; k < others.length; k++) {
            var opt = document.createElement('option');
            opt.value = others[k];
            opt.textContent = others[k];
            if (others[k] === current) opt.selected = true;
            sel.appendChild(opt);
        }
    }
}

function initRconMapDropdown() {
    updateRconMapDropdown();
    // Update map list when mode dropdown changes
    var modeSel = document.getElementById('rcon-mode-select');
    if (modeSel) {
        modeSel.addEventListener('change', function() {
            updateRconMapDropdown(modeSel.value);
        });
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

function toggleDeadEquip() {
    _showDeadEquip = !_showDeadEquip;
    var btn = document.getElementById('dead-equip-toggle');
    if (btn) btn.textContent = _showDeadEquip ? 'Dead Equip: ON' : 'Dead Equip: OFF';
    if (_lastPlayers.length) renderPlayers(_lastPlayers);
}

function renameServer(name) {
    var current = document.getElementById('server-title').textContent;
    var alias = prompt('Rename server:', current);
    if (alias === null) return;
    fetch('/admin/server/' + name + '/rename', {
        method: 'POST',
        headers: {'Content-Type': 'application/x-www-form-urlencoded'},
        body: 'alias=' + encodeURIComponent(alias)
    }).then(function(r) { if (!r.ok) throw new Error(r.status); }).then(function() {
        document.getElementById('server-title').textContent = alias || name;
    });
}

document.addEventListener('DOMContentLoaded', function() {
    initRconAutocomplete();
});

// ── Team Member AJAX ──

function addMember(teamId, form) {
    var input = form.querySelector('[name="steam_name"]');
    var name = input.value.trim();
    if (!name) return false;
    fetch('/admin/tournament/' + _tournamentID + '/teams/' + teamId + '/members', {
        method: 'POST',
        headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest'},
        body: 'steam_name=' + encodeURIComponent(name)
    });
    input.value = '';
    return false;
}

function resetGame(matchId, gameId) {
    if (!confirm('Reset this game? This will clear scores, stats, and undo any bracket advancement.')) return;
    fetch('/admin/match/' + matchId + '/game/' + gameId + '/reset', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'}
    });
}

function swapSide(matchId, gameId, newVal, btn) {
    fetch('/admin/match/' + matchId + '/game/' + gameId + '/side', {
        method: 'POST',
        headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest'},
        body: 'team1_starts_ct=' + newVal
    }).then(function() {
        // Bracket WS will push updated data and re-render
    });
}

function deleteGame(matchId, gameId) {
    if (!confirm('Delete this game?')) return;
    fetch('/admin/match/' + matchId + '/game/' + gameId + '/delete', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'}
    });
}

// Generic AJAX form submit for bracket actions (prevents page reload)
function submitBracketForm(form, url) {
    var data = new FormData(form);
    fetch(url, {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'},
        body: new URLSearchParams(data)
    });
    return false;
}

// Generic AJAX button action for bracket (winner, bestof)
function bracketAction(url, params) {
    var body = Object.keys(params).map(function(k) { return k + '=' + encodeURIComponent(params[k]); }).join('&');
    fetch(url, {
        method: 'POST',
        headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest'},
        body: body
    });
}

// AJAX POST — updates status UI inline, or redirects if specified
function ajaxPost(url, params, opts) {
    var body = '';
    if (params) body = Object.keys(params).map(function(k) { return k + '=' + encodeURIComponent(params[k]); }).join('&');
    fetch(url, {
        method: 'POST',
        headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest'},
        body: body
    }).then(function() {
        if (opts && opts.redirect) { location.href = opts.redirect; return; }
    });
}

function adminAddTeam(form) {
    var data = new URLSearchParams(new FormData(form));
    fetch('/admin/tournament/' + _tournamentID + '/teams', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'},
        body: data
    });
    form.querySelector('input[name="name"]').value = '';
    return false;
}

function adminDeleteTeam(teamId) {
    if (!confirm('Delete team?')) return;
    fetch('/admin/tournament/' + _tournamentID + '/teams/' + teamId + '/delete', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'}
    });
}

function adminRenameTeam(teamId, btn) {
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
            s.className = 'font-medium text-sm';
            s.textContent = current;
            input.replaceWith(s);
            return;
        }
        fetch('/admin/tournament/' + _tournamentID + '/teams/' + teamId + '/rename', {
            method: 'POST',
            headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest'},
            body: 'name=' + encodeURIComponent(name)
        });
    }
    input.addEventListener('blur', save);
    input.addEventListener('keydown', function(e) { if (e.key === 'Enter') { e.preventDefault(); input.blur(); } if (e.key === 'Escape') { input.value = current; input.blur(); } });
}

function renderAdminTeams(teams) {
    var el = document.getElementById('admin-teams-list');
    if (!el) return;
    var heading = document.getElementById('teams-heading');
    if (heading) heading.textContent = 'Teams (' + (teams ? teams.length : 0) + ')';
    // Update seed list if present
    if (typeof renderSeedList === 'function') renderSeedList(teams);

    if (!teams || teams.length === 0) {
        el.innerHTML = '<p class="text-slate-500 text-sm">No teams yet.</p>';
        return;
    }
    var html = '<div class="space-y-3">';
    for (var i = 0; i < teams.length; i++) {
        var t = teams[i];
        html += '<div class="bg-slate-700/50 rounded p-3">';
        html += '<div class="flex items-center justify-between mb-2">';
        html += '<span class="font-medium text-sm">' + t.name + '</span>';
        html += '<span class="flex gap-2">';
        html += '<button onclick="adminRenameTeam(' + t.id + ', this)" class="text-slate-400 hover:text-white text-xs" title="Rename"><svg class="w-3.5 h-3.5 inline" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z"/></svg></button>';
        html += '<button onclick="adminDeleteTeam(' + t.id + ')" class="text-red-400 hover:text-red-300 text-xs">Delete</button>';
        html += '</span></div>';
        html += '<ul class="text-xs text-slate-400 space-y-1 mb-2">';
        if (t.members && t.members.length > 0) {
            for (var j = 0; j < t.members.length; j++) {
                var m = t.members[j];
                html += '<li class="flex items-center justify-between">';
                html += '<span>' + m.steamName + '</span>';
                html += '<button onclick="removeMember(' + t.id + ',' + m.id + ',this)" class="bg-red-600 hover:bg-red-700 rounded px-1.5 py-1" title="Remove player"><img src="/static/icons/ui/friendremove.svg" class="w-4 h-4"></button>';
                html += '</li>';
            }
        } else {
            html += '<li class="text-slate-500">No members</li>';
        }
        html += '</ul>';
        html += '<form onsubmit="return addMember(' + t.id + ', this)" class="flex gap-1">';
        html += '<input type="text" name="steam_name" placeholder="Steam name" required class="flex-1 bg-slate-600 border border-slate-500 rounded px-2 py-1 text-xs text-white focus:outline-none">';
        html += '<button type="submit" class="bg-slate-600 hover:bg-slate-500 rounded px-1.5 py-1" title="Add player"><img src="/static/icons/ui/addplayer.svg" class="w-4 h-4"></button>';
        html += '</form></div>';
    }
    html += '</div>';
    el.innerHTML = html;
}

function removeMember(teamId, memberId, btn) {
    fetch('/admin/tournament/' + _tournamentID + '/teams/' + teamId + '/members/' + memberId + '/delete', {
        method: 'POST',
        headers: {'X-Requested-With': 'XMLHttpRequest'}
    }).then(function() {
        var li = btn.closest('li');
        if (li) li.remove();
    });
}

function renderAdminBracket(matches) {
    renderBracketLayout(document.querySelector('.bracket-container'), matches, renderBracketMatch, 340);
}

function renderBracketMatch(m) {
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

    var boLabel = m.bestOf > 1 ? '<span class="text-xs text-yellow-500">Bo' + m.bestOf + '</span>' : '<span class="text-xs text-slate-500">Bo1</span>';
    var bothTeams = m.team1.id && m.team2.id;

    var html = '<div class="bg-slate-700 border border-slate-600 rounded overflow-hidden">';

    // Team rows
    html += '<div class="flex items-center justify-between px-3 py-1.5 border-b border-slate-600/50">';
    html += '<span class="text-sm ' + t1class + '">' + t1name + '</span>' + boLabel;
    html += '</div>';
    html += '<div class="flex items-center justify-between px-3 py-1.5">';
    html += '<span class="text-sm ' + t2class + '">' + t2name + '</span>';
    html += '</div>';

    // Games section
    if (m.games && m.games.length > 0) {
        html += '<div class="border-t border-slate-600/50">';
        for (var g = 0; g < m.games.length; g++) {
            var game = m.games[g];
            html += '<div class="px-3 py-1.5 flex items-center gap-2 text-xs flex-nowrap whitespace-nowrap' + (g > 0 ? ' border-t border-slate-600/30' : '') + '">';
            if (game.status === 'completed') {
                html += '<span class="text-slate-400">' + (mapDisplayName(game.map) || 'Game ' + game.num) + '</span>';
                html += '<span class="text-slate-300 font-mono">' + game.t1 + '-' + game.t2 + '</span>';
                html += formatHalfScores(game);
            } else if (game.status === 'live') {
                html += '<span class="text-orange-400">' + (mapDisplayName(game.map) || 'Game ' + game.num) + '</span>';
                html += '<span class="text-orange-300 font-mono">' + game.t1 + '-' + game.t2 + '</span>';
                if (game.server) {
                    html += '<a href="/admin/server/' + game.server + '" class="bg-orange-500/20 text-orange-400 hover:bg-orange-500/30 font-bold rounded px-1.5 py-0.5">LIVE</a>';
                } else {
                    html += '<span class="bg-orange-500/20 text-orange-400 font-bold rounded px-1.5 py-0.5">LIVE</span>';
                }
            } else {
                // Pending — show score entry form (AJAX)
                html += '<form onsubmit="return submitBracketForm(this, \'/admin/match/' + m.id + '/game/' + game.id + '\')" class="flex items-center gap-1">';
                html += '<span class="text-slate-400">' + (mapDisplayName(game.map) || 'Game ' + game.num) + '</span>';
                html += '<input type="number" name="team1_score" value="' + game.t1 + '" min="0" class="w-12 bg-slate-600 border border-slate-500 rounded px-1 py-0.5 text-center text-white text-xs">';
                html += '<span class="text-slate-500">-</span>';
                html += '<input type="number" name="team2_score" value="' + game.t2 + '" min="0" class="w-12 bg-slate-600 border border-slate-500 rounded px-1 py-0.5 text-center text-white text-xs">';
                html += '<select name="winner_id" class="bg-slate-600 border border-slate-500 rounded px-1 py-0.5 text-white text-xs">';
                html += '<option value="">Winner</option>';
                if (m.team1.id) html += '<option value="' + m.team1.id + '">' + (m.team1.name || 'T1') + '</option>';
                if (m.team2.id) html += '<option value="' + m.team2.id + '">' + (m.team2.name || 'T2') + '</option>';
                html += '</select>';
                html += '<button type="submit" class="bg-green-700 hover:bg-green-600 text-white rounded px-1.5 py-0.5">Save</button>';
                html += '</form>';
            }
            // Launch server link for non-completed games
            if (game.status !== 'completed' && game.map) {
                html += '<a href="/admin/match/' + m.id + '/launch?game_number=' + game.num + '&map_name=' + encodeURIComponent(game.map) + '" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-1.5 py-0.5">Launch</a>';
            }
            // CT side indicator + swap
            var ctName = game.t1ct ? t1name : t2name;
            var newVal = game.t1ct ? '0' : '1';
            html += '<button onclick="swapSide(' + m.id + ',' + game.id + ',\'' + newVal + '\',this)" class="text-blue-400 hover:text-blue-300 text-xs" title="Click to swap sides">' +
                '<span class="text-blue-400">CT:</span>' + ctName + '</button>';
            // Reset button for non-pending games
            if (game.status !== 'pending') {
                html += '<button onclick="resetGame(' + m.id + ',' + game.id + ')" class="text-red-400 hover:text-red-300 text-xs" title="Reset game results">&#8635;</button>';
            }
            // Delete game button
            html += '<button onclick="deleteGame(' + m.id + ',' + game.id + ')" class="text-red-400 hover:text-red-300 text-xs" title="Delete game">&times;</button>';
            html += '</div>';
        }
        html += '</div>';
    }

    // Admin controls
    html += '<div class="px-2 py-1.5 border-t border-slate-600/50 flex flex-wrap gap-1 text-xs">';

    // Add game (pick map) — AJAX
    if (bothTeams && !m.winner) {
        var nextGameNum = (m.games ? m.games.length : 0) + 1;
        var maxGames = m.bestOf;
        if (!m.games || m.games.length < maxGames) {
            html += '<form onsubmit="return submitBracketForm(this, \'/admin/match/' + m.id + '/game\')" class="flex items-center gap-1">';
            html += '<input type="hidden" name="game_number" value="' + nextGameNum + '">';
            html += '<select name="map_name" class="bg-slate-600 border border-slate-500 rounded px-1 py-0.5 text-white text-xs">';
            var compMaps = typeof _mapPools !== 'undefined' ? (_mapPools.competitive || []) : [];
            var maps = compMaps.length > 0 ? compMaps : (typeof _allMaps !== 'undefined' ? _allMaps : []);
            for (var mi = 0; mi < maps.length; mi++) {
                html += '<option value="' + maps[mi] + '">' + maps[mi] + '</option>';
            }
            html += '</select>';
            html += '<select name="team1_starts_ct" class="bg-slate-600 border border-slate-500 rounded px-1 py-0.5 text-white text-xs">';
            html += '<option value="1">' + (m.team1.name || 'T1') + ' CT</option>';
            html += '<option value="0">' + (m.team2.name || 'T2') + ' CT</option>';
            html += '</select>';
            html += '<button type="submit" class="bg-orange-600 hover:bg-orange-500 text-white rounded px-1.5 py-0.5">+ Game</button>';
            html += '</form>';
        }
    }

    // Winner override buttons — AJAX
    if (bothTeams && !m.winner) {
        html += '<button onclick="bracketAction(\'/admin/bracket/winner\', {match_id:\'' + m.id + '\',winner_id:\'' + m.team1.id + '\'})" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-1.5 py-0.5" title="Manually advance">' + (m.team1.name || 'T1') + ' wins</button>';
        html += '<button onclick="bracketAction(\'/admin/bracket/winner\', {match_id:\'' + m.id + '\',winner_id:\'' + m.team2.id + '\'})" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-1.5 py-0.5" title="Manually advance">' + (m.team2.name || 'T2') + ' wins</button>';
    }

    // Revert winner — AJAX
    if (m.winner) {
        html += '<button onclick="if(confirm(\'Revert winner and undo bracket advancement?\'))bracketAction(\'/admin/bracket/clearwinner\', {match_id:\'' + m.id + '\'})" class="bg-red-600/80 hover:bg-red-600 text-white rounded px-1.5 py-0.5" title="Revert winner and undo advancement">Revert Winner</button>';
    }

    // Bo toggle — AJAX
    if (!m.winner) {
        var nextBo = m.bestOf === 1 ? 3 : m.bestOf === 3 ? 5 : 1;
        html += '<button onclick="bracketAction(\'/admin/bracket/bestof\', {match_id:\'' + m.id + '\',best_of:\'' + nextBo + '\'})" class="bg-slate-600 hover:bg-slate-500 text-white rounded px-1.5 py-0.5">&rarr; Bo' + nextBo + '</button>';
    }

    html += '</div>';
    html += '</div>';
    return html;
}

// Admin bracket live updates via WS
var _adminBracketWS = null;
var _adminBracketRetries = 0;
var _lastAdminBracketJSON = '';
function connectAdminBracketWS() {
    // No live updates for completed tournaments
    if (typeof _tournamentID === 'undefined' || !_tournamentID) return;
    if (typeof _tournamentStatus !== 'undefined' && _tournamentStatus === 'completed') return;

    if (_adminBracketWS) { try { _adminBracketWS.close(); } catch(e) {} }

    var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    var ws = new WebSocket(protocol + '//' + location.host + '/tournament/' + _tournamentID + '/ws');
    _adminBracketWS = ws;

    ws.onmessage = function(e) {
        try {
            _adminBracketRetries = 0;
            var data = JSON.parse(e.data);
            if (data.type === 'bracket') {
                // Update status pills and buttons if status changed
                if (data.status && typeof _tournamentStatus !== 'undefined' && data.status !== _tournamentStatus) {
                    _tournamentStatus = data.status;
                    if (typeof renderStatusPills === 'function') renderStatusPills();
                    if (typeof renderStatusButtons === 'function') renderStatusButtons();
                }
                if (data.bracket) {
                    var key = JSON.stringify(data.bracket);
                    if (key !== _lastAdminBracketJSON) {
                        _lastAdminBracketJSON = key;
                        renderAdminBracket(data.bracket);
                    }
                }
                if (data.teams) {
                    renderAdminTeams(data.teams);
                }
            }
        } catch(err) { console.error('[bracket-ws] parse error:', err); }
    };

    ws.onclose = function() {
        _adminBracketWS = null;
        var delay = Math.min(5000 * Math.pow(2, _adminBracketRetries), 60000);
        _adminBracketRetries++;
        setTimeout(connectAdminBracketWS, delay);
    };
}
