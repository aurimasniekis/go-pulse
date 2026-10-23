import { screen } from '@testing-library/react'
import { renderWithTheme } from '@/test/render'
import { Placeholder } from './Placeholder'

test('placeholder names its milestone', () => {
  renderWithTheme(<Placeholder title="Clients" milestone="M6" />)
  expect(screen.getByRole('heading', { name: 'Clients' })).toBeInTheDocument()
  expect(screen.getByText(/milestone M6/)).toBeInTheDocument()
})
