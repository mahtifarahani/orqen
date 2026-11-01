import type { ProjectDto } from '@/api/generated/model'
import { RulesPanel } from '@/features/rules/rules-panel'

export function ProjectRulesTab({ orgId, project }: { orgId: string; project: ProjectDto }) {
  return <RulesPanel orgId={orgId} projectId={project.id} />
}
