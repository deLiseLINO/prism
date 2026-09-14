// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRoot, type Root } from 'react-dom/client'
import { act } from 'react'
import { ModelSelect } from '../renderer/src/views/OverviewView'

let container: HTMLDivElement | null = null
let root: Root | null = null

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  container = null
  root = null
})

function renderSelect(props: {
  readonly ids: readonly string[]
  readonly selected: string
  readonly onSelect?: (id: string) => void
  readonly placeholder?: string
  readonly disabled?: boolean
}): void {
  act(() => {
    root?.render(
      <ModelSelect
        ids={props.ids}
        selected={props.selected}
        onSelect={props.onSelect ?? vi.fn()}
        placeholder={props.placeholder ?? 'pick a vision model'}
        disabled={props.disabled}
      />,
    )
  })
}

const IDS = ['anthropic/claude-sonnet-4.5', 'google/gemini-3-flash', 'openai/gpt-5.2'] as const

describe('ModelSelect', () => {
  it('renders the current model in the closed button', () => {
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash' })
    const btn = container?.querySelector('.vsd__btn')
    expect(btn?.getAttribute('aria-expanded')).toBe('false')
    expect(btn?.querySelector('.vsd__cur')?.textContent).toBe('google/gemini-3-flash')
    expect(container?.querySelector('.vsd--open')).toBeNull()
  })

  it('renders the placeholder when the selected id is empty', () => {
    renderSelect({ ids: IDS, selected: '' })
    expect(container?.querySelector('.vsd__cur')?.textContent).toBe('pick a vision model')
  })

  it('opens the menu on click and renders options with aria-selected on the current id', () => {
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash' })
    const btn = container?.querySelector<HTMLButtonElement>('.vsd__btn')
    act(() => {
      btn?.click()
    })
    expect(container?.querySelector('.vsd')?.className).toContain('vsd--open')
    expect(btn?.getAttribute('aria-expanded')).toBe('true')
    const options = Array.from(container?.querySelectorAll('.vsd__opt') ?? [])
    expect(options.map((option) => option.textContent)).toEqual([...IDS])
    const chosen = options.filter((option) => option.getAttribute('aria-selected') === 'true')
    expect(chosen).toHaveLength(1)
    expect(chosen[0]?.textContent).toBe('google/gemini-3-flash')
  })

  it('selects an option, calls onSelect with the id, and closes the menu', () => {
    const onSelect = vi.fn()
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash', onSelect })
    act(() => {
      container?.querySelector<HTMLButtonElement>('.vsd__btn')?.click()
    })
    act(() => {
      container?.querySelector<HTMLButtonElement>('.vsd__opt[data-id="openai/gpt-5.2"]')?.click()
    })
    expect(onSelect).toHaveBeenCalledWith('openai/gpt-5.2')
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(container?.querySelector('.vsd')?.className).not.toContain('vsd--open')
    expect(container?.querySelector('.vsd__btn')?.getAttribute('aria-expanded')).toBe('false')
  })

  it('closes on Escape and returns focus to the button', () => {
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash' })
    const btn = container?.querySelector<HTMLButtonElement>('.vsd__btn')
    act(() => {
      btn?.click()
    })
    expect(container?.querySelector('.vsd')?.className).toContain('vsd--open')
    btn?.focus()
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    expect(container?.querySelector('.vsd')?.className).not.toContain('vsd--open')
    expect(btn?.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(btn)
  })

  it('closes on a pointerdown outside the root', () => {
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash' })
    act(() => {
      container?.querySelector<HTMLButtonElement>('.vsd__btn')?.click()
    })
    expect(container?.querySelector('.vsd')?.className).toContain('vsd--open')
    const outsider = document.createElement('button')
    document.body.appendChild(outsider)
    act(() => {
      outsider.dispatchEvent(
        new PointerEvent('pointerdown', { key: 'x', bubbles: true, composed: true }),
      )
    })
    expect(container?.querySelector('.vsd')?.className).not.toContain('vsd--open')
    outsider.remove()
  })

  it('stays closed when a pointerdown lands inside the component', () => {
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash' })
    act(() => {
      container?.querySelector<HTMLButtonElement>('.vsd__btn')?.click()
    })
    act(() => {
      container
        ?.querySelector('.vsd')
        ?.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }))
    })
    expect(container?.querySelector('.vsd')?.className).toContain('vsd--open')
  })

  it('opens with ArrowDown and focuses the selected option', () => {
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash' })
    const btn = container?.querySelector<HTMLButtonElement>('.vsd__btn')
    btn?.focus()
    act(() => {
      btn?.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }),
      )
    })
    expect(container?.querySelector('.vsd')?.className).toContain('vsd--open')
    expect(document.activeElement?.getAttribute('data-id')).toBe('google/gemini-3-flash')
  })

  it('opens with ArrowUp and focuses the first option when none is selected', () => {
    renderSelect({ ids: IDS, selected: '' })
    const btn = container?.querySelector<HTMLButtonElement>('.vsd__btn')
    btn?.focus()
    act(() => {
      btn?.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }),
      )
    })
    expect(container?.querySelector('.vsd')?.className).toContain('vsd--open')
    expect(document.activeElement?.getAttribute('data-id')).toBe(IDS[0])
  })

  it('moves focus between options with ArrowDown and ArrowUp while open', () => {
    renderSelect({ ids: IDS, selected: '' })
    const btn = container?.querySelector<HTMLButtonElement>('.vsd__btn')
    btn?.focus()
    act(() => {
      btn?.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }),
      )
    })
    const options = Array.from(
      container?.querySelectorAll<HTMLButtonElement>('.vsd__opt') ?? [],
    )
    act(() => {
      options[1]?.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }),
      )
    })
    expect(document.activeElement?.getAttribute('data-id')).toBe(IDS[2])
    act(() => {
      options[2]?.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }),
      )
    })
    expect(document.activeElement?.getAttribute('data-id')).toBe(IDS[1])
  })

  it('keeps the menu closed and the button disabled when disabled', () => {
    renderSelect({ ids: IDS, selected: 'google/gemini-3-flash', disabled: true })
    const btn = container?.querySelector<HTMLButtonElement>('.vsd__btn')
    expect(btn?.disabled).toBe(true)
    act(() => {
      btn?.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }))
      btn?.click()
    })
    expect(container?.querySelector('.vsd')?.className).not.toContain('vsd--open')
    expect(btn?.getAttribute('aria-expanded')).toBe('false')
  })

  it('renders a stale selected id on the button with no option aria-selected', () => {
    renderSelect({ ids: IDS, selected: 'zhipu/glm-4.6v' })
    const btn = container?.querySelector<HTMLButtonElement>('.vsd__btn')
    expect(btn?.querySelector('.vsd__cur')?.textContent).toBe('zhipu/glm-4.6v')
    act(() => {
      btn?.click()
    })
    const options = Array.from(container?.querySelectorAll('.vsd__opt') ?? [])
    expect(options).toHaveLength(3)
    expect(options.every((option) => option.getAttribute('aria-selected') === 'false')).toBe(true)
  })
})
