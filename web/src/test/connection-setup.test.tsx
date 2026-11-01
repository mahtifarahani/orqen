import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ConnectionSetupGuide } from '@/features/connections/connection-setup-guide'
import { ConnectionFormDialog } from '@/features/connections/connection-form-dialog'
import { ORG, renderRoute, stubApi } from './helpers'

afterEach(() => vi.unstubAllGlobals())

describe('Connection setup', () => {
  it('keeps hints hidden until their help button is clicked and allows closing them', async () => {
    const user = userEvent.setup()
    renderRoute(<ConnectionFormDialog orgId={ORG} open onOpenChange={vi.fn()} />, '/', '/')
    const name = screen.getByRole('textbox', { name: 'Name' })
    expect(name).not.toHaveAttribute('aria-describedby')
    expect(screen.queryByText(/Choose an internal label/)).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Guide for your selection' })).not.toBeInTheDocument()
    const help = screen.getByRole('button', { name: 'Name — Help' })
    await user.click(help)
    expect(help).toHaveAttribute('aria-expanded', 'true')
    expect(name).toHaveAttribute('aria-describedby', 'con-name-hint')
    expect(screen.getByText(/Choose an internal label/)).toBeVisible()
    await user.click(help)
    expect(screen.queryByText(/Choose an internal label/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Authentication — Help' }))
    expect(screen.getByRole('region', { name: 'Guide for your selection' })).toBeVisible()
    expect(screen.getByRole('link', { name: 'Open the official instructions' })).toBeVisible()
  })

  it('updates credential instructions and support limitations for the selection', () => {
    const { rerender } = render(<ConnectionSetupGuide provider="gitlab" deployment="self_managed" authMethod="personal_access_token" />)
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://docs.gitlab.com/user/profile/personal_access_tokens/')
    expect(screen.getByText(/start with read_api/)).toBeInTheDocument()
    rerender(<ConnectionSetupGuide provider="jira" deployment="cloud" authMethod="personal_access_token" />)
    expect(screen.getByText(/not supported by Orqen/)).toBeInTheDocument()
    rerender(<ConnectionSetupGuide provider="jira" deployment="cloud" authMethod="api_token" />)
    expect(screen.queryByText(/not supported by Orqen/)).not.toBeInTheDocument()
    expect(screen.getByRole('link')).toHaveAttribute('href', expect.stringContaining('manage-api-tokens'))
    rerender(<ConnectionSetupGuide provider="figma" deployment="cloud" authMethod="oauth" />)
    expect(screen.getByText(/checks are not implemented yet/)).toBeInTheDocument()
    expect(screen.getByText(/does not start an authorization flow/)).toBeInTheDocument()
  })

  it('collects and submits the account email required for Jira Cloud API tokens', async () => {
    const calls = stubApi((call) => call.method === 'POST' ? { status: 201, body: { id: 'con_new' } } : undefined)
    const onOpenChange = vi.fn()
    const user = userEvent.setup()
    renderRoute(<ConnectionFormDialog orgId={ORG} open onOpenChange={onOpenChange} />, '/', '/')
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'Team Jira')
    await user.click(screen.getByRole('combobox', { name: 'Deployment' }))
    await user.click(screen.getByRole('option', { name: 'Cloud' }))
    await user.click(screen.getByRole('combobox', { name: 'Authentication' }))
    await user.click(screen.getByRole('option', { name: 'API token' }))
    await user.type(screen.getByRole('textbox', { name: 'Account username or email' }), 'user@example.com')
    await user.type(screen.getByRole('textbox', { name: 'Base URL' }), 'https://team.atlassian.net')
    await user.click(screen.getByRole('button', { name: 'Add connection' }))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(calls.find((call) => call.method === 'POST')?.body).toMatchObject({
      provider: 'jira', deployment: 'cloud', auth_method: 'api_token', auth_username: 'user@example.com',
    })
  })
})
