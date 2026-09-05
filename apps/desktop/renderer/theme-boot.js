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

  var skin = 'obsidian'
  try {
    var storedSkin = localStorage.getItem('prism-skin')
    if (storedSkin === 'obsidian' || storedSkin === 'graphite') skin = storedSkin
  } catch (err) {
    skin = 'obsidian'
  }
  document.documentElement.dataset.skin = skin
})()
