import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Field } from '@/components/field'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { ExtensionDetail } from '@/api/hooks/extensions'

interface Prop {
  type?: string
  minimum?: number
  maximum?: number
  maxLength?: number
}

/** A small form generated from the entry's JSON-schema `properties` (strings and integers only). */
export function ConfigForm({ detail, pending, onSave }: { detail: ExtensionDetail; pending: boolean; onSave: (config: Record<string, unknown>) => void }) {
  const { t } = useTranslation()
  const id = detail.manifest.id
  const props = ((detail.manifest.config_schema?.properties ?? {}) as Record<string, Prop>)
  const keys = Object.keys(props)
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(keys.map((k) => [k, String(detail.config[k] ?? '')])))
  if (keys.length === 0) return null

  const submit = () => {
    const out: Record<string, unknown> = {}
    for (const k of keys) {
      const v = values[k]?.trim() ?? ''
      if (v === '') continue
      out[k] = props[k].type === 'integer' ? Number(v) : v
    }
    onSave(out)
  }
  return (
    <section className="flex flex-col gap-3" aria-label={t('extensions.config.title')}>
      <h3 className="font-medium">{t('extensions.config.title')}</h3>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        {keys.map((k) => {
          const p = props[k]
          return (
            <Field key={k} id={`ext-cfg-${k}`} label={t(`extensions.config.fields.${id}.${k}`, { defaultValue: k })}>
              {(f) => (
                <Input
                  {...f}
                  dir="ltr"
                  type={p.type === 'integer' ? 'number' : 'text'}
                  min={p.minimum}
                  max={p.maximum}
                  maxLength={p.maxLength}
                  value={values[k] ?? ''}
                  onChange={(e) => setValues((s) => ({ ...s, [k]: e.target.value }))}
                />
              )}
            </Field>
          )
        })}
        <div>
          <Button type="submit" disabled={pending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </section>
  )
}
