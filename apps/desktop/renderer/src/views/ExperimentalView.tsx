import type { CSSProperties } from 'react'
import { AGENT_ACTIONS_FLAG, EXPERIMENTAL_FLAGS, useExperimentalFlags, type ExperimentalFlag } from '../experimental'
import { bridge } from '../bridge'
import { useAsync } from '../useAsync'

interface FlagCard {
  readonly flag: ExperimentalFlag
  readonly label: string
  readonly description: string
}

// The agent-actions card only exists when the daemon itself was started with
// actions enabled (PRISM_AGENT_ACTIONS). Without it there is nothing to
// toggle: the daemon refuses the mutations regardless.
export function experimentalCards(daemonAllowsActions: boolean): readonly FlagCard[] {
  return daemonAllowsActions ? [AGENT_ACTIONS_FLAG, ...EXPERIMENTAL_FLAGS] : EXPERIMENTAL_FLAGS
}

export function ExperimentalView(): JSX.Element {
  const { flags, setFlag } = useExperimentalFlags()
  const agentActions = useAsync(() => bridge.agents.status(), [])
  const daemonAllowsActions =
    agentActions.state.kind === 'ready' ? agentActions.state.value.actionsEnabled : false
  const cards = experimentalCards(daemonAllowsActions)

  return (
    <section className="screen" aria-labelledby="h-experimental">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-experimental">Experimental</h1>
          <p className="sub">unfinished features, off by default</p>
        </div>
      </div>
      <div className="cards" style={{ '--i': 1 } as CSSProperties}>
        {cards.map(({ flag, label, description }) => (
          <label key={flag} className="card experimental-flag">
            <input
              type="checkbox"
              checked={flags[flag]}
              onChange={(event) => setFlag(flag, event.target.checked)}
            />
            <span className="toggle__track" aria-hidden="true">
              <span className="toggle__thumb" />
            </span>
            <span className="experimental-flag__text">
              <span className="card__title">{label}</span>
              <span className="card__description">{description}</span>
            </span>
          </label>
        ))}
      </div>
    </section>
  )
}
