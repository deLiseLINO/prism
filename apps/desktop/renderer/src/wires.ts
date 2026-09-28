export const WIRE_OPTIONS: readonly { readonly value: string; readonly label: string }[] = [
  { value: 'chat', label: 'openai-completions' },
  { value: 'responses', label: 'openai-responses' },
  { value: 'messages', label: 'anthropic' },
  { value: 'cline', label: 'cline' },
]

export function wireLabel(value: string): string {
  return WIRE_OPTIONS.find((option) => option.value === value)?.label ?? value
}

export type OverrideWire = 'responses' | 'chat' | 'messages'

export const OVERRIDE_WIRES: readonly { readonly value: OverrideWire; readonly label: string }[] = WIRE_OPTIONS.filter(
  (option): option is { readonly value: OverrideWire; readonly label: string } => option.value !== 'cline',
)

export function isOverridableWire(value: string): boolean {
  return OVERRIDE_WIRES.some((wire) => wire.value === value)
}
