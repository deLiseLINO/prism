import { describe, expect, it } from 'vitest'
import { DEFAULT_TYPOGRAPHY, normalizeFontSizePx, parseTypography, typographyVariables } from '../renderer/src/theme/typography'

describe('typography', () => {
  it('clamps the base font size to 11-18 and defaults to 13', () => {
    expect(normalizeFontSizePx(5)).toBe(11)
    expect(normalizeFontSizePx(40)).toBe(18)
    expect(normalizeFontSizePx(14.4)).toBe(14)
    expect(normalizeFontSizePx('x')).toBe(13)
  })

  it('parses the font size and ignores removed fields', () => {
    expect(parseTypography(null)).toEqual(DEFAULT_TYPOGRAPHY)
    expect(parseTypography('{bad')).toEqual(DEFAULT_TYPOGRAPHY)
    expect(parseTypography('{"uiDensity":"spacious","fontSizePx":99}')).toEqual({ fontSizePx: 18 })
    expect(parseTypography('{"fontSizePx":12}')).toEqual({ fontSizePx: 12 })
  })

  it('maps the font size to a zoom', () => {
    expect(typographyVariables({ fontSizePx: 13 })).toEqual({ '--ui-scale': '1' })
    expect(typographyVariables({ fontSizePx: 18 })).toEqual({ '--ui-scale': '1.3846' })
  })
})
