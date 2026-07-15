// DualListPicker: the Windows-style transfer control — highlighting + › move,
// double-click to move, the » "move all" / « "remove all" buttons, and the
// add-custom affordance that keeps free-typed references possible.
import { describe, it, expect } from 'vitest'
import { useState } from 'react'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '../test/render'
import { DualListPicker } from './DualListPicker'

function Harness({ initial = [], options }: { initial?: string[]; options: string[] }) {
  const [v, setV] = useState<string[]>(initial)
  return (
    <>
      <DualListPicker value={v} onChange={setV} options={options} />
      <output data-testid="value">{v.join(',')}</output>
    </>
  )
}

const val = () => screen.getByTestId('value').textContent

describe('<DualListPicker />', () => {
  it('moves a highlighted option into the selection with ›', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Harness options={['alice', 'bob', 'carol']} />)
    await user.click(await screen.findByRole('button', { name: 'alice' }))
    await user.click(screen.getByRole('button', { name: '›' }))
    expect(val()).toBe('alice')
    // moved, not duplicated: 'alice' now lives only in the selected pane
    expect(screen.getAllByRole('button', { name: 'alice' })).toHaveLength(1)
  })

  it('double-click moves an item immediately', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Harness options={['alice', 'bob']} />)
    await user.dblClick(await screen.findByRole('button', { name: 'bob' }))
    expect(val()).toBe('bob')
  })

  it('» moves all available options and « removes all selected', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Harness options={['a', 'b', 'c']} />)
    await user.click(await screen.findByRole('button', { name: '»' }))
    expect(val()).toBe('a,b,c')
    await user.click(screen.getByRole('button', { name: '«' }))
    expect(val()).toBe('')
  })

  it('adds a custom value typed into the available filter (Enter)', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Harness options={['alice']} />)
    const availFilter = (await screen.findAllByRole('textbox'))[0]! // available pane is first
    await user.type(availFilter, 'zoe{Enter}')
    expect(val()).toBe('zoe')
  })

  it('does not double-add an already-selected value', async () => {
    const user = userEvent.setup()
    renderWithProviders(<Harness initial={['alice']} options={['alice', 'bob']} />)
    // 'alice' is already selected, so it must not appear in the available pane
    const availFilter = (await screen.findAllByRole('textbox'))[0]!
    await user.type(availFilter, 'alice{Enter}')
    expect(val()).toBe('alice')
  })
})
