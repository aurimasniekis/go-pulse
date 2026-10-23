import { fireEvent, screen } from '@testing-library/react'
import { renderWithTheme } from '@/test/render'
import { ConfirmModal } from './ConfirmModal'

test('requires typing the target name', () => {
  const onConfirm = vi.fn()
  renderWithTheme(<ConfirmModal opened onClose={() => {}} onConfirm={onConfirm} title="Delete stream X" confirmText="ORDERS" />)
  const button = screen.getByRole('button', { name: 'Delete' })
  expect(button).toBeDisabled()
  fireEvent.change(screen.getByLabelText('confirmation'), { target: { value: 'ORDER' } })
  expect(button).toBeDisabled()
  fireEvent.change(screen.getByLabelText('confirmation'), { target: { value: 'ORDERS' } })
  expect(button).toBeEnabled()
  fireEvent.click(button)
  expect(onConfirm).toHaveBeenCalledOnce()
})
