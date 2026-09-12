import type { CSSProperties } from 'react'
import { Card, Toggle } from '../components/Ui'
import { EXPERIMENTAL_FLAGS, useExperimentalFlags } from '../experimental'

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
          <Card key={flag} title={label} description={description}>
            <Toggle
              checked={flags[flag]}
              onChange={(next) => setFlag(flag, next)}
              label={`Enable ${label}`}
            />
          </Card>
        ))}
      </div>
    </section>
  )
}
