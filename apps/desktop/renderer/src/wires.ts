export const WIRE_OPTIONS: readonly { readonly value: string; readonly label: string }[] = [
  { value: 'chat', label: 'openai-completions' },
  { value: 'responses', label: 'openai-responses' },
  { value: 'messages', label: 'anthropic' },
]

export function wireLabel(value: string): string {
  return WIRE_OPTIONS.find((option) => option.value === value)?.label ?? value
}
