// Set the theme before the first paint, so the page does not flash the wrong
// one. A file rather than an inline script: the console's Content-Security-Policy
// runs scripts from this server only (internal/httpx ConsolePolicy).
;(function () {
  try {
    var t = localStorage.getItem('theme')
    if (t !== 'light' && t !== 'dark') {
      t = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
    }
    document.documentElement.setAttribute('data-theme', t)
  } catch (e) {
    document.documentElement.setAttribute('data-theme', 'dark')
  }
})()
