import type { RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useArchiveConnection, type Connection } from '@/api/hooks/connections'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { problemMessage } from '@/lib/problem-message'

export function ArchiveDialog({
  orgId,
  connection,
  open,
  onOpenChange,
  returnFocusRef,
}: {
  orgId: string
  connection: Connection
  open: boolean
  onOpenChange: (open: boolean) => void
  returnFocusRef?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation()
  const archive = useArchiveConnection(orgId, connection.revision)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        onCloseAutoFocus={(e) => {
          if (returnFocusRef?.current) {
            e.preventDefault()
            returnFocusRef.current.focus()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{t('connections.archiveDialog.title')}</DialogTitle>
          <DialogDescription>{t('connections.archiveDialog.body', { name: connection.name })}</DialogDescription>
        </DialogHeader>
        {archive.isError && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{problemMessage(t, archive.error)}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="destructive"
            disabled={archive.isPending}
            onClick={() =>
              archive.mutate(
                { orgId, conId: connection.id },
                {
                  onSuccess: () => {
                    toast.success(t('connections.archiveDialog.archived'))
                    onOpenChange(false)
                  },
                },
              )
            }
          >
            {t('connections.archiveDialog.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
