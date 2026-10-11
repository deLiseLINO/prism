(function () {
  var root = document.documentElement
  var mac = /Mac|iPhone|iPad/.test(navigator.platform)
  var electron = 'prism' in window
  // Only the Electron shell on macOS sits on a vibrancy window; the preload
  // bridge is the same signal renderer/src/bridge.ts uses to pick Electron.
  var capable = mac && electron
  root.dataset.os = mac ? 'mac' : 'other'
  if (electron) root.dataset.shell = 'electron'
  if (capable) root.dataset.glass = 'capable'

  // 'prism-theme' holds the serialized ThemeState (theme.logic.ts); older builds stored a bare mode.
  var mode = 'system'
  try {
    var raw = localStorage.getItem('prism-theme')
    if (raw === 'light' || raw === 'dark') mode = raw
    else if (raw && raw.charAt(0) === '{') {
      var saved = JSON.parse(raw)
      if (saved && (saved.mode === 'light' || saved.mode === 'dark')) mode = saved.mode
    }
  } catch (err) {
    mode = 'system'
  }
  var variant = mode === 'system' ? (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light') : mode
  root.dataset.theme = variant

  // useAppearance caches the computed custom properties; applying them here avoids a flash.
  try {
    var cache = JSON.parse(localStorage.getItem('prism-theme-vars') || 'null')
    if (!cache || cache.capable !== capable || !cache[variant]) return
    var build = cache[variant]
    var sets = [build.variables, cache.typography]
    for (var i = 0; i < sets.length; i++) {
      for (var name in sets[i]) {
        if (Object.prototype.hasOwnProperty.call(sets[i], name) && sets[i][name]) root.style.setProperty(name, sets[i][name])
      }
    }
    if (build.material === 'translucent') root.dataset.material = 'translucent'
    root.dataset.translucency = build.translucencyScope
  } catch (err) {
    // a corrupt cache is rewritten by the hook on mount
  }
})()
