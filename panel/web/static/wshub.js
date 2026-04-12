// ── Page lifecycle manager ──
// Provides managed resources (WS, timers, listeners) that auto-cleanup on HTMX navigation.
// Pages call Page.subscribe(), Page.ws(), Page.interval(), Page.timeout(), Page.onLeave().
// All registered resources are torn down on htmx:beforeSwap.
window.Page = (function() {
    var cleanups = [];

    // Single persistent hub connection, created on first use.
    var hub = null;

    function getHub() {
        if (hub) return hub;
        hub = {
            conn: null, topics: new Set(), handlers: {},
            _retries: 0, _closing: false
        };

        hub.connect = function() {
            if (hub._closing) return;
            var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
            var ws = new WebSocket(protocol + '//' + location.host + '/ws');
            hub.conn = ws;
            ws.onopen = function() {
                hub._retries = 0;
                hub.topics.forEach(function(topic) {
                    ws.send(JSON.stringify({subscribe: topic}));
                });
            };
            ws.onmessage = function(e) {
                try {
                    var msg = JSON.parse(e.data);
                    if (msg.topic && msg.data !== undefined) {
                        var fn = hub.handlers[msg.topic];
                        if (fn) fn(msg.data);
                    }
                } catch(err) { console.error('[hub]', err); }
            };
            ws.onclose = function() {
                hub.conn = null;
                if (hub._closing) return;
                var delay = Math.min(3000 * Math.pow(2, hub._retries), 60000);
                hub._retries++;
                setTimeout(function() { hub.connect(); }, delay);
            };
            ws.onerror = function() {};
        };

        hub.connect();
        return hub;
    }

    return {
        // Register a cleanup callback for the current page.
        onLeave: function(fn) { cleanups.push(fn); },

        // Persistent hub subscription — NOT cleaned up on page navigation.
        // Use for layout-level features (e.g. announcements) that live outside #main-content.
        on: function(topic, handler) {
            var hub = getHub();
            hub.topics.add(topic);
            hub.handlers[topic] = handler;
            if (hub.conn && hub.conn.readyState === WebSocket.OPEN) {
                hub.conn.send(JSON.stringify({subscribe: topic}));
            }
        },

        // Subscribe to a hub topic. Handler receives the parsed data payload.
        // Auto-unsubscribes on page leave (HTMX navigation).
        subscribe: function(topic, handler) {
            var hub = getHub();
            hub.topics.add(topic);
            hub.handlers[topic] = handler;
            if (hub.conn && hub.conn.readyState === WebSocket.OPEN) {
                hub.conn.send(JSON.stringify({subscribe: topic}));
            }
            cleanups.push(function() {
                delete hub.handlers[topic];
                hub.topics.delete(topic);
                if (hub.conn && hub.conn.readyState === WebSocket.OPEN) {
                    hub.conn.send(JSON.stringify({unsubscribe: topic}));
                }
            });
        },

        // Dedicated WebSocket (not hub) with auto-reconnect. For server logs/game only.
        // Closed on page leave.
        ws: function(path, onMessage) {
            var closing = false, retries = 0, ws = null;
            function connect() {
                if (closing) return;
                var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
                ws = new WebSocket(protocol + '//' + location.host + path);
                ws.onmessage = function(e) {
                    try { retries = 0; onMessage(JSON.parse(e.data)); }
                    catch(err) { console.error('[Page.ws]', path, err); }
                };
                ws.onclose = function() {
                    ws = null;
                    if (closing) return;
                    var delay = Math.min(3000 * Math.pow(2, retries), 60000);
                    retries++;
                    setTimeout(connect, delay);
                };
            }
            connect();
            cleanups.push(function() { closing = true; if (ws) { ws.close(); ws = null; } });
        },

        // Managed setInterval — auto-cleared on page leave.
        interval: function(fn, ms) {
            var id = setInterval(fn, ms);
            cleanups.push(function() { clearInterval(id); });
            return id;
        },

        // Managed setTimeout — auto-cleared on page leave.
        timeout: function(fn, ms) {
            var id = setTimeout(fn, ms);
            cleanups.push(function() { clearTimeout(id); });
            return id;
        },

        // Run all cleanups. Called by htmx:beforeSwap.
        _cleanup: function() {
            for (var i = 0; i < cleanups.length; i++) { try { cleanups[i](); } catch(e) {} }
            cleanups = [];
        }
    };
})();

document.addEventListener('htmx:beforeSwap', function() { Page._cleanup(); });
