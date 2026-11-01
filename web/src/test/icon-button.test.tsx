import { createRef } from 'react'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { IconButton } from '@/components/icon-button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'

describe('IconButton', () => {
  it('shows the action on keyboard focus and preserves the button ref and click handler', async () => {
    const user = userEvent.setup()
    const onClick = vi.fn()
    const ref = createRef<HTMLButtonElement>()
    render(<IconButton ref={ref} aria-label="Edit command" onClick={onClick}><svg /></IconButton>)

    await user.tab()
    expect(await screen.findByRole('tooltip')).toHaveTextContent('Edit command')
    expect(ref.current).toBe(screen.getByRole('button', { name: 'Edit command' }))
    await user.keyboard('{Enter}')
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('explains disabled actions on hover without activating them', async () => {
    const user = userEvent.setup()
    const onClick = vi.fn()
    render(<IconButton disabled aria-label="Move up" onClick={onClick}><svg /></IconButton>)
    const button = screen.getByRole('button', { name: 'Move up' })
    await user.hover(button.parentElement!)
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    expect(await screen.findByRole('tooltip', {}, { timeout: 2500 })).toHaveTextContent('Move up')
    expect(button).toBeDisabled()
    await user.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('preserves menu trigger composition', async () => {
    const user = userEvent.setup()
    render(
      <DropdownMenu>
        <DropdownMenuTrigger asChild><IconButton aria-label="Theme"><svg /></IconButton></DropdownMenuTrigger>
        <DropdownMenuContent><DropdownMenuItem>Dark</DropdownMenuItem></DropdownMenuContent>
      </DropdownMenu>,
    )
    const trigger = screen.getByRole('button', { name: 'Theme' })
    await user.click(trigger)
    expect(await screen.findByRole('menuitem', { name: 'Dark' })).toBeVisible()
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  })

  it('preserves icon links without nesting interactive elements', () => {
    render(<IconButton asChild aria-label="Open issue"><a href="/issue" aria-label="Open issue"><svg /></a></IconButton>)
    expect(screen.getByRole('link', { name: 'Open issue' })).toHaveAttribute('href', '/issue')
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})
