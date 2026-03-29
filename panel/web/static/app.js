// Log viewer WebSocket
var _logServerName = null;
var _logPaused = false;
var _logBuffer = []; // buffer lines while paused

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

    // Deduplicate consecutive identical lines
    if (_lastLogLine && _lastLogLine._logText === text) {
        _lastLogLine._logCount = (_lastLogLine._logCount || 1) + 1;
        // Update the counter badge
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
    output.appendChild(line);
    _lastLogLine = line;

    while (output.children.length > 5000) {
        output.removeChild(output.firstChild);
    }
    output.scrollTop = output.scrollHeight;
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

// RCON: clear input after submit, scroll output
document.addEventListener('htmx:afterRequest', function(e) {
    if (e.detail.elt.id === 'rcon-form') {
        var input = e.detail.elt.querySelector('input[name=command]');
        if (input) input.value = '';
        var output = document.getElementById('rcon-output');
        if (output) output.scrollTop = output.scrollHeight;
    }
});
