(function () {
  var mode = 'auto'
  try {
    var stored = localStorage.getItem('prism-theme')
    if (stored === 'auto' || stored === 'dark' || stored === 'light') mode = stored
  } catch (err) {
    mode = 'auto'
  }
  var dark =
    mode === 'dark' ||
    (mode === 'auto' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'

})()
