import type { SecurityScanDto } from '@/api/generated/model'

export const scanActive = (s: SecurityScanDto) => s.state === 'queued' || s.state === 'running'
