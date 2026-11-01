import type { ProjectDto } from '@/api/generated/model'
import { SkillBindingsEditor } from '@/features/skills/skill-bindings-editor'

/** The skills of one project: its own bindings override the org set, shown with the resolved result. */
export function ProjectSkillsTab({ orgId, project }: { orgId: string; project: ProjectDto }) {
  return <SkillBindingsEditor orgId={orgId} scopeKey={`project:${project.id}`} projectId={project.id} />
}
