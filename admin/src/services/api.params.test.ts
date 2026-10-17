import { describe, it, expect } from 'vitest'
import axios from 'axios'
import { apiConfig } from './api'

// The server reads repeated keys (ctx=a&ctx=b); axios' default would send
// ctx[]=a&ctx[]=b, which the server would ignore.
describe('API params serialization', () => {
  it('sends ctx filters as repeated keys', () => {
    const uri = axios.getUri({
      ...apiConfig,
      url: '/summary',
      params: { build_type: 'release', ctx: ['levelId:3', 'roomId:r 1', 'note:a&b=c'] },
    })
    const query = new URLSearchParams(uri.split('?')[1])
    expect(query.getAll('ctx')).toEqual(['levelId:3', 'roomId:r 1', 'note:a&b=c'])
    expect(query.get('build_type')).toBe('release')
    expect(uri).not.toContain('ctx[]')
    expect(uri).not.toContain('ctx%5B%5D')
  })
})
