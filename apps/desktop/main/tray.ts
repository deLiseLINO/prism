import { Menu, Tray, app, nativeImage } from 'electron'
import path from 'node:path'
import type { DaemonStatus } from '@prism/contracts'

export class TrayController {
  private tray: Tray | null = null

  constructor(private readonly showMainWindow: () => void) {}

  init(): void {
    const icon = nativeImage.createFromPath(path.join(__dirname, '..', '..', 'resources', 'trayTemplate.png'))
    icon.addRepresentation({
      scaleFactor: 2,
      buffer: nativeImage.createFromPath(path.join(__dirname, '..', '..', 'resources', 'trayTemplate@2x.png')).toPNG(),
    })
    icon.setTemplateImage(true)
    this.tray = new Tray(icon)
    this.tray.setToolTip('Prism')
    this.tray.setContextMenu(
      Menu.buildFromTemplate([
        { label: 'Show Prism', click: () => this.showMainWindow() },
        { type: 'separator' },
        { label: 'Quit Prism', click: () => void app.quit() },
      ]),
    )
    this.tray.on('click', () => this.showMainWindow())
  }

  update(status: DaemonStatus): void {
    this.tray?.setToolTip(`Prism — daemon ${status.state}`)
  }
}
