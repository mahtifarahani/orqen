import { cn } from '@/lib/utils'

export function BrandLogo({ compact = false, className }: { compact?: boolean; className?: string }) {
  return (
    <img
      src={compact ? '/orqen-mark.png' : '/orqen-logo.png'}
      alt="Orqen"
      width={compact ? 40 : 120}
      height={40}
      className={cn(
        'block h-10 shrink-0 object-contain dark:brightness-0 dark:invert',
        compact ? 'w-10 brightness-0' : 'w-30',
        className,
      )}
    />
  )
}
