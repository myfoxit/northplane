// Go-duration validation: free text in Go syntax ("30s", "5m", "1h30m"),
// matching internal/model Duration parsing. Empty is allowed (optional field).
const goDuration = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/

export function isDuration(v: string): boolean {
  return v === '' || goDuration.test(v)
}
