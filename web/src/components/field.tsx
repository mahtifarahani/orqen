import { useState, type ReactNode } from 'react'
import { CircleHelpIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Label } from '@/components/ui/label'
import { IconButton } from '@/components/icon-button'

interface ControlProps {
  id: string
  'aria-describedby'?: string
  'aria-invalid'?: boolean
}

/** Label + control + hint + error, wired together with htmlFor / aria-describedby. */
export function Field({
  id,
  label,
  hint,
  error,
  collapsibleHint = false,
  children,
}: {
  id: string
  label: string
  hint?: ReactNode
  collapsibleHint?: boolean
  error?: string
  children: (props: ControlProps) => ReactNode
}) {
  const { t } = useTranslation()
  const [hintOpen, setHintOpen] = useState(false)
  const showHint = !!hint && (!collapsibleHint || hintOpen)
  const describedBy = [showHint ? `${id}-hint` : null, error ? `${id}-error` : null].filter(Boolean).join(' ')
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-1.5">
        <Label htmlFor={id}>{label}</Label>
        {hint && collapsibleHint && (
          <IconButton
            variant="ghost"
            size="icon-xs"
            type="button"
            aria-label={`${label} — ${t('common.help')}`}
            aria-expanded={hintOpen}
            aria-controls={hintOpen ? `${id}-hint` : undefined}
            onClick={() => setHintOpen((open) => !open)}
            className="inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
          >
            <CircleHelpIcon className="size-3.5" aria-hidden="true" />
          </IconButton>
        )}
      </div>
      {children({
        id,
        'aria-describedby': describedBy || undefined,
        'aria-invalid': error ? true : undefined,
      })}
      {showHint && (
        <div id={`${id}-hint`} className="text-xs text-muted-foreground">
          {hint}
        </div>
      )}
      {error && (
        <p id={`${id}-error`} className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
