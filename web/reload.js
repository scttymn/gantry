// Gantry's development reload (web.Reload). The page knows the version of
// the code that rendered it: the server's boot ID, and how many times templ
// has since rewritten a template's text. The stream says the current one;
// when it differs, the page reloads.
//
// Only a visible page holds a stream (over HTTP/1.1 a browser opens six
// connections to a host, and a stream per tab would use them up). A page
// shown again reconnects, and reloads if the code changed while it was hidden.
(function () {
	"use strict";
	if (window.gantryReload) return;
	window.gantryReload = true;
	var page = document.currentScript.getAttribute("data-version");
	var source = null;
	var retry = null;
	var delay = 250;

	function connect() {
		if (source || document.hidden) return;
		source = new EventSource("/_gantry/reload");
		source.addEventListener("version", function (e) {
			delay = 250;
			if (e.data !== page) location.reload();
		});
		source.onerror = function () {
			// While the server restarts, the proxy answers with an error,
			// which ends an EventSource for good: start another.
			if (source.readyState === EventSource.CLOSED) {
				disconnect();
				retry = setTimeout(connect, delay);
				delay = Math.min(delay * 2, 2000);
			}
		};
	}

	function disconnect() {
		clearTimeout(retry);
		if (source) source.close();
		source = null;
	}

	document.addEventListener("visibilitychange", function () {
		if (document.hidden) disconnect();
		else connect();
	});
	window.addEventListener("pagehide", disconnect);
	window.addEventListener("pageshow", connect);
	connect();
})();
