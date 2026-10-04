import type { CSSProperties } from 'react'
import { EXPERIMENTAL_FLAGS, useExperimentalFlags } from '../experimental'
import { bridge } from '../bridge'

export function ExperimentalView(): JSX.Element {
  const { flags, setFlag } = useExperimentalFlags()

  return (
    <section className="screen" aria-labelledby="h-experimental">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-experimental">Experimental</h1>
          <p className="sub">unfinished features, off by default</p>
        </div>
      </div>
      <div className="cards" style={{ '--i': 1 } as CSSProperties}>
        {EXPERIMENTAL_FLAGS.map(({ flag, label, description }) => (
          <label key={flag} className="card experimental-flag">
            <input
              type="checkbox"
              checked={flags[flag]}
              onChange={(event) => {
                const on = event.target.checked
                setFlag(flag, on)
                if (flag === 'rcChannel') void bridge.updater.setRcChannel(on)
              }}
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
