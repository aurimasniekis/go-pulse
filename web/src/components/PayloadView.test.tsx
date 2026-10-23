import { fireEvent, screen } from '@testing-library/react'
import { renderWithTheme } from '@/test/render'
import { PayloadView } from './PayloadView'

test('pretty-prints JSON and switches to hex', () => {
  renderWithTheme(<PayloadView data='{"door":"front"}' encoding="utf8" contentType="application/json" />)
  expect(screen.getByTestId('payload').textContent).toBe('{\n  "door": "front"\n}')
  fireEvent.click(screen.getByText('Hex'))
  expect(screen.getByTestId('payload').textContent).toContain('7b 22 64 6f 6f 72')
})

test('binary payloads open in hex mode without a text option', () => {
  renderWithTheme(<PayloadView data="AAEC/w==" encoding="base64" />)
  expect(screen.queryByText('Text')).toBeNull()
  expect(screen.getByTestId('payload').textContent).toContain('00 01 02 ff')
  expect(screen.getByText('4 bytes')).toBeInTheDocument()
})
