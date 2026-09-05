import type { PrismBridge } from './bridge'

declare global {
  interface Window {
    readonly prism: PrismBridge
  }
}

export {}
