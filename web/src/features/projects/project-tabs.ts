export const PROJECT_TABS = ['readiness', 'mapping', 'commands', 'protected', 'contract', 'policy', 'profile', 'intake', 'rules', 'skills'] as const
export type ProjectTab = (typeof PROJECT_TABS)[number]
export const isProjectTab = (v: string | null): v is ProjectTab => !!v && (PROJECT_TABS as readonly string[]).includes(v)
