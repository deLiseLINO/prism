import { describe, expect, it } from 'vitest'
import { draftFromSettings, draftToSettings } from '../renderer/src/components/ModelSettingsModal'

describe('model settings draft', () => {
  it('omits imageInput until the user touches the toggle', () => {
    const untouched = draftFromSettings(undefined, true)
    expect(untouched.imageInput).toBe(true)
    expect(draftToSettings(untouched)).toEqual({})

    const touched = draftToSettings({ ...untouched, imageTouched: true, imageInput: false })
    expect(touched).toEqual({ imageInput: false })
  })

  it('keeps an explicit false override', () => {
    const draft = draftFromSettings({ imageInput: false }, true)
    expect(draft.imageInput).toBe(false)
    expect(draft.imageTouched).toBe(true)
    expect(draftToSettings(draft)).toEqual({ imageInput: false })
  })

  it('omits an empty context field and keeps a set override', () => {
    const draft = draftFromSettings({ contextWindow: 128000, imageInput: true })
    expect(draftToSettings(draft)).toEqual({ contextWindow: 128000, imageInput: true })
    expect(draftToSettings({ ...draft, contextWindow: null })).toEqual({ imageInput: true })
  })

  it('reset keeps a saved image override when the listing is silent', () => {
    const draft = draftFromSettings({ imageInput: true }, false)
    expect(draft.imageTouched).toBe(true)
    expect(draftToSettings(draft)).toEqual({ imageInput: true })
  })
})
