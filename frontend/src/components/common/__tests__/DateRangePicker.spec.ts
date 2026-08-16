import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'

import DateRangePicker from '../DateRangePicker.vue'

const messages: Record<string, string> = {
  'dates.today': 'Today',
  'dates.yesterday': 'Yesterday',
  'dates.last24Hours': 'Last 24 Hours',
  'dates.last7Days': 'Last 7 Days',
  'dates.last14Days': 'Last 14 Days',
  'dates.last30Days': 'Last 30 Days',
  'dates.thisMonth': 'This Month',
  'dates.lastMonth': 'Last Month',
  'dates.startDate': 'Start Date',
  'dates.endDate': 'End Date',
  'dates.apply': 'Apply',
  'dates.selectDateRange': 'Select date range'
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
    locale: ref('en')
  })
}))

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

const formatLocalDateTime = (date: Date): string =>
  `${formatLocalDate(date)}T${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`

const dayStartOffset = (n: number): Date => {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  d.setDate(d.getDate() + n)
  return d
}

describe('DateRangePicker', () => {
  it('uses last 24 hours as the default recognized preset', () => {
    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: formatLocalDate(yesterday),
        endDate: formatLocalDate(now)
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('Last 24 Hours')
  })

  it('emits range updates with last24Hours preset when applied', async () => {
    const now = new Date()
    const today = formatLocalDate(now)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: today,
        endDate: today
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const presetButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Last 24 Hours')
    )
    expect(presetButton).toBeDefined()

    await presetButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    const nowAfterClick = new Date()
    const yesterdayAfterClick = new Date(nowAfterClick.getTime() - 24 * 60 * 60 * 1000)
    const expectedStart = formatLocalDate(yesterdayAfterClick)
    const expectedEnd = formatLocalDate(nowAfterClick)

    expect(wrapper.emitted('update:startDate')?.[0]).toEqual([expectedStart])
    expect(wrapper.emitted('update:endDate')?.[0]).toEqual([expectedEnd])
    expect(wrapper.emitted('change')?.[0]).toEqual([
      {
        startDate: expectedStart,
        endDate: expectedEnd,
        preset: 'last24Hours'
      }
    ])
  })

  it('renders date inputs by default and datetime-local inputs with withTime', async () => {
    const plain = mount(DateRangePicker, {
      props: { startDate: '2026-08-20', endDate: '2026-08-21' },
      global: { stubs: { Icon: true } }
    })
    await plain.find('.date-picker-trigger').trigger('click')
    expect(plain.find('input').attributes('type')).toBe('date')

    const withTime = mount(DateRangePicker, {
      props: { startDate: '2026-08-20T10:00', endDate: '2026-08-21T15:00', withTime: true },
      global: { stubs: { Icon: true } }
    })
    await withTime.find('.date-picker-trigger').trigger('click')
    expect(withTime.find('input').attributes('type')).toBe('datetime-local')
  })

  it('normalizes pure-date props to datetime-local format when withTime', async () => {
    const wrapper = mount(DateRangePicker, {
      props: { startDate: '2026-08-20', endDate: '2026-08-21', withTime: true },
      global: { stubs: { Icon: true } }
    })
    await wrapper.find('.date-picker-trigger').trigger('click')
    const inputs = wrapper.findAll('input')
    expect((inputs[0].element as HTMLInputElement).value).toBe('2026-08-20T00:00')
    expect((inputs[1].element as HTMLInputElement).value).toBe('2026-08-21T00:00')
  })

  it('emits full-day datetime range for today preset when withTime', async () => {
    const wrapper = mount(DateRangePicker, {
      props: { startDate: '2026-08-20T10:00', endDate: '2026-08-21T15:00', withTime: true },
      global: { stubs: { Icon: true } }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const presetButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Today')
    )
    expect(presetButton).toBeDefined()

    await presetButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    expect(wrapper.emitted('change')?.[0]).toEqual([
      {
        startDate: formatLocalDateTime(dayStartOffset(0)),
        endDate: formatLocalDateTime(dayStartOffset(1)),
        preset: 'today'
      }
    ])
  })
})
