(() => {
  const root = document.body;
  const base = root.dataset.inbox;
  const list = document.querySelector('.list');
  const reader = document.querySelector('.reader');
  const status = document.querySelector('.refresh-status');
  const params = new URLSearchParams(location.search);
  let revision = root.dataset.revision;
  let selected = root.dataset.selected;
  let timer, pending;
  let retry = 2000;
  let stopped = false;

  // Only these escaped, same-origin template fragments are replaced. In
  // particular, the search input and an unchanged reader stay in the DOM.
  function replaceList(markup) {
    const focused = list.contains(document.activeElement) ? document.activeElement.getAttribute('href') : null;
    const scroll = list.scrollTop;
    const anchor = scroll > 0 ? Array.from(list.querySelectorAll('.message')).find(node => node.offsetTop >= list.offsetTop + scroll) : null;
    const href = anchor?.getAttribute('href');
    const offset = anchor ? anchor.offsetTop - scroll : 0;
    list.innerHTML = markup;
    const links = Array.from(list.querySelectorAll('a'));
    const replacement = href && links.find(node => node.getAttribute('href') === href);
    list.scrollTop = replacement ? replacement.offsetTop - offset : scroll;
    if (focused) links.find(node => node.getAttribute('href') === focused)?.focus({preventScroll: true});
  }

  function schedule(delay = retry) {
    clearTimeout(timer);
    if (!document.hidden && !stopped) timer = setTimeout(refresh, delay);
  }

  async function refresh() {
    if (pending || document.hidden || stopped) return;
    const controller = new AbortController();
    pending = controller;
    const timeout = setTimeout(() => controller.abort(), 8000);
    try {
      const url = new URL(base + '/updates', location.origin);
      url.search = new URLSearchParams({revision, selected, q: params.get('q') || '', view: params.get('view') || ''});
      const response = await fetch(url, {cache: 'no-store', signal: controller.signal, headers: {Accept: 'application/json'}});
      if (response.status !== 204) {
        if (!response.ok) throw new Error('Inbox unavailable');
        const update = await response.json();
        if (controller.signal.aborted) return;
        replaceList(update.list);
        document.querySelector('.count').textContent = `${update.total} ${update.total === 1 ? 'message' : 'messages'}`;
        document.querySelector('.clear-all').hidden = update.total === 0;
        if (update.reader !== undefined) {
          reader.innerHTML = update.reader;
          // A message removed in another tab must not leave a stale detail URL.
          if (document.querySelector('.mailbox').classList.contains('detail')) {
            const next = new URL(location.href);
            next.pathname = update.selectedID === '0' ? base : `${base}/${update.selectedID}`;
            history.replaceState(null, '', next);
            if (update.selectedID === '0') document.querySelector('.mailbox').classList.remove('detail');
          }
        }
        revision = update.revision;
        selected = update.selectedID;
        root.dataset.revision = revision;
        root.dataset.selected = selected;
      }
      retry = 2000;
      status.textContent = 'Auto-refresh on';
    } catch (_) {
      if (!document.hidden && !stopped) {
        status.textContent = 'Reconnecting…';
        retry = Math.min(retry * 2, 15000);
      }
    } finally {
      clearTimeout(timeout);
      pending = null;
      schedule();
    }
  }

  document.addEventListener('visibilitychange', () => {
    clearTimeout(timer);
    if (document.hidden) {
      pending?.abort();
      status.textContent = 'Auto-refresh paused';
    } else {
      status.textContent = 'Auto-refresh on';
      schedule(0);
    }
  });
  window.addEventListener('pagehide', () => {
    stopped = true;
    clearTimeout(timer);
    pending?.abort();
  });
  window.addEventListener('pageshow', () => { stopped = false; schedule(0); });
  status.textContent = document.hidden ? 'Auto-refresh paused' : 'Auto-refresh on';
  schedule();
})();
