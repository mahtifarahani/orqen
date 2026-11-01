import { useState } from 'react'
import { Input } from '@/components/ui/input'

/**
 * Keeps half-typed input local and commits only whole positive numbers (or `undefined` when cleared).
 * Re-mounts from `value` whenever the committed value changes from outside.
 */
export function NumberField({
  value,
  onCommit,
  min = 1,
  max,
  disabled,
  ...aria
}: {
  value: number | undefined
  onCommit: (n: number | undefined) => void
  min?: number
  max?: number
  disabled?: boolean
  id?: string
  'aria-label'?: string
  'aria-describedby'?: string
}) {
  const [text, setText] = useState(value === undefined ? '' : String(value))
  const [seen, setSeen] = useState(value)
  if (value !== seen) {
    setSeen(value)
    setText(value === undefined ? '' : String(value))
  }
  return (
    <Input
      {...aria}
      type="text"
      inputMode="numeric"
      dir="ltr"
      className="w-28"
      disabled={disabled}
      value={text}
      onChange={(e) => {
        const raw = e.target.value
        setText(raw)
        if (raw.trim() === '') return onCommit(undefined)
        const n = Number(raw)
        if (Number.isInteger(n) && n >= min && (max === undefined || n <= max)) onCommit(n)
      }}
      onBlur={() => setText(value === undefined ? '' : String(value))}
    />
  )
}
