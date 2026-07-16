import { describe, expect, it } from 'vitest'
import { emptyStreamingMessage, reduceChunk, type UIChunk } from './uistream'

const fold = (chunks: UIChunk[]) =>
  chunks.reduce((msg, c) => reduceChunk(msg, c), emptyStreamingMessage())

describe('uistream reducer', () => {
  it('folds a text turn', () => {
    const msg = fold([
      { type: 'start', messageId: 'm1' },
      { type: 'start-step' },
      { type: 'text-start', id: 't0' },
      { type: 'text-delta', id: 't0', delta: 'Hallo ' },
      { type: 'text-delta', id: 't0', delta: 'Welt' },
      { type: 'text-end', id: 't0' },
      { type: 'finish-step' },
      { type: 'finish', finishReason: 'stop' },
    ])
    expect(msg.id).toBe('m1')
    expect(msg.status).toBe('done')
    expect(msg.finishReason).toBe('stop')
    const text = msg.parts.find((p) => p.type === 'text')
    expect(text?.text).toBe('Hallo Welt')
  })

  it('keeps reasoning separate from text', () => {
    const msg = fold([
      { type: 'reasoning-start', id: 'r0' },
      { type: 'reasoning-delta', id: 'r0', delta: 'denke…' },
      { type: 'reasoning-end', id: 'r0' },
      { type: 'text-start', id: 't0' },
      { type: 'text-delta', id: 't0', delta: 'Antwort' },
    ])
    expect(msg.parts.map((p) => p.type)).toEqual(['reasoning', 'text'])
    expect(msg.parts[0]?.text).toBe('denke…')
    expect(msg.parts[1]?.text).toBe('Antwort')
  })

  it('tracks the tool state machine incl. proposal metadata', () => {
    const msg = fold([
      { type: 'tool-input-start', toolCallId: 'c1', toolName: 'create_downtime' },
      { type: 'tool-input-delta', toolCallId: 'c1', inputTextDelta: '{"objectId":' },
      { type: 'tool-input-delta', toolCallId: 'c1', inputTextDelta: '"web01"}' },
      { type: 'tool-input-available', toolCallId: 'c1', toolName: 'create_downtime', input: { objectId: 'web01' } },
      {
        type: 'tool-output-available', toolCallId: 'c1',
        output: { status: 'proposed', actionId: 'a9' },
        toolMetadata: { proposed: true, actionId: 'a9' },
      },
    ])
    const tool = msg.parts.find((p) => p.type === 'dynamic-tool')
    expect(tool?.state).toBe('output-available')
    expect(tool?.input).toEqual({ objectId: 'web01' })
    expect(tool?.proposed).toBe(true)
    expect(tool?.actionId).toBe('a9')
  })

  it('marks tool errors and stream errors without losing parts', () => {
    const msg = fold([
      { type: 'text-start', id: 't0' },
      { type: 'text-delta', id: 't0', delta: 'halb' },
      { type: 'tool-input-start', toolCallId: 'c1', toolName: 'get_alerts' },
      { type: 'tool-output-error', toolCallId: 'c1', errorText: 'permission denied' },
      { type: 'error', errorText: 'provider down' },
    ])
    expect(msg.status).toBe('error')
    expect(msg.error).toBe('provider down')
    expect(msg.parts.find((p) => p.type === 'text')?.text).toBe('halb')
    expect(msg.parts.find((p) => p.type === 'dynamic-tool')?.errorText).toBe('permission denied')
  })

  it('separates parts per step: same stream id after finish-step is a new part', () => {
    const msg = fold([
      { type: 'start-step' },
      { type: 'text-start', id: 'txt_0' },
      { type: 'text-delta', id: 'txt_0', delta: 'Runde 1' },
      { type: 'finish-step' },
      { type: 'start-step' },
      { type: 'text-start', id: 'txt_0' },
      { type: 'text-delta', id: 'txt_0', delta: 'Runde 2' },
      { type: 'finish-step' },
    ])
    const texts = msg.parts.filter((p) => p.type === 'text').map((p) => p.text)
    expect(texts).toEqual(['Runde 1', 'Runde 2'])
  })
})
