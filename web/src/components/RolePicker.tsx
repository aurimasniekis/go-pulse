import { Checkbox, Stack } from '@mantine/core'
import { ROLES, roleInfo } from '@/api/auth'

/** Role checkboxes with descriptions. */
export function RolePicker({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  return (
    <Checkbox.Group label="Roles" value={value} onChange={onChange} description="A user without a role can sign in but see nothing.">
      <Stack gap={6} mt={6}>
        {ROLES.map((r) => (
          <Checkbox key={r} value={r} label={r} description={roleInfo[r].description} />
        ))}
      </Stack>
    </Checkbox.Group>
  )
}
