import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/** Isolates URLs, IDs, code and commands as left-to-right text inside RTL layouts. */
export function Ltr({ className, ...props }: ComponentProps<'bdi'>) {
  return <bdi dir="ltr" className={cn('font-mono', className)} {...props} />
}
