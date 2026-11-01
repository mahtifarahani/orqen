import { CheckIcon, CircleHelpIcon, TriangleAlertIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { CatherdRungDto } from '@/api/generated/model'
import { Badge } from '@/components/ui/badge'
import { Ltr } from '@/components/ltr'

/** Glyph chips: ✓ usable, ≈ treated like X, ? unscored, plus tier and the not-offered warning. Text always accompanies the glyph. */
export function RungChips({ rung }: { rung: CatherdRungDto }) {
  const { t } = useTranslation()
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      {rung.availability === 'available' && rung.scored && (
        <Badge variant="outline" className="border-emerald-600/40 text-emerald-800 dark:text-emerald-300">
          <CheckIcon aria-hidden />
          {t('catalog.chips.usable')}
        </Badge>
      )}
      {rung.treat_like && (
        <Badge variant="secondary">
          <span aria-hidden>≈</span>
          <span>{t('catalog.chips.treatedLike')}</span>
          <Ltr className="font-sans">{rung.treat_like.like}</Ltr>
          <span className="text-muted-foreground">({t(`catalog.chips.source.${rung.treat_like.source}`)})</span>
        </Badge>
      )}
      {!rung.scored && (
        <Badge variant="outline">
          <CircleHelpIcon aria-hidden />
          {t('catalog.chips.unscored')}
        </Badge>
      )}
      {rung.inferred_only && rung.scored && <Badge variant="outline">{t('catalog.chips.inferredOnly')}</Badge>}
      {rung.cost_tier != null && (
        <Badge variant="outline">
          {t('catalog.chips.costTier')} <span className="tabular-nums">{rung.cost_tier}</span>
        </Badge>
      )}
      {rung.availability === 'not_offered' && (
        <Badge variant="outline" className="border-amber-500/60 text-amber-800 dark:text-amber-300">
          <TriangleAlertIcon aria-hidden />
          {t('catalog.chips.notOffered')}
        </Badge>
      )}
    </span>
  )
}
