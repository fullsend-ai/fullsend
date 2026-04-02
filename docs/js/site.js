(function () {
  var STORAGE_KEY = 'fs-theme';
  var REPO = 'fullsend-ai/fullsend';

  function getSystemTheme() {
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }

  function applyTheme(theme) {
    if (theme === 'dark') {
      document.documentElement.removeAttribute('data-theme');
    } else {
      document.documentElement.setAttribute('data-theme', theme);
    }
    var btn = document.querySelector('.theme-toggle');
    if (btn) btn.setAttribute('aria-label', 'Switch to ' + (theme === 'dark' ? 'light' : 'dark') + ' theme');
  }

  function getStoredTheme() {
    try { return localStorage.getItem(STORAGE_KEY); } catch (e) { return null; }
  }

  function storeTheme(theme) {
    try { localStorage.setItem(STORAGE_KEY, theme); } catch (e) { /* noop */ }
  }

  // Apply on load (before DOMContentLoaded to avoid flash)
  var initial = getStoredTheme() || getSystemTheme();
  applyTheme(initial);

  document.addEventListener('DOMContentLoaded', function () {
    // Theme toggle
    var btn = document.querySelector('.theme-toggle');
    if (btn) {
      applyTheme(initial);
      btn.addEventListener('click', function () {
        var current = document.documentElement.getAttribute('data-theme') || 'dark';
        var next = current === 'dark' ? 'light' : 'dark';
        applyTheme(next);
        storeTheme(next);
      });
    }

    // Fetch GitHub stars
    var starsEl = document.querySelector('#gh-stars span');
    if (starsEl) {
      fetch('https://api.github.com/repos/' + REPO)
        .then(function (r) { return r.json(); })
        .then(function (data) {
          if (data.stargazers_count != null) {
            starsEl.textContent = data.stargazers_count.toLocaleString();
          }
        })
        .catch(function () {});
    }

    // Fetch contributors
    var grid = document.getElementById('contributors');
    if (grid) {
      fetch('https://api.github.com/repos/' + REPO + '/contributors?per_page=100')
        .then(function (r) { return r.json(); })
        .then(function (contributors) {
          if (!Array.isArray(contributors)) return;
          contributors.forEach(function (c) {
            if (c.type === 'Bot') return;
            var a = document.createElement('a');
            a.href = 'https://github.com/' + c.login;
            a.title = c.login;
            a.target = '_blank';
            a.rel = 'noopener';
            var img = document.createElement('img');
            img.src = c.avatar_url + '&s=96';
            img.alt = c.login;
            img.width = 48;
            img.height = 48;
            img.loading = 'lazy';
            a.appendChild(img);
            grid.appendChild(a);
          });
        })
        .catch(function () {});
    }
  });
})();
