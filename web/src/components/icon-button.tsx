import { useState, type ComponentProps } from 'react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'

type IconButtonProps = Omit<ComponentProps<typeof Button>, 'size' | 'aria-label'> & {
  'aria-label': string
  size?: 'icon' | 'icon-xs' | 'icon-sm' | 'icon-lg'
  /** Modal close buttons receive automatic focus; show their hint on hover only. */
  tooltipOnFocus?: boolean
}

/** Keep the visible tooltip and accessible name in sync for icon-only actions. */
export function IconButton({ size = 'icon', disabled, tooltipOnFocus = true, ...props }: IconButtonProps) {
  const [open, setOpen] = useState(false)
  const button = <Button {...props} size={size} disabled={disabled} />

  return (
    <TooltipProvider>
      <Tooltip open={props['aria-expanded'] === true ? false : open} onOpenChange={setOpen}>
        <TooltipTrigger asChild onFocus={(event) => { if (!tooltipOnFocus) event.preventDefault() }}>
          {disabled ? <span className="inline-flex shrink-0">{button}</span> : button}
        </TooltipTrigger>
        <TooltipContent sideOffset={6}>{props['aria-label']}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
