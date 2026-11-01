export const stateVariant = (state: string): 'default' | 'secondary' | 'destructive' | 'outline' =>
  state === 'enabled' ? 'default' : state.endsWith('failed') || state === 'changed' ? 'destructive' : state === 'absent' || state === 'disabled' ? 'outline' : 'secondary'
