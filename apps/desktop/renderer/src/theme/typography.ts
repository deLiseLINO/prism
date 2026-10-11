// Typography settings: font size range and default.

export const MIN_FONT_SIZE_PX = 11;
export const MAX_FONT_SIZE_PX = 18;
export const DEFAULT_FONT_SIZE_PX = 13;

export const TYPOGRAPHY_KEY = "prism-typography";

export interface TypographyState {
  readonly fontSizePx: number;
}

export const DEFAULT_TYPOGRAPHY: TypographyState = {
  fontSizePx: DEFAULT_FONT_SIZE_PX,
};

export function normalizeFontSizePx(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return DEFAULT_FONT_SIZE_PX;
  return Math.min(MAX_FONT_SIZE_PX, Math.max(MIN_FONT_SIZE_PX, Math.round(value)));
}

// Unknown stored fields (such as the removed UI density) are ignored.
export function parseTypography(raw: string | null | undefined): TypographyState {
  if (!raw) return DEFAULT_TYPOGRAPHY;
  try {
    const value: unknown = JSON.parse(raw);
    const record = typeof value === "object" && value !== null ? (value as Record<string, unknown>) : {};
    return { fontSizePx: normalizeFontSizePx(record.fontSizePx) };
  } catch {
    return DEFAULT_TYPOGRAPHY;
  }
}

export function typographyVariables(state: TypographyState): Record<string, string> {
  return {
    "--ui-scale": String(Math.round((state.fontSizePx / DEFAULT_FONT_SIZE_PX) * 10000) / 10000),
  };
}
