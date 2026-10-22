(function () {
  'use strict';
  var form = document.getElementById('form');
  var input = document.getElementById('url');
  var go = document.getElementById('go');
  var errEl = document.getElementById('error');
  var noticeEl = document.getElementById('notice');

  function showError(msg) {
    errEl.textContent = msg || '';
    errEl.hidden = !msg;
  }

  // Mirrors isPrivateHost in connect.go (the tested reference implementation).
  function isPrivateHost(host) {
    var h = host.toLowerCase().replace(/^\[|\]$/g, '').replace(/\.$/, '');
    if (h === 'localhost' || /\.localhost$|\.local$/.test(h)) return true;
    var m = h.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
    if (m) {
      var a = +m[1], b = +m[2];
      return a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) ||
             (a === 192 && b === 168) || (a === 169 && b === 254);
    }
    if (h.indexOf(':') >= 0) {
      return h === '::1' || /^f[cd][0-9a-f]{2}:/.test(h) || /^fe[89ab][0-9a-f]:/.test(h);
    }
    return false;
  }

  function updateNotice() {
    var v = input.value.trim();
    var insecure = false;
    if (v) {
      try {
        var u = new URL(/:\/\//.test(v) ? v : 'http://' + v);
        insecure = u.protocol === 'http:' && u.hostname !== '' && !isPrivateHost(u.hostname);
      } catch (e) { /* ignore while typing */ }
    }
    noticeEl.hidden = !insecure;
  }

  input.addEventListener('input', updateNotice);

  form.addEventListener('submit', function (ev) {
    ev.preventDefault();
    showError('');
    go.disabled = true;
    go.textContent = 'Connecting…';
    window.go.main.App.Connect(input.value).then(function (url) {
      window.location.href = url;
    }, function (e) {
      showError(String((e && e.message) || e));
      go.disabled = false;
      go.textContent = 'Connect';
    });
  });

  // Called from Go after the page loads: prefill and show startup errors.
  window.steamcmdInit = function (state) {
    if (state && state.url) input.value = state.url;
    showError(state && state.error);
    updateNotice();
    input.focus();
    input.select();
  };
})();
