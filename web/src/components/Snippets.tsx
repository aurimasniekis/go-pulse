import { ActionIcon, Code, CopyButton, Tabs, Tooltip } from '@mantine/core'
import { IconCheck, IconCopy } from '@tabler/icons-react'

/** Tabbed copyable code snippets (nothing when there are none). */
export function Snippets({ snippets }: { snippets: Record<string, string> }) {
  const names = Object.keys(snippets)
  if (names.length === 0) return null
  return (
    <Tabs defaultValue={names[0]} keepMounted={false} data-testid="snippets">
      <Tabs.List>
        {names.map((n) => (
          <Tabs.Tab key={n} value={n}>
            {n}
          </Tabs.Tab>
        ))}
      </Tabs.List>
      {names.map((n) => (
        <Tabs.Panel key={n} value={n} pt="xs" pos="relative">
          <Code block style={{ whiteSpace: 'pre', overflowX: 'auto' }}>
            {snippets[n]}
          </Code>
          <CopyButton value={snippets[n]}>
            {({ copied, copy }) => (
              <Tooltip label={copied ? 'Copied' : 'Copy'}>
                <ActionIcon pos="absolute" top={14} right={6} onClick={copy} aria-label={`Copy ${n} snippet`}>
                  {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                </ActionIcon>
              </Tooltip>
            )}
          </CopyButton>
        </Tabs.Panel>
      ))}
    </Tabs>
  )
}
