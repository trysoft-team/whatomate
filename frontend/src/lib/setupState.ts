let pending: Promise<boolean> | null = null

async function fetchFromServer(): Promise<boolean> {
  const { setupService } = await import('@/services/api')
  const res = await setupService.status()
  return res.data.data.needed === true
}

/** setupNeeded asks the server once per page load whether first-run setup is pending. */
export function setupNeeded(fetchNeeded: () => Promise<boolean> = fetchFromServer): Promise<boolean> {
  if (!pending) pending = fetchNeeded().catch(() => false)
  return pending
}

export function resetSetupNeeded(): void {
  pending = null
}

/** setupRedirect returns where to send a navigation, or null to let it through. */
export function setupRedirect(needed: boolean, routeName: unknown): string | null {
  if (needed && routeName !== 'setup') return '/setup'
  if (!needed && routeName === 'setup') return '/login'
  return null
}
