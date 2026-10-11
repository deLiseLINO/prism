import { EXPERIMENTAL_FLAGS, useExperimentalFlags } from '../experimental'
import { bridge } from '../bridge'
import { PageHeader, Section } from '../components/Patterns'

export function ExperimentalView(): JSX.Element {
  const { flags, setFlag } = useExperimentalFlags()

  return (
    <section className="screen" aria-labelledby="h-experimental">
      <PageHeader id="h-experimental" icon="#i-flask" title="Experimental" />
      <Section title="Features">
        <div className="st-card">
          {EXPERIMENTAL_FLAGS.map(({ flag, label }) => (
            <label key={flag} className="experimental-flag">
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
              </span>
            </label>
          ))}
        </div>
      </Section>
    </section>
  )
}
