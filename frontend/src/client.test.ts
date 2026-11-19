import { describe, expect, it } from 'vitest'
import { formatBytes, unwrap } from './client'

describe('API response', () => {
  it('returns successful data and forwards human-readable errors', () => {
    expect(unwrap({ data: [] })).toEqual([])
    expect(() => unwrap({ error: { message: 'Таймаут подписки' } })).toThrow(
      'Таймаут подписки',
    )
  })
})

describe('traffic units', () => {
  it('formats counters around scale boundaries', () => {
    expect(formatBytes(0)).toBe('0 Б')
    expect(formatBytes(1024)).toBe('1 КБ')
    expect(formatBytes(1024 ** 3)).toBe('1 ГБ')
  })
})
