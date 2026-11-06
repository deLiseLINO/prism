import { useEffect, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import type { HostId } from '@prism/contracts'
import { currentHost, useActiveHost } from '../ActiveHost'
import { api } from '../api'
import { useExperimentalFlags } from '../experimental'
import {
  ALL_MACHINES,
  encodeScope,
  fetchMachines,
  initialScope,
  machineLabel,
  machineShade,
  machinesFrom,
  readStoredScope,
  scopeTargets,
  stackSegments,
  storeScope,
  unavailableError,
  type Machine,
  type MachineFailure,
  type MachineFetch,
  type MachineScope,
  type MachineTokens,
} from '../machines'
import { Banner } from './Ui'

export type MachineScopeState =
  | { readonly kind: 'single' }
  | {
      readonly kind: 'scoped'
      readonly machines: readonly Machine[]
      readonly scope: MachineScope
      readonly setScope: (scope: MachineScope) => void
    }

// Scoped reads need the desktop bridge: the web UI's same-origin proxy ignores `host` and
// would silently answer every machine with local data.
export function useMachineScope(): MachineScopeState {
  const { flags } = useExperimentalFlags()
  const { host } = useActiveHost()
  const enabled = flags.remoteInstall && 'prism' in window
  const [machines, setMachines] = useState<readonly Machine[]>([])
  const [scope, setScope] = useState<MachineScope>(ALL_MACHINES)

  useEffect(() => {
    if (!enabled) {
      setMachines([])
      return
    }
    let active = true
    api.localHosts().then(
      (view) => {
        if (!active) return
        const list = machinesFrom(view.hosts)
        setMachines(list)
        setScope(initialScope({ activeHost: host, stored: readStoredScope(), machines: list }))
      },
      () => {
        if (active) setMachines([])
      },
    )
    return () => {
      active = false
    }
  }, [enabled, host])

  if (!enabled || !machines.some((machine) => !machine.local)) return { kind: 'single' }
  return {
    kind: 'scoped',
    machines,
    scope,
    setScope: (next) => {
      storeScope(next)
      setScope(next)
    },
  }
}

export function scopeKey(state: MachineScopeState): string {
  if (state.kind === 'single') return 'single'
  return `${encodeScope(state.scope)} ${state.machines.map((machine) => machine.id).join(' ')}`
}

export async function loadAcross<T, M>({
  state,
  load,
  merge,
}: {
  readonly state: MachineScopeState
  readonly load: (host: HostId) => Promise<T>
  readonly merge: (results: readonly MachineFetch<T>[], machineOrder: readonly HostId[]) => M
}): Promise<M> {
  if (state.kind === 'single') {
    const machine = currentHost()
    return merge([{ machine, ok: true, value: await load(machine) }], [machine])
  }
  const results = await fetchMachines(scopeTargets(state.scope, state.machines), load)
  const failures = results.flatMap((result) => (result.ok ? [] : [result]))
  if (failures.length === results.length) throw unavailableError(failures, state.machines)
  return merge(
    results,
    state.machines.map((machine) => machine.id),
  )
}

interface MenuOption {
  readonly scope: MachineScope
  readonly label: string
  readonly note: string
}

export function MachineMenu({
  machines,
  scope,
  onChange,
  countOf,
}: {
  readonly machines: readonly Machine[]
  readonly scope: MachineScope
  readonly onChange: (scope: MachineScope) => void
  readonly countOf: (scope: MachineScope) => number | undefined
}): JSX.Element {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const buttonRef = useRef<HTMLButtonElement | null>(null)
  const listRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    if (!open) return
    listRef.current?.querySelector<HTMLButtonElement>('[aria-selected="true"]')?.focus()
    const onPointer = (event: PointerEvent): void => {
      if (event.target instanceof Node && rootRef.current?.contains(event.target) !== true) setOpen(false)
    }
    const onKey = (event: KeyboardEvent): void => {
      if (event.key !== 'Escape') return
      setOpen(false)
      buttonRef.current?.focus()
    }
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const onListKey = (event: ReactKeyboardEvent<HTMLDivElement>): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    const buttons = [...(listRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]') ?? [])]
    const index = buttons.findIndex((button) => button === document.activeElement)
    const step = event.key === 'ArrowDown' ? 1 : -1
    const next = index === -1 ? (step === 1 ? 0 : buttons.length - 1) : (index + step + buttons.length) % buttons.length
    buttons[next]?.focus()
  }

  const selected = encodeScope(scope)
  const options: readonly MenuOption[] = [
    { scope: ALL_MACHINES, label: 'All machines', note: `${machines.length} machines` },
    ...machines.map((machine) => ({ scope: { kind: 'machine', id: machine.id } as const, label: machine.label, note: machine.note })),
  ]
  const dots = scope.kind === 'all' ? Math.min(machines.length, 3) : 1

  return (
    <div className="machine-menu" ref={rootRef}>
      <button
        ref={buttonRef}
        type="button"
        className="btn btn--ghost machine-menu__btn"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
      >
        <span className="machine-menu__dots" aria-hidden="true">
          {Array.from({ length: dots }, (_, index) => (
            <span key={index} className="dot dot-ok" />
          ))}
        </span>
        <span>{scope.kind === 'all' ? 'All machines' : machineLabel(machines, scope.id)}</span>
        <svg aria-hidden="true" width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"><path d="m2 4 3 3 3-3" /></svg>
      </button>
      {open ? (
        <div className="machine-menu__list" role="listbox" aria-label="Machine" ref={listRef} onKeyDown={onListKey}>
          {options.map((option) => {
            const key = encodeScope(option.scope)
            const count = countOf(option.scope)
            return (
              <button
                key={key}
                type="button"
                role="option"
                className="machine-menu__opt"
                aria-selected={key === selected}
                onClick={() => {
                  onChange(option.scope)
                  setOpen(false)
                  buttonRef.current?.focus()
                }}
              >
                <span className="machine-menu__check" aria-hidden="true">{key === selected ? '✓' : ''}</span>
                <span className="machine-menu__name">
                  {option.label}
                  {option.note !== '' ? <span className="machine-menu__note">{option.note}</span> : null}
                </span>
                <span className="num machine-menu__count">{count === undefined ? '' : `${count.toLocaleString('en-US')} req`}</span>
              </button>
            )
          })}
        </div>
      ) : null}
    </div>
  )
}

export function MachineTag({ label }: { readonly label: string }): JSX.Element {
  return (
    <span className="machine-tag" title={label}>
      <span className="dot dot-ok" aria-hidden="true" />
      {label}
    </span>
  )
}

export function MachineFailures({
  failures,
  machines,
}: {
  readonly failures: readonly MachineFailure[]
  readonly machines: readonly Machine[]
}): JSX.Element | null {
  if (failures.length === 0) return null
  return (
    <div className="machine-failures">
      {failures.map((failure) => (
        <Banner key={failure.machine} tone="warn" title={`Could not load ${machineLabel(machines, failure.machine)}: ${failure.message}`} />
      ))}
    </div>
  )
}

export function StackBar({
  byMachine,
  machines,
  max,
}: {
  readonly byMachine: MachineTokens
  readonly machines: readonly Machine[]
  readonly max: number
}): JSX.Element {
  return (
    <span className="bar stack-bar">
      {stackSegments(byMachine, machines, max).map((segment) => (
        <span
          key={segment.machine.id}
          className="stack-bar__seg"
          style={{ '--w': segment.ratio, '--shade': `${segment.shade}%` } as CSSProperties}
          title={`${segment.machine.label}: ${segment.tokens.toLocaleString('en-US')}`}
        />
      ))}
    </span>
  )
}

export function MachineLegend({ machines }: { readonly machines: readonly Machine[] }): JSX.Element {
  return (
    <span className="machine-legend">
      {machines.map((machine, index) => (
        <span key={machine.id} className="machine-legend__item">
          <span
            className="machine-legend__swatch"
            style={{ '--shade': `${machineShade(index, machines.length)}%` } as CSSProperties}
            aria-hidden="true"
          />
          {machine.label}
        </span>
      ))}
    </span>
  )
}
