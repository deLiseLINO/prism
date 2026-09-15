import { app } from 'electron'
import path from 'node:path'

// Runtime assets (icons) live in the packaged app under process.resourcesPath
// (extraResources in electron-builder.yml); in dev they sit in the repo's
// resources/ directory next to the built dist/ tree.
export function locateResource(name: string): string {
  return app.isPackaged
    ? path.join(process.resourcesPath, name)
    : path.join(app.getAppPath(), 'resources', name)
}
